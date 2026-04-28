package tui

import (
	"strings"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTableRows_AggregatesAndSorts(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "1", "duration": "10m", "query": "SELECT * FROM `alice`.`hskp_message` WHERE id=?"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "2", "duration": "20m", "query": "UPDATE `alice`.`hskp_message` SET x=?"}},
		{Detector: "long_transaction", Severity: detector.SeverityWarning,
			Details: map[string]string{"thread_id": "3", "duration": "30s", "query": "SELECT * FROM other"}},
	}
	rows := buildTableRows(issues, db.Snapshot{})
	require.Len(t, rows, 2, "two distinct tables expected")

	assert.Equal(t, "alice.hskp_message", rows[0].name, "critical+busy table sorts first")
	assert.Equal(t, 2, rows[0].issueCount)
	assert.Equal(t, detector.SeverityCritical, rows[0].maxSev)
	assert.Equal(t, "20m", rows[0].oldestStr, "oldest age picks longer duration")
	assert.ElementsMatch(t, []uint64{1, 2}, rows[0].pids)

	assert.Equal(t, "other", rows[1].name)
	assert.Equal(t, detector.SeverityWarning, rows[1].maxSev)
}

func TestBuildTableRows_NoTableBucket(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityWarning,
			Details: map[string]string{"thread_id": "1", "duration": "5s", "query": "BEGIN"}},
		{Detector: "long_transaction", Severity: detector.SeverityWarning,
			Details: map[string]string{"thread_id": "2", "duration": "10s", "query": "COMMIT"}},
	}
	rows := buildTableRows(issues, db.Snapshot{})
	require.Len(t, rows, 1)
	assert.Equal(t, bucketTxnControl, rows[0].name)
	assert.Equal(t, 2, rows[0].issueCount)
}

func TestBucketForUntableable_Categories(t *testing.T) {
	cases := map[string]string{
		"":                                          bucketIdle,
		"   ":                                       bucketIdle,
		"/* hint */":                                bucketIdle,
		"BEGIN":                                     bucketTxnControl,
		"COMMIT":                                    bucketTxnControl,
		"ROLLBACK TO `sp1`":                         bucketTxnControl,
		"START TRANSACTION READ ONLY":               bucketTxnControl,
		"SAVEPOINT sp1":                             bucketTxnControl,
		"SET autocommit=0":                          bucketSessionAdmin,
		"SET NAMES utf8mb4":                         bucketSessionAdmin,
		"SHOW VARIABLES":                            bucketSessionAdmin,
		"USE alice":                                 bucketSessionAdmin,
		"SELECT @@SESSION . `transaction_isolation`": bucketSessionAdmin,
		"SELECT `this_` . `id` AS `id1_264_1_` ...": bucketDigestTrunc,
		"SELECT 1":                                  bucketNoTableRef,
		"SELECT NOW()":                              bucketNoTableRef,
	}
	for in, want := range cases {
		assert.Equalf(t, want, bucketForUntableable(in), "input=%q", in)
	}
}

func TestBuildTableRows_SplitNoTableBuckets(t *testing.T) {
	issues := []detector.Issue{
		// 3 admin queries
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "1", "duration": "1m", "query": "SELECT @@SESSION.transaction_isolation"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "2", "duration": "1m", "query": "SELECT @@SESSION.transaction_isolation"}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "3", "duration": "1m", "query": "SET autocommit=0"}},
		// 2 truncated digests
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "4", "duration": "2m", "query": "SELECT `this_` . `id` AS `id1_264_1_` ..."}},
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "5", "duration": "2m", "query": "SELECT `this_` . `id` AS `id1_123_4_` ..."}},
	}
	rows := buildTableRows(issues, db.Snapshot{})
	byName := map[string]int{}
	for _, r := range rows {
		byName[r.name] = r.issueCount
	}
	assert.Equal(t, 3, byName[bucketSessionAdmin])
	assert.Equal(t, 2, byName[bucketDigestTrunc])
	assert.NotContains(t, byName, "(no table)",
		"the legacy single bucket should no longer collect everything")
}

func TestBuildTableRows_LockChainUsesDetailLockTable(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "lock_chain", Severity: detector.SeverityCritical,
			Details: map[string]string{
				"chain_depth":  "3",
				"root_blocker": "100",
				"lock_table":   "alice.hskp_board_aggregate",
				"root_query":   "/* hint */ SELECT 1", // unparseable on purpose
			}},
	}
	rows := buildTableRows(issues, db.Snapshot{})
	require.Len(t, rows, 1)
	assert.Equal(t, "alice.hskp_board_aggregate", rows[0].name,
		"lock_chain.lock_table must win over query parsing")
}

func TestBuildTableRows_DeadlockFanOutAggregatesPerTable(t *testing.T) {
	issues := []detector.Issue{{
		Detector: "deadlock", Severity: detector.SeverityCritical,
		Details: map[string]string{
			"timestamp":      "2026-01-01 00:00:00",
			"participants":   "2",
			"trx1_thread_id": "10", "trx1_table": "alice.hskp_task_request", "trx1_query": "SELECT 1",
			"trx2_thread_id": "11", "trx2_table": "alice.hskp_board_aggregate", "trx2_query": "SELECT 2",
		},
	}}
	rows := buildTableRows(issues, db.Snapshot{})
	require.Len(t, rows, 2)
	names := []string{rows[0].name, rows[1].name}
	assert.ElementsMatch(t, []string{"alice.hskp_task_request", "alice.hskp_board_aggregate"}, names)
}

func TestComputeTablesColWidths_KeepsTableColAtMin(t *testing.T) {
	narrow := computeTablesColWidths(40)
	assert.Equal(t, 24, narrow.table, "table column floors at 24 cells")
	wide := computeTablesColWidths(160)
	assert.Greater(t, wide.table, 24)
}

func TestTableRowKillPIDs_ReturnsCopy(t *testing.T) {
	r := tableRow{pids: []uint64{1, 2, 3}}
	got := r.killPIDs()
	got[0] = 999
	assert.Equal(t, uint64(1), r.pids[0], "killPIDs must return a copy, not the underlying slice")
	assert.Equal(t, []uint64{999, 2, 3}, got)
}

func TestRenderTablesView_FilterBannerNotShownWhenNoFilter(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "1", "duration": "10m", "query": "SELECT * FROM users"}},
	}
	m := Model{width: 140, height: 30, view: ViewTables, result: monitorResultFromIssues(issues)}
	out := renderTablesView(m)
	assert.NotContains(t, out, "filter:")
}

func TestRenderTablesView_BulkKillPromptShownWhenConfirming(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "1", "duration": "10m", "query": "SELECT * FROM users"}},
	}
	m := Model{
		width: 140, height: 30, view: ViewTables, result: monitorResultFromIssues(issues),
		confirmKillBatch: []uint64{1, 2, 3}, confirmKillTarget: "alice.hskp_message",
	}
	out := renderTablesView(m)
	assert.Contains(t, out, "Kill 3 connection")
	assert.Contains(t, out, "alice.hskp_message")
}

func TestRenderTablesView_HeaderColumns(t *testing.T) {
	issues := []detector.Issue{
		{Detector: "long_transaction", Severity: detector.SeverityCritical,
			Details: map[string]string{"thread_id": "1", "duration": "10m", "query": "SELECT * FROM users"}},
	}
	m := Model{width: 140, height: 30, view: ViewTables, result: monitorResultFromIssues(issues)}
	out := renderTablesView(m)
	for _, want := range []string{"Tables", "SEV", "ISSUES", "CRIT/WARN/INFO", "OLDEST", "PIDS", "TABLE", "users"} {
		assert.True(t, strings.Contains(out, want), "missing %q in output", want)
	}
}
