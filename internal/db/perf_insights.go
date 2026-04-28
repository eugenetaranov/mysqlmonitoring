package db

import (
	"context"
	"fmt"
)

// DigestRow is one row from events_statements_summary_by_digest.
// All counters are absolute totals since server start; the collector
// is responsible for diffing across polls.
type DigestRow struct {
	Schema                  string
	Digest                  string
	DigestText              string
	CountStar               uint64
	SumTimerWait            uint64
	SumLockTime             uint64
	SumRowsExamined         uint64
	SumRowsSent             uint64
	SumNoIndexUsed          uint64
	SumCreatedTmpDiskTables uint64
	SumSortMergePasses      uint64
}

// WaitRow is one row from events_waits_summary_global_by_event_name.
type WaitRow struct {
	EventName    string
	CountStar    uint64
	SumTimerWait uint64
}

// CurrentStmt is the live state of one foreground session.
type CurrentStmt struct {
	ProcesslistID uint64
	Schema        string
	Digest        string
	SQLText       string
	Executing     bool   // events_statements_current is in-progress
	CurrentWait   string // empty / "idle" means CPU-eligible
	ProgramName   string // session_connect_attrs program_name
}

// Example is a recent statement instance recovered from
// events_statements_history_long.
type Example struct {
	SQLText string
	Schema  string
}

// PerfCapabilities reports what we can observe at startup. Each Available
// flag controls whether the matching collector is allowed to run.
// Warnings carries human-readable hints to print to stderr exactly once.
type PerfCapabilities struct {
	DigestAvailable      bool
	WaitsAvailable       bool
	HistoryLongAvailable bool
	ConnectAttrsAvailable bool
	Warnings              []string
}

// PerfInsightsDB is the narrow interface the perf-insights collectors
// depend on, separate from the broader db.DB interface so tests can
// fake just these methods.
type PerfInsightsDB interface {
	DigestStats(ctx context.Context) ([]DigestRow, error)
	WaitStats(ctx context.Context) ([]WaitRow, error)
	CurrentStatements(ctx context.Context) ([]CurrentStmt, error)
	RecentExample(ctx context.Context, digest string) (Example, error)
	ProbeCapabilities(ctx context.Context) (PerfCapabilities, error)
}

// DigestStats returns a snapshot of every digest's totals. Rows with a
// NULL digest (truncated entries) are skipped.
func (m *MySQLDB) DigestStats(ctx context.Context) ([]DigestRow, error) {
	const q = `
		SELECT
			COALESCE(SCHEMA_NAME, '') AS schema_name,
			DIGEST,
			COALESCE(DIGEST_TEXT, '') AS digest_text,
			COUNT_STAR,
			SUM_TIMER_WAIT,
			SUM_LOCK_TIME,
			SUM_ROWS_EXAMINED,
			SUM_ROWS_SENT,
			SUM_NO_INDEX_USED,
			SUM_CREATED_TMP_DISK_TABLES,
			SUM_SORT_MERGE_PASSES
		FROM performance_schema.events_statements_summary_by_digest
		WHERE DIGEST IS NOT NULL`

	rows, err := m.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("digest stats: %w", err)
	}
	defer rows.Close()

	var out []DigestRow
	for rows.Next() {
		var r DigestRow
		if err := rows.Scan(
			&r.Schema, &r.Digest, &r.DigestText,
			&r.CountStar, &r.SumTimerWait, &r.SumLockTime,
			&r.SumRowsExamined, &r.SumRowsSent,
			&r.SumNoIndexUsed, &r.SumCreatedTmpDiskTables,
			&r.SumSortMergePasses,
		); err != nil {
			return nil, fmt.Errorf("scan digest row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WaitStats returns a snapshot of every wait event's totals. Rows
// with zero counts are skipped to keep the working set small.
func (m *MySQLDB) WaitStats(ctx context.Context) ([]WaitRow, error) {
	const q = `
		SELECT EVENT_NAME, COUNT_STAR, SUM_TIMER_WAIT
		FROM performance_schema.events_waits_summary_global_by_event_name
		WHERE COUNT_STAR > 0`

	rows, err := m.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("wait stats: %w", err)
	}
	defer rows.Close()

	var out []WaitRow
	for rows.Next() {
		var r WaitRow
		if err := rows.Scan(&r.EventName, &r.CountStar, &r.SumTimerWait); err != nil {
			return nil, fmt.Errorf("scan wait row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CurrentStatements returns one row per foreground session with the
// information needed to: (a) decide CPU-eligibility for AAS sampling,
// (b) resolve an app tag from program_name and any leading SQL comment.
//
// The current-wait correlated subquery picks the deepest non-idle
// wait event for the thread so a session blocked on a real wait is
// not mis-classified as CPU.
func (m *MySQLDB) CurrentStatements(ctx context.Context) ([]CurrentStmt, error) {
	const q = `
		SELECT
			t.PROCESSLIST_ID,
			COALESCE(esc.CURRENT_SCHEMA, '') AS schema_name,
			COALESCE(esc.DIGEST, '')         AS digest,
			COALESCE(esc.SQL_TEXT, '')       AS sql_text,
			CASE WHEN esc.EVENT_ID IS NOT NULL AND esc.END_EVENT_ID IS NULL
			     THEN 1 ELSE 0 END           AS executing,
			COALESCE((
				SELECT ewc.EVENT_NAME
				FROM performance_schema.events_waits_current ewc
				WHERE ewc.THREAD_ID = t.THREAD_ID
				  AND ewc.EVENT_NAME <> 'idle'
				ORDER BY ewc.EVENT_ID DESC
				LIMIT 1
			), '')                           AS current_wait,
			COALESCE((
				SELECT ATTR_VALUE
				FROM performance_schema.session_connect_attrs
				WHERE PROCESSLIST_ID = t.PROCESSLIST_ID
				  AND ATTR_NAME = 'program_name'
				LIMIT 1
			), '')                           AS program_name
		FROM performance_schema.threads t
		LEFT JOIN performance_schema.events_statements_current esc
		       ON esc.THREAD_ID = t.THREAD_ID
		WHERE t.PROCESSLIST_ID IS NOT NULL
		  AND t.TYPE = 'FOREGROUND'`

	rows, err := m.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("current statements: %w", err)
	}
	defer rows.Close()

	var out []CurrentStmt
	for rows.Next() {
		var c CurrentStmt
		var executing int
		if err := rows.Scan(
			&c.ProcesslistID, &c.Schema, &c.Digest, &c.SQLText,
			&executing, &c.CurrentWait, &c.ProgramName,
		); err != nil {
			return nil, fmt.Errorf("scan current statement: %w", err)
		}
		c.Executing = executing == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecentExample pulls the most recent fully-rendered SQL text matching
// digest. Empty strings are filtered out at the SQL layer; if no row
// matches, the returned Example has empty fields and err is nil so
// callers can decide how to surface "no example".
func (m *MySQLDB) RecentExample(ctx context.Context, digest string) (Example, error) {
	const q = `
		SELECT SQL_TEXT, COALESCE(CURRENT_SCHEMA, '')
		FROM performance_schema.events_statements_history_long
		WHERE DIGEST = ? AND SQL_TEXT IS NOT NULL AND SQL_TEXT <> ''
		ORDER BY TIMER_END DESC
		LIMIT 1`

	var ex Example
	row := m.db.QueryRowContext(ctx, q, digest)
	switch err := row.Scan(&ex.SQLText, &ex.Schema); err {
	case nil:
		return ex, nil
	default:
		// sql.ErrNoRows arrives here when nothing matched; treat as soft miss.
		if err.Error() == "sql: no rows in result set" {
			return Example{}, nil
		}
		return Example{}, fmt.Errorf("recent example: %w", err)
	}
}

// ProbeCapabilities checks performance_schema setup once at startup
// and returns which collectors are safe to run plus a list of
// human-readable warnings the caller should print exactly once.
func (m *MySQLDB) ProbeCapabilities(ctx context.Context) (PerfCapabilities, error) {
	caps := PerfCapabilities{}

	// Required consumers for digest sampling. We require BOTH the
	// global statement instrumentation and the digest consumer.
	consumers, err := m.queryConsumers(ctx)
	if err != nil {
		// performance_schema may be off entirely. Mark everything
		// unavailable but don't fail — the lock-monitor side runs fine.
		caps.Warnings = append(caps.Warnings,
			"performance_schema setup_consumers query failed: "+err.Error()+
				"; perf-insights collectors disabled")
		return caps, nil
	}

	caps.DigestAvailable = consumers["statements_digest"] && consumers["global_instrumentation"]
	if !caps.DigestAvailable {
		caps.Warnings = append(caps.Warnings, classifyDigestWarning(consumers))
	}

	caps.WaitsAvailable = consumers["events_waits_current"] || consumers["global_instrumentation"]
	// events_waits_summary_global_by_event_name is always populated when
	// global_instrumentation is on; events_waits_current is only required
	// for per-session sampling. We treat any of the two as "good enough".
	if !caps.WaitsAvailable {
		caps.Warnings = append(caps.Warnings,
			"performance_schema wait events not available; enable consumer "+
				"'global_instrumentation' to populate events_waits_*")
	}

	caps.HistoryLongAvailable = consumers["events_statements_history_long"]
	if !caps.HistoryLongAvailable {
		caps.Warnings = append(caps.Warnings,
			"events_statements_history_long is disabled; EXPLAIN-on-demand "+
				"will report 'no recent example' until you run "+
				"UPDATE performance_schema.setup_consumers SET ENABLED='YES' "+
				"WHERE NAME='events_statements_history_long';")
	}

	// session_connect_attrs is a table, not a consumer; check for its
	// presence by selecting from it. If the table is missing or the user
	// lacks privileges, app-tag resolution falls back to the SQL-comment
	// path only.
	caps.ConnectAttrsAvailable = m.tableExists(ctx, "performance_schema", "session_connect_attrs")
	if !caps.ConnectAttrsAvailable {
		caps.Warnings = append(caps.Warnings,
			"performance_schema.session_connect_attrs unavailable; "+
				"app tags will only resolve from sqlcommenter SQL comments")
	}

	return caps, nil
}

// queryConsumers reads setup_consumers into a map keyed by NAME with
// boolean values for ENABLED. Unknown error → returned to caller.
func (m *MySQLDB) queryConsumers(ctx context.Context) (map[string]bool, error) {
	const q = `SELECT NAME, ENABLED FROM performance_schema.setup_consumers`
	rows, err := m.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var name, enabled string
		if err := rows.Scan(&name, &enabled); err != nil {
			return nil, err
		}
		out[name] = enabled == "YES"
	}
	return out, rows.Err()
}

func (m *MySQLDB) tableExists(ctx context.Context, schema, name string) bool {
	const q = `
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
	var n int
	if err := m.db.QueryRowContext(ctx, q, schema, name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// classifyDigestWarning produces a precise hint based on which
// consumer is the missing piece, so operators copy-paste the correct
// fix instead of toggling everything.
func classifyDigestWarning(consumers map[string]bool) string {
	switch {
	case !consumers["global_instrumentation"]:
		return "performance_schema global_instrumentation consumer is OFF; " +
			"enable it with UPDATE performance_schema.setup_consumers " +
			"SET ENABLED='YES' WHERE NAME='global_instrumentation';"
	case !consumers["statements_digest"]:
		return "performance_schema statements_digest consumer is OFF; " +
			"enable it with UPDATE performance_schema.setup_consumers " +
			"SET ENABLED='YES' WHERE NAME='statements_digest';"
	default:
		return "performance_schema digest consumers appear off; " +
			"verify setup_consumers and statement instruments are enabled"
	}
}
