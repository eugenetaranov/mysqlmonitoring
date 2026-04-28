package explain

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// Source is the narrow interface explain.Engine needs from the
// database: it must be able to fetch a recent example for a digest
// and run an EXPLAIN against arbitrary SQL.
type Source interface {
	RecentExample(ctx context.Context, digest string) (db.Example, error)
	ExplainJSON(ctx context.Context, sql, schema string) (string, error)
}

// FlipEvent is recorded when a digest's plan_hash changes.
type FlipEvent struct {
	Digest        string
	PriorPlanHash string
	NewPlanHash   string
	Time          time.Time
}

// Result is what Engine.Run returns to the caller.
type Result struct {
	Digest      string
	Skipped     bool
	SkipReason  string
	Example     db.Example
	PlanJSON    string
	PlanText    string
	PlanHash    string
	RedFlags    []RedFlag
	Flipped     bool
	PriorHash   string
	FromCache   bool
	GeneratedAt time.Time
}

// Engine runs EXPLAIN-on-demand for digests with a small in-memory
// plan cache and emits FlipEvents when a digest's plan changes.
type Engine struct {
	src Source

	mu     sync.Mutex
	cache  map[string]planEntry // keyed by digest
	flips  []FlipEvent
}

type planEntry struct {
	Hash      string
	Rendered  string
	JSON      string
	Flags     []RedFlag
	Example   db.Example
	UpdatedAt time.Time
}

// New constructs an Engine wired to src.
func New(src Source) *Engine {
	return &Engine{
		src:   src,
		cache: make(map[string]planEntry),
	}
}

// Run produces a Result for digest. The full pipeline is:
// (1) fetch a recent example; (2) refuse if it is not a SELECT;
// (3) run EXPLAIN with the supplied context's deadline (5s default);
// (4) parse, hash, and render the plan; (5) update the cache and
// record a flip if the hash changed.
func (e *Engine) Run(ctx context.Context, digest string) (Result, error) {
	if digest == "" {
		return Result{}, errors.New("empty digest")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	now := time.Now()
	res := Result{Digest: digest, GeneratedAt: now}

	example, err := e.src.RecentExample(ctx, digest)
	if err != nil {
		return res, fmt.Errorf("recent example: %w", err)
	}
	res.Example = example

	if example.SQLText == "" {
		res.Skipped = true
		res.SkipReason = "no recent example for digest; enable consumer events_statements_history_long"
		return res, nil
	}

	if !SafeVerb(example.SQLText) {
		res.Skipped = true
		res.SkipReason = fmt.Sprintf("EXPLAIN skipped for safety: leading verb %q is not SELECT/WITH", VerbFor(example.SQLText))
		return res, nil
	}

	planJSON, err := e.src.ExplainJSON(ctx, example.SQLText, example.Schema)
	if err != nil {
		return res, fmt.Errorf("explain: %w", err)
	}

	flags, rendered, err := AnalyzePlan(planJSON)
	if err != nil {
		return res, fmt.Errorf("analyze plan: %w", err)
	}
	hash, err := PlanHash(planJSON)
	if err != nil {
		return res, fmt.Errorf("hash plan: %w", err)
	}

	res.PlanJSON = planJSON
	res.PlanText = rendered
	res.PlanHash = hash
	res.RedFlags = flags

	e.mu.Lock()
	defer e.mu.Unlock()

	if prev, ok := e.cache[digest]; ok && prev.Hash == hash {
		res.FromCache = true
		res.PriorHash = prev.Hash
		// Refresh the timestamp so the entry remains warm.
		prev.UpdatedAt = now
		e.cache[digest] = prev
		return res, nil
	}

	if prev, ok := e.cache[digest]; ok && prev.Hash != hash {
		res.Flipped = true
		res.PriorHash = prev.Hash
		e.flips = append(e.flips, FlipEvent{
			Digest:        digest,
			PriorPlanHash: prev.Hash,
			NewPlanHash:   hash,
			Time:          now,
		})
	}

	e.cache[digest] = planEntry{
		Hash: hash, Rendered: rendered, JSON: planJSON, Flags: flags, Example: example, UpdatedAt: now,
	}
	return res, nil
}

// Flips returns a copy of every recorded FlipEvent.
func (e *Engine) Flips() []FlipEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]FlipEvent, len(e.flips))
	copy(out, e.flips)
	return out
}
