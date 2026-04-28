package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// tabDef describes one entry in the top-level tab bar.
type tabDef struct {
	key   string
	label string
	view  ViewState
}

// orderedTabs is the left-to-right tab order. ViewTop is gated on
// the perf-insights subsystem being available; orderedTabs always
// lists it but renderTabBar filters at draw time.
var orderedTabs = []tabDef{
	{"O", "Overview", ViewOverview},
	{"I", "Issues", ViewIssues},
	{"B", "Tables", ViewTables},
	{"M", "MDL", ViewMDL},
	{"L", "Lock", ViewLock},
	{"t", "Top SQL", ViewTop},
}

// effectiveTabView reports which top-level tab a (possibly modal)
// view belongs to so the tab bar always shows a coherent active
// highlight regardless of whether the user has popped open a detail
// panel or the help overlay.
func effectiveTabView(m Model) ViewState {
	v := m.view
	if v == ViewHelp {
		v = m.helpReturn
	}
	switch v {
	case ViewIssueDetail:
		return ViewIssues
	case ViewExplain:
		return ViewTop
	}
	return v
}

var (
	// activeTabStyle reuses the same blue as titleStyle so the
	// active tab visually anchors the screen.
	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)

	// inactiveTabStyle is muted enough that it never competes with
	// data underneath, but readable enough to scan in one glance.
	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Background(lipgloss.Color("236")).
				Padding(0, 1)

	// tabKeyStyle paints the leading shortcut key in a distinct
	// colour so the eye finds the keybinding before the label.
	tabKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220")) // amber

	// tabKeyActiveStyle is the on-active variant so the key still
	// stands out against the blue background.
	tabKeyActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("228"))

	// tabBarStyle pads the bar to the full terminal width so the
	// "fill" between right-edge content and the screen edge picks
	// up the inactive background colour rather than terminal black.
	tabBarFillBG color.Color = lipgloss.Color("236")
)

// renderTabBar draws a zellij/tmux-style tab strip across the top of
// the screen. The active tab is highlighted; inactive tabs are
// muted. The bar is padded to the full terminal width so it reads
// as a clear horizontal band even on wide screens.
func renderTabBar(m Model) string {
	active := effectiveTabView(m)

	var parts []string
	for _, t := range orderedTabs {
		if t.view == ViewTop && m.insights == nil {
			continue
		}
		key := tabKeyStyle.Render(t.key)
		styledLabel := fmt.Sprintf("%s %s", key, t.label)
		if t.view == active {
			styledLabel = fmt.Sprintf("%s %s",
				tabKeyActiveStyle.Render(t.key), t.label)
			parts = append(parts, activeTabStyle.Render(styledLabel))
			continue
		}
		parts = append(parts, inactiveTabStyle.Render(styledLabel))
	}

	bar := strings.Join(parts, "")

	// Pad to full width with the inactive background so the bar
	// extends edge-to-edge like a tmux/zellij status line.
	width := m.width
	if width <= 0 {
		return bar
	}
	visible := lipgloss.Width(bar)
	if visible >= width {
		return bar
	}
	pad := lipgloss.NewStyle().
		Background(tabBarFillBG).
		Render(strings.Repeat(" ", width-visible))
	return bar + pad
}
