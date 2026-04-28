package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/explain"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
	"github.com/stretchr/testify/assert"
)

func TestRenderSpark_EmptyTrailRendersBlanks(t *testing.T) {
	got := renderSpark([]float64{0, 0, 0})
	assert.Equal(t, "   ", got)
}

func TestRenderSpark_HighestValueIsFullBlock(t *testing.T) {
	got := renderSpark([]float64{0.0, 0.5, 1.0})
	runes := []rune(got)
	assert.Len(t, runes, 3)
	assert.Equal(t, '█', runes[2], "max should map to the highest block")
}

func TestRenderSparklineHeader_IncludesLabelAndLegend(t *testing.T) {
	header := renderSparklineHeader(120, []float64{0.1, 0.5, 1.0}, insights.LoadBreakdown{
		Total: 1.5,
		Classes: []insights.ClassLoad{
			{Class: series.WaitClassCPU, AAS: 1.0},
			{Class: series.WaitClassIO, AAS: 0.5},
		},
	})
	assert.Contains(t, header, "DB Load")
	assert.Contains(t, header, "CPU 1.00")
	assert.Contains(t, header, "IO 0.50")
	assert.Contains(t, header, "1.50")
}

func TestTruncateTrail_ShorterThanWidthLeftPads(t *testing.T) {
	got := truncateTrail([]float64{1, 2}, 5)
	assert.Equal(t, []float64{0, 0, 0, 1, 2}, got)
}

func TestTruncateTrail_LongerThanWidthKeepsRecent(t *testing.T) {
	got := truncateTrail([]float64{1, 2, 3, 4, 5}, 3)
	assert.Equal(t, []float64{3, 4, 5}, got)
}

func TestRenderTopPanel_EmptyDataShowsHint(t *testing.T) {
	m := Model{width: 100, insights: nil, view: ViewTop}
	out := renderTopPanel(m)
	assert.Contains(t, out, "Top SQL")
	assert.Contains(t, out, "no digests yet")
}

func TestRenderTopPanel_DataRendersHeaderAndRows(t *testing.T) {
	m := Model{width: 120, view: ViewTop, topData: []insights.DigestSummary{
		{Schema: "app", Digest: "abc", Text: "SELECT 1", Calls: 5, AAS: 1.5,
			AvgLatency: 250 * time.Millisecond},
	}}
	out := renderTopPanel(m)
	assert.Contains(t, out, "AAS")
	assert.Contains(t, out, "Calls/s")
	assert.Contains(t, out, "1.50")
	assert.Contains(t, out, "SELECT 1")
}

func TestRenderTopPanel_SortKeyShownInHeader(t *testing.T) {
	m := Model{width: 120, view: ViewTop, topSort: insights.SortByCalls,
		topData: []insights.DigestSummary{{Digest: "a", Text: "X", Calls: 1, AAS: 0.1}}}
	out := renderTopPanel(m)
	assert.Contains(t, out, "sort=calls")
}

func TestComputeTrail_HandlesNilInsights(t *testing.T) {
	got := computeTrail(nil, time.Now(), time.Hour)
	assert.Empty(t, got)
}

func TestRenderExplainModal_RunningState(t *testing.T) {
	m := Model{width: 100, view: ViewExplain}
	out := renderExplainModal(m)
	assert.Contains(t, out, "EXPLAIN")
	assert.Contains(t, out, "running")
}

func TestRenderExplainModal_SkippedShowsReason(t *testing.T) {
	skip := "no recent example for digest"
	m := Model{width: 100, view: ViewExplain, explainResult: &explain.Result{
		Digest: "d1", Skipped: true, SkipReason: skip,
	}}
	out := renderExplainModal(m)
	assert.Contains(t, out, "skipped")
	assert.Contains(t, out, skip)
}

func TestRenderExplainModal_ShowsRedFlagsAndPlan(t *testing.T) {
	m := Model{width: 100, view: ViewExplain, explainResult: &explain.Result{
		Digest:   "d1",
		PlanText: "  table users access=ALL rows_examined=100000",
		PlanHash: "abc123",
		RedFlags: []explain.RedFlag{
			{NodePath: "qb.table", Kind: explain.FlagFullScan, Detail: "users: full scan over 100000 rows"},
		},
	}}
	out := renderExplainModal(m)
	assert.Contains(t, out, "FULL SCAN")
	assert.Contains(t, out, "users: full scan")
	assert.Contains(t, out, "table users access=ALL")
}

func TestRenderTopPanel_ContainsHeaderRow(t *testing.T) {
	out := renderTopPanel(Model{width: 100, view: ViewTop, topData: []insights.DigestSummary{
		{Digest: "a", Text: "X", Calls: 1, AAS: 0.1},
	}})
	// Sanity-check stable column names so the snapshot doesn't drift silently.
	for _, want := range []string{"AAS", "Calls/s", "Calls", "Avg", "Rows ex.", "Schema", "Digest"} {
		assert.True(t, strings.Contains(out, want), "missing column: "+want)
	}
}
