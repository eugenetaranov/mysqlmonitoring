package series

import "time"

// Sample is the minimum contract a series item must satisfy: it must
// expose the wall-clock time at which it was produced so the sink can
// answer window queries.
type Sample interface {
	SampleTime() time.Time
}

// DigestKey identifies a query digest in the registry. Two digests with
// the same hash but different schemas are tracked independently because
// the same SQL on different schemas is conceptually a different query.
type DigestKey struct {
	Schema string
	Digest string
}

// DigestSample carries one polling interval's per-digest deltas.
// All Sum* fields are differences against the prior poll, in the units
// returned by performance_schema (picoseconds for timer fields, raw
// counts otherwise). Interval is the wall-clock duration of the diff.
type DigestSample struct {
	Time     time.Time
	Interval time.Duration

	ExecCountDelta             uint64
	SumTimerWaitDelta          uint64 // picoseconds
	SumLockTimeDelta           uint64 // picoseconds
	SumRowsExaminedDelta       uint64
	SumRowsSentDelta           uint64
	SumNoIndexUsedDelta        uint64
	SumCreatedTmpDiskTablesDelta uint64
	SumSortMergePassesDelta    uint64
}

func (s DigestSample) SampleTime() time.Time { return s.Time }

// WaitClass is the bucket each performance_schema wait event maps to.
// CPU is synthetic — it is produced by sampling executing sessions
// with no current wait, not by reading events_waits_*.
type WaitClass uint8

const (
	WaitClassOther WaitClass = iota
	WaitClassCPU
	WaitClassIO
	WaitClassLock
	WaitClassSync
	WaitClassNetwork
)

// AllWaitClasses lists every class in stable display order so the TUI
// stacked sparkline always renders bands the same way.
var AllWaitClasses = [...]WaitClass{
	WaitClassCPU, WaitClassIO, WaitClassLock,
	WaitClassSync, WaitClassNetwork, WaitClassOther,
}

func (c WaitClass) String() string {
	switch c {
	case WaitClassCPU:
		return "CPU"
	case WaitClassIO:
		return "IO"
	case WaitClassLock:
		return "Lock"
	case WaitClassSync:
		return "Sync"
	case WaitClassNetwork:
		return "Network"
	default:
		return "Other"
	}
}

// WaitSample is one polling interval's per-class wait totals. CPU
// samples carry CPUObservations / CPUTicks instead of timer deltas
// because CPU AAS is sampled, not timed.
type WaitSample struct {
	Time     time.Time
	Interval time.Duration
	Class    WaitClass

	CountDelta        uint64
	SumTimerWaitDelta uint64 // picoseconds; zero for CPU class

	CPUObservations uint64 // executing sessions seen across this interval
	CPUTicks        uint64 // sample ticks contributing to the count above
}

func (s WaitSample) SampleTime() time.Time { return s.Time }

// SessionSample is a snapshot of one executing session at sampling
// time, used both for CPU AAS and for app-tagging digests/waits.
type SessionSample struct {
	Time          time.Time
	ProcesslistID uint64
	Digest        string
	Schema        string
	AppTag        string
	EventName     string // empty if no current wait (CPU candidate)
	Executing     bool   // session is actively running a statement
}

func (s SessionSample) SampleTime() time.Time { return s.Time }
