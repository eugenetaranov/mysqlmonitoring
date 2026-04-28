package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDuration_RoundTrip(t *testing.T) {
	cases := map[string]time.Duration{
		"13s":    13 * time.Second,
		"44m53s": 44*time.Minute + 53*time.Second,
		"19h14m": 19*time.Hour + 14*time.Minute,
		"":       0,
		"bad":    0,
	}
	for in, want := range cases {
		assert.Equalf(t, want, parseDuration(in), "input=%q", in)
	}
}

func TestBuildIssueRows_LongTransaction(t *testing.T) {
	now := time.Now()
	snap := db.Snapshot{
		Time: now,
		Transactions: []db.Transaction{
			{ID: 12345, User: "app", Host: "10.0.0.1:5", TrxStarted: now.Add(-19 * time.Hour)},
		},
	}
	issues := []detector.Issue{{
		Detector: "long_transaction",
		Severity: detector.SeverityCritical,
		Title:    "Long-running transaction (19h0m)",
		Details: map[string]string{
			"thread_id": "12345",
			"user":      "app",
			"host":      "10.0.0.1:5",
			"duration":  "19h0m",
			"query":     "SELECT * FROM users WHERE id = ?",
		},
	}}

	got := buildIssueRows(issues, snap)
	require.Len(t, got, 1)
	assert.Equal(t, uint64(12345), got[0].pid)
	assert.Equal(t, "app", got[0].user)
	assert.Equal(t, "long-trx", got[0].kind)
	assert.Equal(t, "SELECT * FROM users WHERE id = ?", got[0].query)
	assert.Equal(t, 19*time.Hour, got[0].age)
	assert.True(t, got[0].killable())
}

func TestBuildIssueRows_SortByCriticalityThenAge(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityWarning, Details: map[string]string{"duration": "30s", "thread_id": "1"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical, Details: map[string]string{"duration": "10s", "thread_id": "2"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical, Details: map[string]string{"duration": "1h", "thread_id": "3"}},
	}
	got := buildIssueRows(issues, db.Snapshot{})
	require.Len(t, got, 3)
	assert.Equal(t, uint64(3), got[0].pid, "longest critical first")
	assert.Equal(t, uint64(2), got[1].pid, "shorter critical second")
	assert.Equal(t, uint64(1), got[2].pid, "warning last")
}

func TestComputeColWidths_HidesUserHostOnNarrow(t *testing.T) {
	narrow := computeColWidths(100, true)
	assert.Equal(t, 0, narrow.userHost)
	wide := computeColWidths(160, true)
	assert.Greater(t, wide.userHost, 0)
}

func TestRenderIssuesTable_ShowsExpectedColumnsAndCursor(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"duration": "19h14m", "thread_id": "999", "user": "app", "host": "10.0.0.1:5", "query": "SELECT 1"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"duration": "10h37m", "thread_id": "888", "user": "app", "host": "10.0.0.2:5", "query": "UPDATE x SET y=?"}},
	}
	m := Model{
		width:  140,
		height: 30,
		view:   ViewIssues,
		result: monitorResultFromIssues(issues),
	}
	out := renderIssuesTable(m)
	for _, want := range []string{"SEV", "AGE", "PID", "KIND", "QUERY", "19h14m", "999", "SELECT 1"} {
		assert.True(t, strings.Contains(out, want), "missing %q in output", want)
	}
}

func TestWrapText_BreaksOnSpaces(t *testing.T) {
	out := wrapText("the quick brown fox jumps over the lazy dog", 10)
	for _, line := range out {
		assert.LessOrEqual(t, len(line), 10)
	}
	assert.Equal(t, "the quick", out[0])
}

func TestRenderIssueDetail_IncludesQueryWrapped(t *testing.T) {
	issues := []detector.Issue{{
		Detector: "long_transaction", Severity: detector.SeverityWarning,
		Title: "Long-running transaction (1m)",
		Details: map[string]string{
			"duration": "1m", "thread_id": "1", "user": "u", "host": "h",
			"query": "SELECT this is a fairly long statement that should wrap across multiple lines when the panel renders it",
		},
	}}
	m := Model{width: 60, view: ViewIssueDetail, result: monitorResultFromIssues(issues)}
	out := renderIssueDetail(m)
	assert.Contains(t, out, "Issue detail")
	assert.Contains(t, out, "PID:")
	assert.Contains(t, out, "Query:")
	assert.Contains(t, out, "fairly long statement")
}

func monitorResultFromIssues(issues []detector.Issue) monitor.Result {
	return monitor.Result{Issues: issues}
}

func TestBuildIssueRows_DeadlockFansOutToParticipants(t *testing.T) {
	now := time.Now()
	snap := db.Snapshot{
		Time: now,
		Transactions: []db.Transaction{
			{ID: 1001, TrxStarted: now.Add(-30 * time.Second)},
		},
		Processes: []db.Process{
			{ID: 1002, User: "live-app", Host: "10.0.0.2:5", Info: "DELETE FROM t WHERE id=2"},
		},
	}
	issues := []detector.Issue{{
		Detector:    "deadlock",
		Severity:    detector.SeverityCritical,
		Title:       "Deadlock detected (2 transactions)",
		Description: "Deadlock at 2026-04-26 12:00:00",
		Details: map[string]string{
			"timestamp":    now.Add(-2 * time.Minute).Format("2006-01-02 15:04:05"),
			"participants": "2",

			"trx1_thread_id": "1001",
			"trx1_user":      "app",
			"trx1_host":      "10.0.0.1",
			"trx1_query":     "UPDATE t SET x=1 WHERE id=1",

			"trx2_thread_id": "1002",
			"trx2_user":      "",
			"trx2_host":      "",
			"trx2_query":     "",
		},
	}}

	got := buildIssueRows(issues, snap)
	require.Len(t, got, 2, "deadlock should fan out into one row per participant")

	byPID := map[uint64]issueRow{}
	for _, r := range got {
		byPID[r.pid] = r
	}

	r1 := byPID[1001]
	assert.Equal(t, "deadlock", r1.kind)
	assert.Equal(t, "app", r1.user)
	assert.Equal(t, "UPDATE t SET x=1 WHERE id=1", r1.query)
	// Live trx age (~30s) preferred over deadlock timestamp (~2m).
	assert.InDelta(t, 30, r1.age.Seconds(), 2)

	r2 := byPID[1002]
	assert.Equal(t, "live-app", r2.user, "missing user backfilled from process list")
	assert.Equal(t, "DELETE FROM t WHERE id=2", r2.query, "missing query backfilled from process list")
	// No live trx for 1002 → falls back to deadlock timestamp age (~2m).
	assert.InDelta(t, 120, r2.age.Seconds(), 2)
}

func TestVisibleIssueRows_FilterByTable(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "1", "duration": "5m", "query": "SELECT * FROM `alice`.`hskp_message`"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "2", "duration": "5m", "query": "SELECT * FROM other"}},
	}
	all := visibleIssueRows(issues, db.Snapshot{}, "")
	assert.Len(t, all, 2)

	filtered := visibleIssueRows(issues, db.Snapshot{}, "alice.hskp_message")
	require.Len(t, filtered, 1)
	assert.Equal(t, uint64(1), filtered[0].pid)
}

func TestVisibleIssueRows_FilterByNoTableBucket(t *testing.T) {
	// BEGIN classifies as a transaction-control statement, so the
	// filter must use that bucket label rather than the legacy
	// single "(no table)" string.
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityWarning,
			Details: map[string]string{"thread_id": "1", "duration": "1s", "query": "BEGIN"}},
		{Detector: "long_transaction", Severity: detector.SeverityWarning,
			Details: map[string]string{"thread_id": "2", "duration": "1s", "query": "SELECT * FROM users"}},
	}
	got := visibleIssueRows(issues, db.Snapshot{}, bucketTxnControl)
	require.Len(t, got, 1)
	assert.Equal(t, uint64(1), got[0].pid)
}

func TestBuildIssueRows_DeadlockFallbackWhenParticipantsMissing(t *testing.T) {
	issues := []detector.Issue{{
		Detector: "deadlock",
		Severity: detector.SeverityCritical,
		Title:    "Deadlock detected",
		Details:  map[string]string{}, // no "participants" key
	}}
	got := buildIssueRows(issues, db.Snapshot{})
	require.Len(t, got, 1, "issue must not be silently dropped")
	assert.Equal(t, "deadlock", got[0].kind)
}
