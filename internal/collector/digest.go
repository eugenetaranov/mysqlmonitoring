package collector

import (
	"context"
	"sync"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// digestBaseline is the prior poll's monotonic counters for one
// digest. Used to compute deltas on the next poll.
type digestBaseline struct {
	countStar               uint64
	sumTimerWait            uint64
	sumLockTime             uint64
	sumRowsExamined         uint64
	sumRowsSent             uint64
	sumNoIndexUsed          uint64
	sumCreatedTmpDiskTables uint64
	sumSortMergePasses      uint64
	pollTime                time.Time
}

// DigestPollResult summarizes one poll for tests and TUI footer info.
type DigestPollResult struct {
	Time            time.Time
	Interval        time.Duration
	SeenDigests     int
	EmittedSamples  int
	ResetSkips      int // digests whose counters decreased and were reseeded
	BaselineSeeded  int // digests added to the baseline this poll (no sample emitted)
	QueryDurationMs int64
}

// DigestCollector polls performance_schema.events_statements_summary_by_digest
// on each call to Poll, diffs against the prior poll's baseline, and
// appends DigestSamples to the registry.
type DigestCollector struct {
	source   db.PerfInsightsDB
	registry *series.Registry

	mu        sync.Mutex
	baselines map[series.DigestKey]digestBaseline
	lastPoll  time.Time
}

// NewDigestCollector constructs a collector wired to source and registry.
func NewDigestCollector(source db.PerfInsightsDB, registry *series.Registry) *DigestCollector {
	return &DigestCollector{
		source:    source,
		registry:  registry,
		baselines: make(map[series.DigestKey]digestBaseline),
	}
}

// Poll reads the current digest snapshot from source and emits per-digest
// delta samples for digests that were also present at the previous poll.
// The first call to Poll seeds the baseline and emits no samples. Counter
// resets (any monotonic field decreasing) cause that digest's interval
// to be skipped and its baseline reseeded; the rest of the poll proceeds.
func (c *DigestCollector) Poll(ctx context.Context, now time.Time) (DigestPollResult, error) {
	start := time.Now()
	rows, err := c.source.DigestStats(ctx)
	if err != nil {
		return DigestPollResult{Time: now}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	prevPoll := c.lastPoll
	res := DigestPollResult{
		Time:            now,
		SeenDigests:     len(rows),
		QueryDurationMs: time.Since(start).Milliseconds(),
	}

	// On the first poll there is no prior timestamp; we cannot compute
	// per-digest intervals, so we just seed baselines and return.
	firstPoll := prevPoll.IsZero()
	interval := now.Sub(prevPoll)
	res.Interval = interval

	for _, r := range rows {
		key := series.DigestKey{Schema: r.Schema, Digest: r.Digest}
		newBaseline := digestBaseline{
			countStar:               r.CountStar,
			sumTimerWait:            r.SumTimerWait,
			sumLockTime:             r.SumLockTime,
			sumRowsExamined:         r.SumRowsExamined,
			sumRowsSent:             r.SumRowsSent,
			sumNoIndexUsed:          r.SumNoIndexUsed,
			sumCreatedTmpDiskTables: r.SumCreatedTmpDiskTables,
			sumSortMergePasses:      r.SumSortMergePasses,
			pollTime:                now,
		}

		prev, hadPrev := c.baselines[key]

		switch {
		case firstPoll || !hadPrev:
			// New digest — register text and seed baseline. No sample.
			c.registry.Touch(now, key, r.DigestText)
			c.baselines[key] = newBaseline
			res.BaselineSeeded++

		case isCounterReset(prev, newBaseline):
			// Server restart, TRUNCATE TABLE, or eviction inside
			// performance_schema. Drop the interval, reseed baseline.
			c.baselines[key] = newBaseline
			res.ResetSkips++

		default:
			sample := series.DigestSample{
				Time:                         now,
				Interval:                     interval,
				ExecCountDelta:               newBaseline.countStar - prev.countStar,
				SumTimerWaitDelta:            newBaseline.sumTimerWait - prev.sumTimerWait,
				SumLockTimeDelta:             newBaseline.sumLockTime - prev.sumLockTime,
				SumRowsExaminedDelta:         newBaseline.sumRowsExamined - prev.sumRowsExamined,
				SumRowsSentDelta:             newBaseline.sumRowsSent - prev.sumRowsSent,
				SumNoIndexUsedDelta:          newBaseline.sumNoIndexUsed - prev.sumNoIndexUsed,
				SumCreatedTmpDiskTablesDelta: newBaseline.sumCreatedTmpDiskTables - prev.sumCreatedTmpDiskTables,
				SumSortMergePassesDelta:      newBaseline.sumSortMergePasses - prev.sumSortMergePasses,
			}
			// Only emit if at least one execution happened in the interval;
			// otherwise the digest is idle and the sample carries no info.
			if sample.ExecCountDelta > 0 || sample.SumTimerWaitDelta > 0 {
				c.registry.Append(key, r.DigestText, sample)
				res.EmittedSamples++
			}
			c.baselines[key] = newBaseline
		}
	}

	// Drop baselines for digests the server no longer reports (e.g.,
	// purged from the digest table). They will reseed if they reappear.
	if !firstPoll {
		seen := make(map[series.DigestKey]struct{}, len(rows))
		for _, r := range rows {
			seen[series.DigestKey{Schema: r.Schema, Digest: r.Digest}] = struct{}{}
		}
		for k := range c.baselines {
			if _, ok := seen[k]; !ok {
				delete(c.baselines, k)
			}
		}
	}

	c.lastPoll = now
	return res, nil
}

// isCounterReset reports whether any monotonic counter went backward
// between two baselines for the same digest. Picosecond clocks can
// jitter slightly across reads but never decrease for the same row;
// any decrease is treated as a reset.
func isCounterReset(prev, next digestBaseline) bool {
	return next.countStar < prev.countStar ||
		next.sumTimerWait < prev.sumTimerWait ||
		next.sumRowsExamined < prev.sumRowsExamined ||
		next.sumRowsSent < prev.sumRowsSent ||
		next.sumLockTime < prev.sumLockTime
}
