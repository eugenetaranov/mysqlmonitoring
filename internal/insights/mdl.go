package insights

import (
	"sort"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// MDLEntry is one row from performance_schema.metadata_locks joined
// with performance_schema.threads. We mirror db.MetadataLock here so
// callers don't need to import the db package — and so the field
// names line up with how the TUI renders them ("Query" vs "Info").
type MDLEntry struct {
	PID          uint64
	ThreadID     uint64
	User         string
	Host         string
	LockType     string // SHARED_READ, SHARED_WRITE, EXCLUSIVE, …
	LockDuration string // STATEMENT / TRANSACTION / EXPLICIT
	LockStatus   string // GRANTED / PENDING (others rare)
	WaitSeconds  int64
	Query        string
}

// MDLQueue is one table's slice of the MDL state: every granted lock
// holder plus every pending waiter, with helpers to read queue
// position and derive blockers.
//
// Pending is sorted oldest-waiter-first — MySQL grants pending MDL
// requests in FIFO order, so the slice index doubles as the queue
// position (1-indexed via PositionOf).
type MDLQueue struct {
	Schema, Name string
	Granted      []MDLEntry
	Pending      []MDLEntry
	// ByLockType counts pending entries per LOCK_TYPE. Convenient
	// for the list-mode "TYPES" column without the consumer
	// re-walking Pending.
	ByLockType map[string]int
}

// MDLBreakdown is the entire snapshot's MDL view, one MDLQueue per
// (OBJECT_SCHEMA, OBJECT_NAME) pair with at least one row. Tables are
// sorted by len(Pending) descending; ties broken by the oldest pending
// entry's WaitSeconds.
type MDLBreakdown struct {
	Tables []MDLQueue
}

// BuildMDL groups snap.MetadataLocks by table and returns the
// breakdown. Pure in-memory — no DB calls.
func BuildMDL(snap db.Snapshot) MDLBreakdown {
	grouped := make(map[string]*MDLQueue)
	for _, ml := range snap.MetadataLocks {
		if ml.ObjectType != "TABLE" || ml.ObjectName == "" {
			continue
		}
		key := ml.ObjectSchema + "." + ml.ObjectName
		q, ok := grouped[key]
		if !ok {
			q = &MDLQueue{
				Schema:     ml.ObjectSchema,
				Name:       ml.ObjectName,
				ByLockType: make(map[string]int),
			}
			grouped[key] = q
		}
		entry := MDLEntry{
			PID:          ml.PID,
			ThreadID:     ml.ThreadID,
			User:         ml.User,
			Host:         ml.Host,
			LockType:     ml.LockType,
			LockDuration: ml.Duration,
			LockStatus:   ml.LockStatus,
			WaitSeconds:  ml.TimeSeconds,
			Query:        ml.Info,
		}
		switch ml.LockStatus {
		case "GRANTED":
			q.Granted = append(q.Granted, entry)
		case "PENDING":
			q.Pending = append(q.Pending, entry)
			q.ByLockType[ml.LockType]++
		default:
			// VICTIM, TIMEOUT, KILLED, PRE_ACQUIRE_NOTIFY, etc. —
			// rare transient states. We don't bucket them; they
			// neither hold nor wait at the moment of capture.
		}
	}

	out := MDLBreakdown{Tables: make([]MDLQueue, 0, len(grouped))}
	for _, q := range grouped {
		// Pending sorted oldest-first (longest wait at the head).
		sort.SliceStable(q.Pending, func(i, j int) bool {
			return q.Pending[i].WaitSeconds > q.Pending[j].WaitSeconds
		})
		// Granted sorted by holder age too — so the operator sees
		// the oldest holder (often the long-running transaction
		// holding everything up) first.
		sort.SliceStable(q.Granted, func(i, j int) bool {
			return q.Granted[i].WaitSeconds > q.Granted[j].WaitSeconds
		})
		out.Tables = append(out.Tables, *q)
	}
	sort.SliceStable(out.Tables, func(i, j int) bool {
		if len(out.Tables[i].Pending) != len(out.Tables[j].Pending) {
			return len(out.Tables[i].Pending) > len(out.Tables[j].Pending)
		}
		// Tiebreak on oldest pending wait age.
		var a, b int64
		if len(out.Tables[i].Pending) > 0 {
			a = out.Tables[i].Pending[0].WaitSeconds
		}
		if len(out.Tables[j].Pending) > 0 {
			b = out.Tables[j].Pending[0].WaitSeconds
		}
		return a > b
	})
	return out
}

// Find returns the MDLQueue for a table, or nil when the table is not
// in the breakdown.
func (b MDLBreakdown) Find(schema, name string) *MDLQueue {
	for i := range b.Tables {
		if b.Tables[i].Schema == schema && b.Tables[i].Name == name {
			return &b.Tables[i]
		}
	}
	return nil
}

// PositionOf returns the 1-indexed rank of the pending entry whose
// PID matches pid, plus the total queue depth. ok is false when the
// PID is not pending on this table.
func (q MDLQueue) PositionOf(pid uint64) (rank, total int, ok bool) {
	total = len(q.Pending)
	for i, e := range q.Pending {
		if e.PID == pid {
			return i + 1, total, true
		}
	}
	return 0, total, false
}

// BlockersOf returns every Granted entry whose LockType is
// incompatible with the requested LockType of the pending entry
// identified by pid. The compatibility check uses a static map; for
// authoritative answers the Phase-4 sys.schema_table_lock_waits path
// (deferred) takes precedence when available.
//
// If pid is not pending on this table, the result is nil.
func (q MDLQueue) BlockersOf(pid uint64) []MDLEntry {
	var requested string
	for _, e := range q.Pending {
		if e.PID == pid {
			requested = e.LockType
			break
		}
	}
	if requested == "" {
		return nil
	}
	var out []MDLEntry
	for _, h := range q.Granted {
		if conflicts(requested, h.LockType) {
			out = append(out, h)
		}
	}
	return out
}

// mdlCompat is the lock-type compatibility map. The cell at
// [waiter][holder] is true when the waiter's request is BLOCKED by
// the holder's grant. Mirrors the documented MDL conflict matrix:
// EXCLUSIVE conflicts with everything, SHARED_NO_READ_WRITE with
// almost everything, SHARED_READ only with EXCLUSIVE-class types,
// etc.
//
// The matrix is conservative: when in doubt, mark the holder as a
// blocker. False positives in the HOLDERS panel are recoverable
// (operator inspects); false negatives ("nothing is blocking you")
// are misleading.
var mdlCompat = map[string]map[string]bool{
	// SHARED_READ: blocked only by exclusive-class holders.
	"SHARED_READ": {
		"EXCLUSIVE":               true,
		"SHARED_NO_READ_WRITE":    true,
		"SHARED_NO_WRITE":         false, // SNW allows SR
	},
	"SHARED_HIGH_PRIO": {
		"EXCLUSIVE":            true,
		"SHARED_NO_READ_WRITE": true,
	},
	"SHARED_WRITE": {
		"EXCLUSIVE":            true,
		"SHARED_NO_READ_WRITE": true,
		"SHARED_NO_WRITE":      true,
		"SHARED_UPGRADABLE":    false,
	},
	"SHARED_WRITE_LOW_PRIO": {
		"EXCLUSIVE":            true,
		"SHARED_NO_READ_WRITE": true,
		"SHARED_NO_WRITE":      true,
	},
	"SHARED_UPGRADABLE": {
		"SHARED_UPGRADABLE":    true,
		"SHARED_NO_READ_WRITE": true,
		"SHARED_NO_WRITE":      true,
		"EXCLUSIVE":            true,
	},
	"SHARED_NO_WRITE": {
		"SHARED_WRITE":         true,
		"SHARED_NO_WRITE":      true,
		"SHARED_NO_READ_WRITE": true,
		"SHARED_UPGRADABLE":    true,
		"EXCLUSIVE":            true,
	},
	"SHARED_NO_READ_WRITE": {
		"SHARED_READ":           true,
		"SHARED_HIGH_PRIO":      true,
		"SHARED_WRITE":          true,
		"SHARED_WRITE_LOW_PRIO": true,
		"SHARED_UPGRADABLE":     true,
		"SHARED_NO_WRITE":       true,
		"SHARED_NO_READ_WRITE":  true,
		"EXCLUSIVE":             true,
	},
	"INTENTION_EXCLUSIVE": {
		// IX is a schema-level intent that conflicts only with
		// schema-level shared locks — none of which appear at the
		// TABLE OBJECT_TYPE we filter for, so this row is normally
		// empty in practice.
		"EXCLUSIVE": true,
	},
	"EXCLUSIVE": {
		// EXCLUSIVE conflicts with every other lock type.
		"SHARED_READ":           true,
		"SHARED_HIGH_PRIO":      true,
		"SHARED_WRITE":          true,
		"SHARED_WRITE_LOW_PRIO": true,
		"SHARED_UPGRADABLE":     true,
		"SHARED_NO_WRITE":       true,
		"SHARED_NO_READ_WRITE":  true,
		"INTENTION_EXCLUSIVE":   true,
		"EXCLUSIVE":             true,
	},
}

// conflicts reports whether a holder's LockType blocks a waiter's
// LockType. Unknown waiter type → conservative true (we'd rather
// list the holder than miss a real blocker).
func conflicts(waiter, holder string) bool {
	row, ok := mdlCompat[waiter]
	if !ok {
		return true
	}
	return row[holder]
}
