package tui

import (
	"strings"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/stretchr/testify/assert"
)

// stripANSI removes lipgloss-emitted escape sequences so test
// assertions can match on visible text content without caring about
// styling.
func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestRenderTabBar_ShowsCoreTabs(t *testing.T) {
	m := Model{width: 100, view: ViewIssues, insights: nil}
	got := stripANSI(renderTabBar(m))
	assert.Contains(t, got, "Issues")
	assert.Contains(t, got, "Tables")
	assert.Contains(t, got, "Lock")
	assert.NotContains(t, got, "Top SQL", "Top SQL tab hidden when perf insights is off")
}

func TestRenderTabBar_IncludesTopSQLWithInsights(t *testing.T) {
	// renderTabBar only checks insights for non-nilness; an empty
	// Insights struct is enough.
	m := Model{width: 100, view: ViewIssues, insights: &insights.Insights{}}
	got := stripANSI(renderTabBar(m))
	assert.Contains(t, got, "Top SQL")
}

func TestEffectiveTabView_ModalsMapToParent(t *testing.T) {
	cases := map[ViewState]ViewState{
		ViewIssues:      ViewIssues,
		ViewTables:      ViewTables,
		ViewLock:        ViewLock,
		ViewTop:         ViewTop,
		ViewIssueDetail: ViewIssues,
		ViewExplain:     ViewTop,
	}
	for v, want := range cases {
		got := effectiveTabView(Model{view: v})
		assert.Equalf(t, want, got, "view=%d", v)
	}
}

func TestEffectiveTabView_HelpHonoursReturnView(t *testing.T) {
	got := effectiveTabView(Model{view: ViewHelp, helpReturn: ViewTables})
	assert.Equal(t, ViewTables, got)

	// Help opened from a modal should still resolve to the parent tab.
	got = effectiveTabView(Model{view: ViewHelp, helpReturn: ViewIssueDetail})
	assert.Equal(t, ViewIssues, got)
}

func TestRenderTabBar_PadsToFullWidth(t *testing.T) {
	m := Model{width: 200, view: ViewIssues}
	got := renderTabBar(m)
	visible := stripANSI(got)
	assert.GreaterOrEqual(t, len(visible), 100,
		"tab bar should pad out toward terminal width")
}
