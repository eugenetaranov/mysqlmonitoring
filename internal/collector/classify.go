package collector

import (
	"strings"

	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// ClassifyWaitEvent maps a performance_schema event name to its wait
// class. The mapping is deterministic and based on prefix only:
// performance_schema event names already form a hierarchical taxonomy
// rooted at "wait/", so a prefix test is unambiguous.
//
// CPU is intentionally not produced here: it is sampled, not timed.
// Anything that is not a wait/* event lands in Other.
func ClassifyWaitEvent(name string) series.WaitClass {
	switch {
	case strings.HasPrefix(name, "wait/io/socket/"):
		return series.WaitClassNetwork
	case strings.HasPrefix(name, "wait/io/file/"),
		strings.HasPrefix(name, "wait/io/table/"):
		return series.WaitClassIO
	case strings.HasPrefix(name, "wait/lock/"):
		return series.WaitClassLock
	case strings.HasPrefix(name, "wait/synch/mutex/"),
		strings.HasPrefix(name, "wait/synch/rwlock/"),
		strings.HasPrefix(name, "wait/synch/cond/"),
		strings.HasPrefix(name, "wait/synch/sxlock/"):
		return series.WaitClassSync
	default:
		return series.WaitClassOther
	}
}
