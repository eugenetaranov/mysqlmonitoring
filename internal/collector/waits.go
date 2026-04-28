package collector

import (
	"context"
	"sync"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// WaitSeries holds one RingSink per WaitClass. Wait, CPU and any
// future synthetic class share this structure so the TUI sparkline
// can iterate AllWaitClasses and pull a single sink per band.
type WaitSeries struct {
	sinks map[series.WaitClass]*series.RingSink[series.WaitSample]
}

// NewWaitSeries creates one sink of the given capacity per class.
func NewWaitSeries(capacity int) *WaitSeries {
	w := &WaitSeries{sinks: make(map[series.WaitClass]*series.RingSink[series.WaitSample], len(series.AllWaitClasses))}
	for _, c := range series.AllWaitClasses {
		w.sinks[c] = series.NewRingSink[series.WaitSample](capacity)
	}
	return w
}

// Sink returns the sink for class. The result is never nil for any
// class in series.AllWaitClasses.
func (w *WaitSeries) Sink(class series.WaitClass) *series.RingSink[series.WaitSample] {
	return w.sinks[class]
}

// append is the internal helper both wait and CPU paths use to add a
// per-interval sample. Public callers should use Append to make
// intent explicit.
func (w *WaitSeries) Append(s series.WaitSample) {
	if sink, ok := w.sinks[s.Class]; ok {
		sink.Append(s)
	}
}

// waitBaseline mirrors digestBaseline for events_waits_*.
type waitBaseline struct {
	count    uint64
	timeWait uint64
}

// WaitPollResult summarises one wait poll.
type WaitPollResult struct {
	Time         time.Time
	Interval     time.Duration
	EventsSeen   int
	ResetSkips   int
	NewBaselines int
	Emitted      int
}

// WaitCollector polls events_waits_summary_global_by_event_name on
// each Poll, diffs against the prior baseline, buckets per WaitClass,
// and appends one WaitSample per class per interval.
type WaitCollector struct {
	source db.PerfInsightsDB
	series *WaitSeries

	mu        sync.Mutex
	baselines map[string]waitBaseline
	lastPoll  time.Time
}

// NewWaitCollector constructs a collector wired to source and series.
func NewWaitCollector(source db.PerfInsightsDB, series *WaitSeries) *WaitCollector {
	return &WaitCollector{
		source:    source,
		series:    series,
		baselines: make(map[string]waitBaseline),
	}
}

// Poll reads the wait totals, computes per-event deltas, sums them
// into per-class buckets, and emits one WaitSample per non-CPU class.
// The first poll seeds the baseline and emits nothing.
func (c *WaitCollector) Poll(ctx context.Context, now time.Time) (WaitPollResult, error) {
	rows, err := c.source.WaitStats(ctx)
	if err != nil {
		return WaitPollResult{Time: now}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	prevPoll := c.lastPoll
	res := WaitPollResult{Time: now, EventsSeen: len(rows)}
	firstPoll := prevPoll.IsZero()
	interval := now.Sub(prevPoll)
	res.Interval = interval

	// Per-class accumulators for this poll.
	type bucket struct {
		count    uint64
		timeWait uint64
	}
	buckets := make(map[series.WaitClass]*bucket)
	for _, cls := range series.AllWaitClasses {
		if cls == series.WaitClassCPU {
			continue // CPU comes from CPUSampler, not from waits
		}
		buckets[cls] = &bucket{}
	}

	for _, r := range rows {
		newBL := waitBaseline{count: r.CountStar, timeWait: r.SumTimerWait}
		prev, hadPrev := c.baselines[r.EventName]

		switch {
		case firstPoll || !hadPrev:
			c.baselines[r.EventName] = newBL
			res.NewBaselines++
			continue
		case newBL.count < prev.count || newBL.timeWait < prev.timeWait:
			c.baselines[r.EventName] = newBL
			res.ResetSkips++
			continue
		}

		cls := ClassifyWaitEvent(r.EventName)
		if cls == series.WaitClassCPU {
			cls = series.WaitClassOther // defensive — classifier never returns CPU
		}
		b := buckets[cls]
		b.count += newBL.count - prev.count
		b.timeWait += newBL.timeWait - prev.timeWait
		c.baselines[r.EventName] = newBL
	}

	if firstPoll {
		c.lastPoll = now
		return res, nil
	}

	for _, cls := range series.AllWaitClasses {
		if cls == series.WaitClassCPU {
			continue
		}
		b := buckets[cls]
		if b.count == 0 && b.timeWait == 0 {
			continue
		}
		c.series.Append(series.WaitSample{
			Time:              now,
			Interval:          interval,
			Class:             cls,
			CountDelta:        b.count,
			SumTimerWaitDelta: b.timeWait,
		})
		res.Emitted++
	}

	c.lastPoll = now
	return res, nil
}
