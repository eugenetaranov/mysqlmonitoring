package insights

import (
	"sort"
	"strings"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/collector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// SortKey selects how DigestSummaries are ordered.
type SortKey int

const (
	SortByAAS SortKey = iota
	SortByCalls
	SortByAvgLatency
	SortByRowsExamined
)

// ParseSortKey parses a CLI value into a SortKey. Unknown values
// fall back to SortByAAS so the user gets a sensible result.
func ParseSortKey(s string) SortKey {
	switch strings.ToLower(s) {
	case "calls":
		return SortByCalls
	case "p95", "avg", "latency":
		return SortByAvgLatency
	case "rows-examined", "rows":
		return SortByRowsExamined
	default:
		return SortByAAS
	}
}

// DigestSummary is the per-digest aggregate emitted by TopSQL.
// Latencies and rates are computed from delta samples within the
// requested window. Percentile latencies are intentionally absent in
// M1; use AvgLatency until histogram support lands.
type DigestSummary struct {
	Schema           string
	Digest           string
	Text             string
	Calls            uint64
	AAS              float64
	CallsPerSec      float64
	AvgLatency       time.Duration
	TotalLatency     time.Duration
	RowsExamined     uint64
	RowsExamPerCall  float64
	NoIndexUsedCalls uint64
}

// TopSQLOptions controls how TopSQL ranks and filters results.
type TopSQLOptions struct {
	Window time.Duration
	Sort   SortKey
	Limit  int
	App    string
	Schema string
}

// TopSQL aggregates per-digest samples within window and returns up
// to options.Limit summaries sorted by options.Sort. The app filter,
// when set, scopes results to digests that have at least one
// SessionSample tagged with app within the same window.
func TopSQL(reg *series.Registry, sessions *series.RingSink[series.SessionSample], now time.Time, opts TopSQLOptions) []DigestSummary {
	if opts.Window <= 0 {
		opts.Window = time.Hour
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	allowed := digestsForApp(sessions, now, opts.Window, opts.App)

	out := make([]DigestSummary, 0, reg.Len())
	for _, e := range reg.Snapshot() {
		if opts.Schema != "" && e.Key.Schema != opts.Schema {
			continue
		}
		if opts.App != "" {
			if _, ok := allowed[e.Key.Digest]; !ok {
				continue
			}
		}
		s := summarise(e, now, opts.Window)
		if s.Calls == 0 {
			continue
		}
		out = append(out, s)
	}

	sortSummaries(out, opts.Sort)

	if len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out
}

// digestsForApp returns the set of digest keys that appear in
// session samples tagged app within the window. When app is empty,
// the returned map is nil and callers should treat that as "no
// filter".
func digestsForApp(sessions *series.RingSink[series.SessionSample], now time.Time, window time.Duration, app string) map[string]struct{} {
	if app == "" || sessions == nil {
		return nil
	}
	out := make(map[string]struct{})
	for s := range sessions.Range(now, window) {
		if s.AppTag == app && s.Digest != "" {
			out[s.Digest] = struct{}{}
		}
	}
	return out
}

// summarise folds every retained DigestSample for entry into a
// DigestSummary covering the requested window.
func summarise(e *series.DigestEntry, now time.Time, window time.Duration) DigestSummary {
	var (
		callsTotal      uint64
		timeTotal       uint64 // picoseconds
		rowsExam        uint64
		noIdx           uint64
		intervalSeconds float64
	)
	for s := range e.Sink.Range(now, window) {
		callsTotal += s.ExecCountDelta
		timeTotal += s.SumTimerWaitDelta
		rowsExam += s.SumRowsExaminedDelta
		noIdx += s.SumNoIndexUsedDelta
		intervalSeconds += s.Interval.Seconds()
	}

	sum := DigestSummary{
		Schema:           e.Key.Schema,
		Digest:           e.Key.Digest,
		Text:             e.Text,
		Calls:            callsTotal,
		RowsExamined:     rowsExam,
		NoIndexUsedCalls: noIdx,
	}
	if callsTotal > 0 {
		sum.AvgLatency = picoseconds(timeTotal / callsTotal)
		sum.RowsExamPerCall = float64(rowsExam) / float64(callsTotal)
	}
	if intervalSeconds > 0 {
		sum.AAS = float64(timeTotal) / 1e12 / intervalSeconds
		sum.CallsPerSec = float64(callsTotal) / intervalSeconds
	}
	sum.TotalLatency = picoseconds(timeTotal)
	return sum
}

// picoseconds converts a picosecond count into a time.Duration.
// Wait fields in performance_schema are reported in picoseconds,
// while time.Duration's unit is nanoseconds.
func picoseconds(p uint64) time.Duration {
	return time.Duration(p / 1000)
}

func sortSummaries(s []DigestSummary, key SortKey) {
	switch key {
	case SortByCalls:
		sort.Slice(s, func(i, j int) bool { return s[i].Calls > s[j].Calls })
	case SortByAvgLatency:
		sort.Slice(s, func(i, j int) bool { return s[i].AvgLatency > s[j].AvgLatency })
	case SortByRowsExamined:
		sort.Slice(s, func(i, j int) bool { return s[i].RowsExamined > s[j].RowsExamined })
	default:
		sort.Slice(s, func(i, j int) bool { return s[i].AAS > s[j].AAS })
	}
}

// GroupKey selects the dimension by which LoadByGroup attributes
// load. Each session sample contributes to exactly one group.
type GroupKey int

const (
	GroupKeyUser GroupKey = iota
	GroupKeyHost
	GroupKeySchema
)

// GroupLoad is one row in a load-by-group breakdown.
//
// AAS is computed as observation-count / tick-count, where one
// observation is one Executing session seen at one sampler tick.
// This is symmetric with how the global CPU class is computed in
// collector.CPUSampler — i.e. AAS expressed as "average concurrent
// active sessions for this group, over the window".
type GroupLoad struct {
	Group string
	AAS   float64
	// Observations is the raw session-tick count (sum across all
	// ticks in the window of executing sessions belonging to Group).
	// Useful for ranking even when the window has zero ticks.
	Observations uint64
}

// LoadByGroup attributes the executing-session load over the window
// to groups identified by key. Output is sorted by AAS descending.
//
// The function operates purely in-memory over the existing
// SessionSample sink; it never touches the database. Sessions whose
// group value is empty (e.g. system threads with NULL processlist
// user) are bucketed under "(unknown)" so the totals reconcile.
func LoadByGroup(sessions *series.RingSink[series.SessionSample], now time.Time, window time.Duration, key GroupKey) []GroupLoad {
	if window <= 0 {
		window = time.Hour
	}
	if sessions == nil {
		return nil
	}

	obs := make(map[string]uint64)
	ticks := make(map[time.Time]struct{})
	for s := range sessions.Range(now, window) {
		ticks[s.Time] = struct{}{}
		if !s.Executing {
			continue
		}
		obs[groupValue(s, key)]++
	}

	tickCount := uint64(len(ticks))
	out := make([]GroupLoad, 0, len(obs))
	for g, n := range obs {
		gl := GroupLoad{Group: g, Observations: n}
		if tickCount > 0 {
			gl.AAS = float64(n) / float64(tickCount)
		}
		out = append(out, gl)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].AAS != out[j].AAS {
			return out[i].AAS > out[j].AAS
		}
		// Stable tiebreak on Group name so test output is deterministic.
		return out[i].Group < out[j].Group
	})
	return out
}

func groupValue(s series.SessionSample, key GroupKey) string {
	var v string
	switch key {
	case GroupKeyHost:
		v = s.Host
	case GroupKeySchema:
		v = s.Schema
	default:
		v = s.User
	}
	if v == "" {
		return "(unknown)"
	}
	return v
}

// ClassLoad is one wait-class entry in the LoadBreakdown.
type ClassLoad struct {
	Class series.WaitClass
	AAS   float64
}

// LoadBreakdown is the per-class DB-load summary for a window.
type LoadBreakdown struct {
	Window  time.Duration
	Classes []ClassLoad
	Total   float64
}

// Load summarises wait-class load over the supplied window. CPU AAS
// is computed from observation count and tick count; non-CPU classes
// from sum_timer_wait_delta divided by elapsed time.
func Load(waits *collector.WaitSeries, now time.Time, window time.Duration) LoadBreakdown {
	if window <= 0 {
		window = time.Hour
	}
	out := LoadBreakdown{Window: window}

	for _, cls := range series.AllWaitClasses {
		sink := waits.Sink(cls)
		if sink == nil {
			continue
		}

		var (
			timePicos       uint64
			intervalSeconds float64
			cpuObs          uint64
			cpuTicks        uint64
		)
		for s := range sink.Range(now, window) {
			timePicos += s.SumTimerWaitDelta
			intervalSeconds += s.Interval.Seconds()
			cpuObs += s.CPUObservations
			cpuTicks += s.CPUTicks
		}

		var aas float64
		switch cls {
		case series.WaitClassCPU:
			if cpuTicks > 0 {
				aas = float64(cpuObs) / float64(cpuTicks)
			}
		default:
			if intervalSeconds > 0 {
				aas = float64(timePicos) / 1e12 / intervalSeconds
			}
		}
		out.Classes = append(out.Classes, ClassLoad{Class: cls, AAS: aas})
		out.Total += aas
	}
	return out
}
