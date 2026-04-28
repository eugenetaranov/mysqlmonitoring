package insights

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/collector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// Config tunes the perf-insights subsystem.
type Config struct {
	// PollInterval is how often digest and wait deltas are read.
	PollInterval time.Duration
	// CPUSampleInterval is how often events_statements_current is
	// sampled to estimate the CPU class. Should be <= PollInterval.
	CPUSampleInterval time.Duration
	// Window is the in-memory retention horizon. The sample capacity
	// is sized so that a fully populated buffer covers Window.
	Window time.Duration
	// MaxDigests caps the number of distinct digests held in memory.
	MaxDigests int
	// SessionCapacity caps the per-session sample buffer used for
	// per-(digest, app) breakdowns.
	SessionCapacity int
	// NewDigestProtection delays evicting brand-new digests so they
	// have a chance to accumulate load.
	NewDigestProtection time.Duration
}

// DefaultConfig matches the design.md "M1 lean" profile.
func DefaultConfig() Config {
	return Config{
		PollInterval:        10 * time.Second,
		CPUSampleInterval:   1 * time.Second,
		Window:              1 * time.Hour,
		MaxDigests:          2000,
		SessionCapacity:     8192,
		NewDigestProtection: 30 * time.Second,
	}
}

// Insights holds all the in-memory series for the perf-insights
// subsystem and runs the collectors that feed them.
type Insights struct {
	cfg Config
	src db.PerfInsightsDB

	Registry *series.Registry
	Waits    *collector.WaitSeries
	Sessions *series.RingSink[series.SessionSample]

	digest *collector.DigestCollector
	waits  *collector.WaitCollector
	cpu    *collector.CPUSampler

	mu           sync.Mutex
	caps         db.PerfCapabilities
	probed       bool
	digestErrors uint64
	waitErrors   uint64
	cpuErrors    uint64
}

// New constructs an Insights with cfg and source. The series and
// collectors are initialised eagerly so callers can read empty
// buffers before Run is invoked.
func New(cfg Config, src db.PerfInsightsDB) *Insights {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultConfig().PollInterval
	}
	if cfg.CPUSampleInterval <= 0 {
		cfg.CPUSampleInterval = DefaultConfig().CPUSampleInterval
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultConfig().Window
	}
	if cfg.SessionCapacity <= 0 {
		cfg.SessionCapacity = DefaultConfig().SessionCapacity
	}

	// Sample capacity = ceil(window / interval) + 1 slack.
	sampleCap := int(cfg.Window/cfg.PollInterval) + 1
	if sampleCap < 8 {
		sampleCap = 8
	}

	registry := series.NewRegistry(series.RegistryConfig{
		MaxDigests:          cfg.MaxDigests,
		SampleCapacity:      sampleCap,
		NewDigestProtection: cfg.NewDigestProtection,
	})
	waits := collector.NewWaitSeries(sampleCap)
	sessions := series.NewRingSink[series.SessionSample](cfg.SessionCapacity)

	return &Insights{
		cfg:      cfg,
		src:      src,
		Registry: registry,
		Waits:    waits,
		Sessions: sessions,
		digest:   collector.NewDigestCollector(src, registry),
		waits: collector.NewWaitCollector(src, waits),
		cpu: collector.NewCPUSampler(src, waits, sessions, time.Now()),
	}
}

// Probe runs the capability probe once and writes any human-readable
// warnings to warn. Subsequent calls return the cached capability set
// without re-probing.
func (i *Insights) Probe(ctx context.Context, warn io.Writer) error {
	i.mu.Lock()
	if i.probed {
		caps := i.caps
		i.mu.Unlock()
		_ = caps
		return nil
	}
	i.mu.Unlock()

	caps, err := i.src.ProbeCapabilities(ctx)
	if err != nil {
		return fmt.Errorf("probe perf-insights capabilities: %w", err)
	}

	i.mu.Lock()
	i.caps = caps
	i.probed = true
	i.mu.Unlock()

	if warn != nil {
		for _, w := range caps.Warnings {
			fmt.Fprintln(warn, "perf-insights:", w)
		}
	}
	return nil
}

// Capabilities returns the most recently probed capability set.
func (i *Insights) Capabilities() db.PerfCapabilities {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.caps
}

// Run launches the digest, wait and CPU collectors as goroutines and
// blocks until ctx is cancelled. Errors from individual polls are
// counted but never abort Run — perf insights is best-effort, and a
// transient performance_schema error must not take the lock monitor
// with it.
func (i *Insights) Run(ctx context.Context) {
	caps := i.Capabilities()

	var wg sync.WaitGroup

	if caps.DigestAvailable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i.runDigestLoop(ctx)
		}()
	}
	if caps.WaitsAvailable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i.runWaitLoop(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			i.runCPULoop(ctx)
		}()
	}

	wg.Wait()
}

func (i *Insights) runDigestLoop(ctx context.Context) {
	t := time.NewTicker(i.cfg.PollInterval)
	defer t.Stop()
	if _, err := i.digest.Poll(ctx, time.Now()); err != nil {
		i.bumpErr(&i.digestErrors)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if _, err := i.digest.Poll(ctx, now); err != nil {
				i.bumpErr(&i.digestErrors)
			}
		}
	}
}

func (i *Insights) runWaitLoop(ctx context.Context) {
	t := time.NewTicker(i.cfg.PollInterval)
	defer t.Stop()
	if _, err := i.waits.Poll(ctx, time.Now()); err != nil {
		i.bumpErr(&i.waitErrors)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if _, err := i.waits.Poll(ctx, now); err != nil {
				i.bumpErr(&i.waitErrors)
			}
		}
	}
}

func (i *Insights) runCPULoop(ctx context.Context) {
	sample := time.NewTicker(i.cfg.CPUSampleInterval)
	flush := time.NewTicker(i.cfg.PollInterval)
	defer sample.Stop()
	defer flush.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-sample.C:
			if err := i.cpu.Sample(ctx, t); err != nil {
				i.bumpErr(&i.cpuErrors)
			}
		case t := <-flush.C:
			i.cpu.Flush(t)
		}
	}
}

func (i *Insights) bumpErr(n *uint64) {
	i.mu.Lock()
	*n++
	i.mu.Unlock()
}

// ErrorCounts returns the cumulative error counts across collectors.
type ErrorCounts struct {
	Digest uint64
	Wait   uint64
	CPU    uint64
}

func (i *Insights) ErrorCounts() ErrorCounts {
	i.mu.Lock()
	defer i.mu.Unlock()
	return ErrorCounts{Digest: i.digestErrors, Wait: i.waitErrors, CPU: i.cpuErrors}
}
