package collector

import (
	"context"
	"sync"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// CPUSampler estimates the CPU class of the DB-load chart by sampling
// performance_schema.events_statements_current at a fixed cadence.
// At each Sample call it counts foreground sessions that are
// executing a statement and have no current non-idle wait. CPU AAS
// for a window is later computed as observations / ticks.
//
// As a side effect the sampler also emits SessionSamples with the
// resolved AppTag into sessionSink so per-app aggregations can slice
// load by application without a second query path.
type CPUSampler struct {
	source      db.PerfInsightsDB
	series      *WaitSeries
	sessionSink *series.RingSink[series.SessionSample]

	mu            sync.Mutex
	pendingObs    uint64
	pendingTicks  uint64
	intervalStart time.Time
}

// NewCPUSampler constructs a sampler. intervalStart is the wall-clock
// time the first interval is considered to begin at; pass time.Now()
// at startup. sessionSink may be nil to disable per-session emission.
func NewCPUSampler(source db.PerfInsightsDB, waits *WaitSeries, sessionSink *series.RingSink[series.SessionSample], intervalStart time.Time) *CPUSampler {
	return &CPUSampler{
		source:        source,
		series:        waits,
		sessionSink:   sessionSink,
		intervalStart: intervalStart,
	}
}

// Sample takes one observation tick. It is safe to call concurrently
// with Flush.
func (s *CPUSampler) Sample(ctx context.Context, sampleTime time.Time) error {
	stmts, err := s.source.CurrentStatements(ctx)
	if err != nil {
		return err
	}
	var obs uint64
	for _, st := range stmts {
		if !st.Executing {
			continue
		}
		onCPU := isCPUWait(st.CurrentWait)
		if onCPU {
			obs++
		}
		if s.sessionSink != nil {
			s.sessionSink.Append(series.SessionSample{
				Time:          sampleTime,
				ProcesslistID: st.ProcesslistID,
				Digest:        st.Digest,
				Schema:        st.Schema,
				AppTag:        ResolveAppTag(st),
				EventName:     st.CurrentWait,
				Executing:     true,
			})
		}
	}
	s.mu.Lock()
	s.pendingObs += obs
	s.pendingTicks++
	s.mu.Unlock()
	return nil
}

// Flush closes the current interval ending at now and emits one CPU
// WaitSample with the accumulated observation and tick counts. If no
// ticks were taken, Flush is a no-op so an idle sampler does not
// pollute the series with phantom zero samples.
func (s *CPUSampler) Flush(now time.Time) {
	s.mu.Lock()
	obs, ticks := s.pendingObs, s.pendingTicks
	start := s.intervalStart
	s.pendingObs, s.pendingTicks = 0, 0
	s.intervalStart = now
	s.mu.Unlock()

	if ticks == 0 {
		return
	}
	s.series.Append(series.WaitSample{
		Time:            now,
		Interval:        now.Sub(start),
		Class:           series.WaitClassCPU,
		CPUObservations: obs,
		CPUTicks:        ticks,
	})
}

// isCPUWait reports whether the wait-event name (which may be empty
// when no wait is recorded) implies the session is on CPU. The empty
// string means "no current wait at all"; the literal "idle" appears
// for sessions sitting between statements but is sometimes also the
// only entry returned for an active session, so we treat both as CPU.
func isCPUWait(eventName string) bool {
	return eventName == "" || eventName == "idle"
}
