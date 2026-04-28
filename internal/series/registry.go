package series

import (
	"sync"
	"time"
)

// RegistryConfig tunes per-digest tracking limits.
type RegistryConfig struct {
	// MaxDigests is the upper bound on simultaneously tracked digests.
	// When reached, Touch evicts the lowest-load eligible digest before
	// admitting a new one.
	MaxDigests int

	// SampleCapacity is the ring size handed to each digest's sink.
	SampleCapacity int

	// NewDigestProtection is the minimum age a digest must reach before
	// becoming an eviction candidate. This keeps a brand-new digest
	// from being evicted before it has had a chance to accumulate load.
	NewDigestProtection time.Duration
}

// DefaultRegistryConfig matches the design.md defaults.
func DefaultRegistryConfig() RegistryConfig {
	return RegistryConfig{
		MaxDigests:          2000,
		SampleCapacity:      720, // 1h @ 5s, 2h @ 10s
		NewDigestProtection: 30 * time.Second,
	}
}

// DigestEntry is the per-digest state held by the registry: its
// normalized SQL text, when it was first seen, the in-memory series
// of deltas, and a running aggregate of load used for eviction.
type DigestEntry struct {
	Key       DigestKey
	Text      string
	FirstSeen time.Time
	Sink      *RingSink[DigestSample]

	// LoadPicos is the sum of SumTimerWaitDelta across every retained
	// sample. Updated incrementally on Append and on Range eviction.
	LoadPicos uint64
}

// Registry tracks digest entries with a load-aware cap. It is safe
// for concurrent access from one collector and any number of readers.
type Registry struct {
	cfg RegistryConfig

	mu       sync.Mutex
	entries  map[DigestKey]*DigestEntry
	evicted  uint64
}

// NewRegistry builds a registry from cfg. A zero MaxDigests or
// SampleCapacity is replaced with the default for that field.
func NewRegistry(cfg RegistryConfig) *Registry {
	d := DefaultRegistryConfig()
	if cfg.MaxDigests <= 0 {
		cfg.MaxDigests = d.MaxDigests
	}
	if cfg.SampleCapacity <= 0 {
		cfg.SampleCapacity = d.SampleCapacity
	}
	if cfg.NewDigestProtection < 0 {
		cfg.NewDigestProtection = 0
	}
	return &Registry{
		cfg:     cfg,
		entries: make(map[DigestKey]*DigestEntry, cfg.MaxDigests),
	}
}

// Touch records that key was observed at now, with normalized text.
// If the digest is new and the registry is at capacity, the
// lowest-load eligible digest is evicted first. The returned entry's
// sink is ready for sample appends.
func (r *Registry) Touch(now time.Time, key DigestKey, text string) *DigestEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[key]; ok {
		if text != "" && e.Text == "" {
			e.Text = text
		}
		return e
	}

	if len(r.entries) >= r.cfg.MaxDigests {
		r.evictOldestLowestLoadLocked(now)
	}

	e := &DigestEntry{
		Key:       key,
		Text:      text,
		FirstSeen: now,
		Sink:      NewRingSink[DigestSample](r.cfg.SampleCapacity),
	}
	r.entries[key] = e
	return e
}

// Append records s for key, creating the entry if needed.
func (r *Registry) Append(key DigestKey, text string, s DigestSample) {
	e := r.Touch(s.Time, key, text)
	e.Sink.Append(s)
	r.mu.Lock()
	e.LoadPicos += s.SumTimerWaitDelta
	r.mu.Unlock()
}

// Get returns the entry for key, or nil if the digest is not tracked.
func (r *Registry) Get(key DigestKey) *DigestEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[key]
}

// Snapshot returns a copy of every tracked entry for read-only
// consumption (TUI render, top-SQL aggregation, tests). The returned
// slice is decoupled from the registry — sinks are still shared
// pointers so callers see live data.
func (r *Registry) Snapshot() []*DigestEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*DigestEntry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}

// Len returns the number of currently tracked digests.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Evicted returns the cumulative number of digests evicted since the
// registry was created. The TUI footer surfaces this so operators can
// tell when the cap is biting.
func (r *Registry) Evicted() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evicted
}

// evictOldestLowestLoadLocked drops the eligible entry with the
// smallest LoadPicos. Eligibility means FirstSeen is at least
// NewDigestProtection in the past. If no entry is eligible (every
// digest is brand-new), the oldest one is evicted as a fallback.
func (r *Registry) evictOldestLowestLoadLocked(now time.Time) {
	cutoff := now.Add(-r.cfg.NewDigestProtection)

	var (
		victim         *DigestEntry
		victimLoad     uint64
		fallback       *DigestEntry
		fallbackOldest time.Time
	)

	for _, e := range r.entries {
		if fallback == nil || e.FirstSeen.Before(fallbackOldest) {
			fallback = e
			fallbackOldest = e.FirstSeen
		}
		if e.FirstSeen.After(cutoff) {
			continue
		}
		if victim == nil || e.LoadPicos < victimLoad {
			victim = e
			victimLoad = e.LoadPicos
		}
	}

	if victim == nil {
		victim = fallback
	}
	if victim == nil {
		return
	}
	delete(r.entries, victim.Key)
	r.evicted++
}
