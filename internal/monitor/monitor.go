package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
)

// Config holds monitor configuration.
type Config struct {
	Interval             time.Duration
	LongQueryThreshold   time.Duration
	LockWaitThreshold    time.Duration
	CriticalTrxThreshold time.Duration
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Interval:             2 * time.Second,
		LongQueryThreshold:   30 * time.Second,
		LockWaitThreshold:    10 * time.Second,
		CriticalTrxThreshold: 5 * time.Minute,
	}
}

// Result is what the monitor sends on each poll cycle.
type Result struct {
	Snapshot db.Snapshot
	Issues   []detector.Issue
	Error    error
}

// MaxSeverity returns the highest severity among all issues.
func (r Result) MaxSeverity() detector.Severity {
	max := detector.SeverityInfo
	for _, issue := range r.Issues {
		if issue.Severity > max {
			max = issue.Severity
		}
	}
	return max
}

// Monitor polls the database and runs detectors.
type Monitor struct {
	database  db.DB
	detectors []detector.Detector
	config    Config
}

// New creates a new Monitor.
func New(database db.DB, cfg Config) *Monitor {
	detectors := []detector.Detector{
		detector.NewLongTransactionDetector(cfg.LongQueryThreshold, cfg.CriticalTrxThreshold),
		detector.NewLockChainDetector(cfg.LockWaitThreshold),
		detector.NewDDLConflictDetector(cfg.LockWaitThreshold),
		detector.NewDeadlockDetector(),
	}

	return &Monitor{
		database:  database,
		detectors: detectors,
		config:    cfg,
	}
}

// Snapshot takes a single point-in-time snapshot.
func (m *Monitor) Snapshot(ctx context.Context) Result {
	snap := db.Snapshot{Time: time.Now()}

	info, err := m.database.ServerInfo(ctx)
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("server info: %w", err)}
	}
	snap.ServerInfo = info

	snap.Transactions, err = m.database.Transactions(ctx)
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("transactions: %w", err)}
	}

	snap.LockWaits, err = m.database.LockWaits(ctx)
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("lock waits: %w", err)}
	}

	snap.Processes, err = m.database.Processes(ctx)
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("processes: %w", err)}
	}

	snap.MetadataLocks, err = m.database.MetadataLocks(ctx)
	if err != nil {
		// Non-fatal, metadata locks may not be available
		snap.MetadataLocks = nil
	}

	snap.InnoDBStatus, _ = m.database.InnoDBStatus(ctx)

	// Run detectors
	var issues []detector.Issue
	for _, d := range m.detectors {
		issues = append(issues, d.Detect(snap)...)
	}

	return Result{Snapshot: snap, Issues: issues}
}

// Run starts the polling loop, sending results on the returned channel.
// It stops when the context is cancelled.
func (m *Monitor) Run(ctx context.Context) <-chan Result {
	ch := make(chan Result, 1)

	go func() {
		defer close(ch)

		ticker := time.NewTicker(m.config.Interval)
		defer ticker.Stop()

		// Send initial snapshot immediately
		result := m.Snapshot(ctx)
		select {
		case ch <- result:
		case <-ctx.Done():
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result := m.Snapshot(ctx)
				select {
				case ch <- result:
				default:
					// Drop if consumer is slow
				}
			}
		}
	}()

	return ch
}
