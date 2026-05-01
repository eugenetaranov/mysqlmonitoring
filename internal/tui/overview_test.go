package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/stretchr/testify/assert"
)

func makeOverviewModel() Model {
	return Model{
		width:        120,
		height:       40,
		view:         ViewOverview,
		loadGrouping: insights.GroupKeyUser,
		loadWindow:   time.Hour,
		result: monitor.Result{
			Snapshot: db.Snapshot{
				Time: time.Date(2026, 4, 28, 10, 42, 0, 0, time.UTC),
				ServerInfo: db.ServerInfo{
					Version: "8.0.36",
				},
			},
		},
	}
}

func TestRenderOverview_HealthyVerdictWithNoInsights(t *testing.T) {
	m := makeOverviewModel()
	out := renderOverview(m)
	assert.Contains(t, out, "[HEALTHY]")
	// No replication panel when no replica role is detected.
	assert.NotContains(t, out, "Replication")
	// Sparkline collapses to a notice when insights is nil.
	assert.Contains(t, out, "perf-insights disabled")
}

func TestRenderOverview_WarnTriggeredByHLL(t *testing.T) {
	m := makeOverviewModel()
	m.result.Snapshot.InnoDBStatus.HistoryListLength = 2_000_000 // > warn threshold (1M)
	out := renderOverview(m)
	assert.Contains(t, out, "[WARN]")
	assert.Contains(t, out, "HLL 2.0M")
}

func TestRenderOverview_PageTriggeredByHLL(t *testing.T) {
	m := makeOverviewModel()
	m.result.Snapshot.InnoDBStatus.HistoryListLength = 6_000_000 // > page threshold (5M)
	out := renderOverview(m)
	assert.Contains(t, out, "[PAGE]")
	assert.Contains(t, out, "HLL 6.0M")
}

func TestRenderOverview_PageTriggeredByDeadlock(t *testing.T) {
	m := makeOverviewModel()
	m.result.Issues = []detector.Issue{
		{Detector: "deadlock", Severity: detector.SeverityCritical, Title: "deadlock"},
	}
	out := renderOverview(m)
	assert.Contains(t, out, "[PAGE]")
	assert.Contains(t, out, "dl 1")
}

func TestRenderOverview_LiveIssuesPanelShowsHealthyMessage(t *testing.T) {
	m := makeOverviewModel()
	out := renderOverview(m)
	assert.Contains(t, out, "Live issues")
	assert.Contains(t, out, "System healthy.")
}

func TestRenderOverview_LiveIssuesPanelShowsTopThree(t *testing.T) {
	m := makeOverviewModel()
	m.result.Issues = []detector.Issue{
		{Detector: "deadlock", Severity: detector.SeverityCritical, Title: "deadlock 1"},
		{Detector: "long-trx", Severity: detector.SeverityCritical, Title: "long trx"},
		{Detector: "lock-chain", Severity: detector.SeverityWarning, Title: "lock chain"},
	}
	out := renderOverview(m)
	assert.Contains(t, out, "Live issues (3)")
	// Severity badges are rendered for at least the critical rows.
	assert.Contains(t, out, "CRIT")
}

func TestRenderOverview_LoadPanelToggleAffectsTitle(t *testing.T) {
	m := makeOverviewModel()
	m.loadGrouping = insights.GroupKeyHost
	out := renderOverview(m)
	assert.Contains(t, out, "Load by HOST")

	m.loadGrouping = insights.GroupKeySchema
	out = renderOverview(m)
	assert.Contains(t, out, "Load by SCHEMA")

	m.loadGrouping = insights.GroupKeyUser
	out = renderOverview(m)
	assert.Contains(t, out, "Load by USER")
}

func TestRenderOverview_HottestTablesEmptyWhenNoIssues(t *testing.T) {
	m := makeOverviewModel()
	out := renderOverview(m)
	assert.Contains(t, out, "Hottest tables")
	assert.Contains(t, out, "no contention")
}

func TestRenderOverview_HotQueriesShowsDisabledWhenNoInsights(t *testing.T) {
	m := makeOverviewModel()
	out := renderOverview(m)
	assert.Contains(t, out, "Hottest queries")
	assert.Contains(t, out, "perf-insights disabled")
}

func TestComputeVerdict_WarnRunningRatio(t *testing.T) {
	// Synthesize a model that bypasses health by injecting via a
	// fake — we don't have a way to construct an Insights from
	// the test scope without a DB, so instead exercise the snapshot-
	// only verdict path: lock waits should bump severity to WARN.
	m := makeOverviewModel()
	m.result.Issues = []detector.Issue{
		{Detector: "lock-chain", Severity: detector.SeverityWarning},
	}
	v := computeVerdict(m)
	assert.Equal(t, "WARN", v.word)
}

func TestComputeVerdict_PageOnHLLOverThreshold(t *testing.T) {
	m := makeOverviewModel()
	m.result.Snapshot.InnoDBStatus.HistoryListLength = overviewPageHLL + 1
	v := computeVerdict(m)
	assert.Equal(t, "PAGE", v.word)
}

func TestRenderHBar_FullAndEmpty_FormatBytes(t *testing.T) {
	assert.Equal(t, "0B", formatBytes(0))
	assert.Equal(t, "512B", formatBytes(512))
	assert.Equal(t, "2KB", formatBytes(2*1024))
	assert.Equal(t, "5MB", formatBytes(5*1024*1024))
	assert.Equal(t, "1.5GB", formatBytes(1024*1024*1024+512*1024*1024))
}

func TestFormatRate(t *testing.T) {
	assert.Equal(t, "47", formatRate(47.3))
	assert.Equal(t, "1.2k", formatRate(1234))
	assert.Equal(t, "12.5k", formatRate(12500))
}

func TestWrapVerdictParts_BreaksOnOverflow(t *testing.T) {
	parts := []string{"[HEALTHY]", "AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC"}
	got := wrapVerdictParts(parts, 25)
	// Each part is 10 chars; with 2-space gap and 1-char leading
	// space, we can fit "[HEALTHY]  AAAAAAAAAA" (=21 chars) but
	// adding "  BBBBBBBBBB" (12 more) overflows 25 → break.
	lines := strings.Count(got, "\n")
	assert.GreaterOrEqual(t, lines, 1, "expected at least one wrap")
}

func TestWrapVerdictParts_NoOverflowKeepsSingleLine(t *testing.T) {
	parts := []string{"[HEALTHY]", "x"}
	got := wrapVerdictParts(parts, 200)
	assert.NotContains(t, got, "\n")
}

func TestRenderHBar_FullAndEmpty(t *testing.T) {
	full := renderHBar(1.0, 1.0, 8)
	empty := renderHBar(0.0, 1.0, 8)
	assert.Equal(t, strings.Repeat("▰", 8), full)
	assert.Equal(t, strings.Repeat("░", 8), empty)
}

func TestRenderHBar_HalfFilled(t *testing.T) {
	got := renderHBar(0.5, 1.0, 8)
	assert.Equal(t, strings.Repeat("▰", 4)+strings.Repeat("░", 4), got)
}

func TestFormatBigCount(t *testing.T) {
	assert.Equal(t, "42", formatBigCount(42))
	assert.Equal(t, "1.5k", formatBigCount(1500))
	assert.Equal(t, "2.0M", formatBigCount(2_000_000))
	assert.Equal(t, "1.0B", formatBigCount(1_000_000_000))
}

func TestRenderReplicationPanel_LagWarn(t *testing.T) {
	r := &db.ReplicaStatus{
		SourceHost:          "db-01",
		IOThreadRunning:     true,
		SQLThreadRunning:    true,
		SecondsBehindSource: 12,
	}
	out := renderReplicationPanel(r, 38)
	assert.Contains(t, out, "Replication")
	assert.Contains(t, out, "source=db-01")
	assert.Contains(t, out, "lag 12s")
}

func TestRenderReplicationPanel_BrokenThreadHighlighted(t *testing.T) {
	r := &db.ReplicaStatus{
		SourceHost:          "db-01",
		IOThreadRunning:     false,
		SQLThreadRunning:    true,
		SecondsBehindSource: 0,
	}
	out := renderReplicationPanel(r, 38)
	assert.Contains(t, out, "IO=No")
}

func TestOverview_TabBarIncludesOverviewKey(t *testing.T) {
	m := makeOverviewModel()
	bar := renderTabBar(m)
	assert.Contains(t, bar, "O")
	assert.Contains(t, bar, "Overview")
}

func TestOverview_DefaultViewIsOverview(t *testing.T) {
	m := NewModel(nil, nil, nil, nil)
	assert.Equal(t, ViewOverview, m.view)
}
