package tui

import (
	"fmt"
	"strings"
)

// helpEntry is one row in the keybinding overlay.
type helpEntry struct {
	keys string
	desc string
}

// helpSection groups related bindings under a heading.
type helpSection struct {
	title   string
	entries []helpEntry
}

// helpSectionsFor returns the bindings that apply to the view we
// were in when '?' was pressed, plus the global section.
func helpSectionsFor(returnView ViewState, hasInsights bool) []helpSection {
	global := helpSection{
		title: "Global",
		entries: []helpEntry{
			{"?", "show / hide this help"},
			{"tab", "cycle Issues → Tables → Lock Tree"},
			{"I", "jump to Issues view"},
			{"L", "jump to Lock Tree view"},
			{"B", "jump to Tables view (issues grouped by table)"},
			{"t", "jump to Top SQL view (perf insights only)"},
			{"esc", "back to previous view"},
			{"q / ctrl+c", "quit"},
		},
	}
	if !hasInsights {
		// Strip the perf-only entry when the feature is off.
		filtered := global.entries[:0]
		for _, e := range global.entries {
			if !strings.Contains(e.desc, "perf insights") {
				filtered = append(filtered, e)
			}
		}
		global.entries = filtered
	}

	var view helpSection
	switch returnView {
	case ViewIssues:
		view = helpSection{
			title: "Issues",
			entries: []helpEntry{
				{"j / ↓", "move cursor down"},
				{"k / ↑", "move cursor up"},
				{"g", "jump to top"},
				{"G", "jump to bottom"},
				{"enter / v", "open issue detail (full query)"},
				{"y", "copy selected query to clipboard (OSC 52)"},
				{"K", "kill the selected connection (with confirm)"},
				{"/", "clear table filter (set by drilling in from Tables)"},
			},
		}
	case ViewTables:
		view = helpSection{
			title: "Tables",
			entries: []helpEntry{
				{"j / ↓", "next table"},
				{"k / ↑", "previous table"},
				{"g / G", "jump to top / bottom"},
				{"enter", "drill into Issues filtered by this table"},
				{"K", "kill ALL connections on this table (with confirm)"},
			},
		}
	case ViewIssueDetail:
		view = helpSection{
			title: "Issue Detail",
			entries: []helpEntry{
				{"y", "copy query to clipboard"},
				{"K", "kill this connection"},
				{"esc", "back to Issues table"},
			},
		}
	case ViewLock:
		view = helpSection{
			title: "Lock Tree",
			entries: []helpEntry{
				{"j / ↓", "next blocker"},
				{"k / ↑", "previous blocker"},
				{"K", "kill the selected blocker"},
			},
		}
	case ViewTop:
		view = helpSection{
			title: "Top SQL",
			entries: []helpEntry{
				{"j / ↓", "next digest"},
				{"k / ↑", "previous digest"},
				{"s", "cycle sort: AAS → calls → latency → rows-examined"},
				{"e / enter", "EXPLAIN the highlighted digest"},
			},
		}
	case ViewExplain:
		view = helpSection{
			title: "EXPLAIN",
			entries: []helpEntry{
				{"esc", "back to Top SQL"},
			},
		}
	}

	if view.title == "" {
		return []helpSection{global}
	}
	return []helpSection{view, global}
}

// renderHelp draws the keybindings overlay. The overlay completely
// replaces the body of the current view so users do not have to
// search through the regular UI for the legend.
func renderHelp(m Model) string {
	sections := helpSectionsFor(m.helpReturn, m.insights != nil)

	var b strings.Builder
	b.WriteString(headerStyle.Render("Keyboard shortcuts"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  press any key to dismiss"))
	b.WriteString("\n\n")

	for i, sec := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(headerStyle.Render(sec.title))
		b.WriteString("\n")
		for _, e := range sec.entries {
			b.WriteString(fmt.Sprintf("    %-12s  %s\n", e.keys, e.desc))
		}
	}
	return b.String()
}
