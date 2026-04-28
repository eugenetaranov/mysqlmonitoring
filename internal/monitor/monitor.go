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

// queryTimeout caps each individual monitoring query. When the deadline fires,
// go-sql-driver/mysql issues KILL QUERY on a separate connection so the server
// stops the work — without this, server-side queries can outlive the client and
// pile up across reconnects.
const queryTimeout = 5 * time.Second

// Snapshot takes a single point-in-time snapshot.
func (m *Monitor) Snapshot(ctx context.Context) Result {
	snap := db.Snapshot{Time: time.Now()}

	infoCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	info, err := m.database.ServerInfo(infoCtx)
	cancel()
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("server info: %w", err)}
	}
	snap.ServerInfo = info

	txnCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	snap.Transactions, err = m.database.Transactions(txnCtx)
	cancel()
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("transactions: %w", err)}
	}

	lwCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	snap.LockWaits, err = m.database.LockWaits(lwCtx)
	cancel()
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("lock waits: %w", err)}
	}

	procCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	snap.Processes, err = m.database.Processes(procCtx)
	cancel()
	if err != nil {
		return Result{Snapshot: snap, Error: fmt.Errorf("processes: %w", err)}
	}

	mlCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	snap.MetadataLocks, err = m.database.MetadataLocks(mlCtx)
	cancel()
	if err != nil {
		// Non-fatal, metadata locks may not be available
		snap.MetadataLocks = nil
	}

	innoCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	snap.InnoDBStatus, _ = m.database.InnoDBStatus(innoCtx)
	cancel()

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
				// If Snapshot ran longer than the interval, drop the queued
				// tick instead of immediately firing another poll.
				select {
				case <-ticker.C:
				default:
				}
			}
		}
	}()

	return ch
}
