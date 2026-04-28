package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
)

// renderMDL dispatches to list or detail based on the model's mode.
func renderMDL(m Model) string {
	if !mdlInstrumentEnabled(m) {
		return renderMDLDisabledNotice(m)
	}
	mdl := insights.BuildMDL(m.result.Snapshot)
	if m.mdlMode == MDLModeDetail {
		return renderMDLDetail(m, mdl)
	}
	return renderMDLList(m, mdl)
}

// mdlInstrumentEnabled returns whether the operator can expect rows
// in performance_schema.metadata_locks. We assume yes when insights
// hasn't probed yet — the view will simply be empty until a probe
// happens, instead of preemptively warning.
func mdlInstrumentEnabled(m Model) bool {
	if m.insights == nil {
		// Insights always starts in the new wiring, but tests may
		// construct models without it. Assume enabled so the test
		// paths exercise the real renderers.
		return true
	}
	caps := m.insights.Capabilities()
	// MDLAvailable defaults to false on a zero PerfCapabilities
	// (e.g. probe hasn't run yet). Treat zero-cap probe as "unknown
	// → assume enabled" so cold starts don't render a misleading
	// warning. The Probe runs on first call to insights.Run, so
	// this branch is only relevant in pre-probe windows.
	if !caps.DigestAvailable && !caps.WaitsAvailable && !caps.MDLAvailable {
		return true
	}
	return caps.MDLAvailable
}

func renderMDLDisabledNotice(m Model) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("MDL queue"))
	b.WriteString("\n\n")
	b.WriteString(warningStyle.Render(
		"  metadata_locks instrumentation is OFF; the queue is empty"))
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("  Enable it on the server with:"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(
		"    UPDATE performance_schema.setup_instruments"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(
		"       SET ENABLED='YES', TIMED='YES'"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(
		"     WHERE NAME='wait/lock/metadata/sql/mdl';"))
	b.WriteString("\n")
	return b.String()
}

func renderMDLList(m Model, mdl insights.MDLBreakdown) string {
	var b strings.Builder
	hdr := "MDL queue · hottest tables"
	if len(mdl.Tables) > 0 {
		hdr = fmt.Sprintf("MDL queue · hottest tables (%d)", len(mdl.Tables))
	}
	b.WriteString(headerStyle.Render(hdr))
	b.WriteString("\n\n")

	if len(mdl.Tables) == 0 {
		b.WriteString(dimStyle.Render(
			"  No metadata locks observed. Either nothing is contended"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(
			"  right now, or wait/lock/metadata/sql/mdl is OFF — see ?:help"))
		b.WriteString("\n")
		return b.String()
	}

	header := fmt.Sprintf("  %-32s %-5s %-5s  %-50s %s",
		"TABLE", "PEND", "GRANT", "TYPES", "OLDEST")
	b.WriteString(dimStyle.Render(header))
	b.WriteString("\n")

	w := widthOr120(m.width)
	for i, q := range mdl.Tables {
		oldest := ""
		if len(q.Pending) > 0 {
			oldest = humanDuration(q.Pending[0].WaitSeconds * 1000)
		}
		tableName := q.Schema + "." + q.Name
		row := fmt.Sprintf("  %-32s %-5d %-5d  %-50s %s",
			truncateStr(tableName, 32),
			len(q.Pending), len(q.Granted),
			truncateStr(formatLockTypeBuckets(q.ByLockType), 50),
			oldest,
		)
		if i == m.mdlListCursor && m.view == ViewMDL && m.mdlMode == MDLModeList {
			b.WriteString(selectedStyle.Width(w).Render(">" + row[1:]))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(
		"  ↑↓ select  enter:detail  esc:overview"))
	b.WriteString("\n")
	return b.String()
}

func renderMDLDetail(m Model, mdl insights.MDLBreakdown) string {
	q := mdl.Find(m.mdlTableSchema, m.mdlTableName)
	var b strings.Builder

	title := fmt.Sprintf("MDL queue · %s.%s", m.mdlTableSchema, m.mdlTableName)
	b.WriteString(headerStyle.Render(title))
	b.WriteString("\n")

	if q == nil {
		b.WriteString(dimStyle.Render(
			"  No metadata locks on this table. Either contention cleared,"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(
			"  or this table never had any. Press esc to return."))
		b.WriteString("\n")
		return b.String()
	}

	oldest := ""
	if len(q.Pending) > 0 {
		oldest = humanDuration(q.Pending[0].WaitSeconds * 1000)
	}
	summary := fmt.Sprintf("  %d waiters · %d holders",
		len(q.Pending), len(q.Granted))
	if oldest != "" {
		summary += " · longest wait " + oldest
	}
	b.WriteString(dimStyle.Render(summary))
	b.WriteString("\n\n")

	// QUEUE panel
	b.WriteString(headerStyle.Render("  QUEUE  "))
	b.WriteString(dimStyle.Render("(waiters, longest wait first)"))
	b.WriteString("\n")
	if len(q.Pending) == 0 {
		b.WriteString(dimStyle.Render("    (empty)"))
		b.WriteString("\n")
	} else {
		b.WriteString(dimStyle.Render(fmt.Sprintf("    %-3s %-8s %-8s %-28s %-22s %s",
			"#", "AGE", "PID", "USER@HOST", "LOCK", "QUERY")))
		b.WriteString("\n")
		w := widthOr120(m.width)
		room := w - 78 // padded prefix + fixed columns
		if room < 12 {
			room = 12
		}
		for i, e := range q.Pending {
			age := humanDuration(e.WaitSeconds * 1000)
			userHost := truncateStr(e.User+"@"+stripPort(e.Host), 28)
			row := fmt.Sprintf("    %-3d %-8s %-8d %-28s %-22s %s",
				i+1, age, e.PID, userHost,
				truncateStr(e.LockType, 22),
				truncateStr(queryLabel("", e.Query), room),
			)
			selected := i == m.mdlQueueCursor && m.view == ViewMDL && m.mdlMode == MDLModeDetail
			if selected {
				b.WriteString(selectedStyle.Width(w).Render(" >  " + row[4:]))
			} else {
				b.WriteString(row)
			}
			b.WriteString("\n")
		}
	}

	// HOLDERS panel
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("  HOLDERS  "))
	holders := q.Granted
	if m.mdlBlockerFilter && m.mdlQueueCursor < len(q.Pending) {
		holders = q.BlockersOf(q.Pending[m.mdlQueueCursor].PID)
		b.WriteString(warningStyle.Render(fmt.Sprintf(
			"(filtered to entries blocking PID %d's %s request)",
			q.Pending[m.mdlQueueCursor].PID,
			q.Pending[m.mdlQueueCursor].LockType,
		)))
	} else {
		b.WriteString(dimStyle.Render("(granted; press B to filter to current waiter's blockers)"))
	}
	b.WriteString("\n")
	if len(holders) == 0 {
		if m.mdlBlockerFilter {
			b.WriteString(dimStyle.Render(
				"    (no granted holders block this waiter — likely an"))
			b.WriteString("\n")
			b.WriteString(dimStyle.Render(
				"    earlier-pending PENDING request is in front of it)"))
			b.WriteString("\n")
		} else {
			b.WriteString(dimStyle.Render("    (none)"))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(dimStyle.Render(fmt.Sprintf("    %-8s %-28s %-22s %-12s %s",
			"PID", "USER@HOST", "LOCK", "DURATION", "QUERY")))
		b.WriteString("\n")
		w := widthOr120(m.width)
		room := w - 80
		if room < 12 {
			room = 12
		}
		for _, h := range holders {
			userHost := truncateStr(h.User+"@"+stripPort(h.Host), 28)
			row := fmt.Sprintf("    %-8d %-28s %-22s %-12s %s",
				h.PID, userHost,
				truncateStr(h.LockType, 22),
				truncateStr(h.LockDuration, 12),
				truncateStr(queryLabel("", h.Query), room),
			)
			b.WriteString(row)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(
		"  ↑↓ select waiter  B:blockers-only  K:kill  esc:back"))
	b.WriteString("\n")
	return b.String()
}

// formatLockTypeBuckets renders the per-LOCK_TYPE pending counter map
// as a compact "38×SHARED_READ 6×SHARED_WRITE 2×EXCLUSIVE" string,
// sorted by count desc.
func formatLockTypeBuckets(m map[string]int) string {
	if len(m) == 0 {
		return "—"
	}
	type kv struct {
		k string
		v int
	}
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].v != out[j].v {
			return out[i].v > out[j].v
		}
		return out[i].k < out[j].k
	})
	var parts []string
	for _, e := range out {
		parts = append(parts, fmt.Sprintf("%d×%s", e.v, e.k))
	}
	return strings.Join(parts, " ")
}

// handleMDLKey processes navigation/actions inside ViewMDL.
func (m Model) handleMDLKey(key string) (tea.Model, tea.Cmd) {
	mdl := insights.BuildMDL(m.result.Snapshot)
	if m.mdlMode == MDLModeList {
		return m.handleMDLListKey(key, mdl)
	}
	return m.handleMDLDetailKey(key, mdl)
}

func (m Model) handleMDLListKey(key string, mdl insights.MDLBreakdown) (tea.Model, tea.Cmd) {
	rows := mdl.Tables
	switch key {
	case "up", "k":
		if m.mdlListCursor > 0 {
			m.mdlListCursor--
		}
		return m, nil
	case "down", "j":
		if m.mdlListCursor < len(rows)-1 {
			m.mdlListCursor++
		}
		return m, nil
	case "g":
		m.mdlListCursor = 0
		return m, nil
	case "G":
		if len(rows) > 0 {
			m.mdlListCursor = len(rows) - 1
		}
		return m, nil
	case "enter":
		if m.mdlListCursor >= len(rows) {
			return m, nil
		}
		picked := rows[m.mdlListCursor]
		m.mdlMode = MDLModeDetail
		m.mdlTableSchema = picked.Schema
		m.mdlTableName = picked.Name
		m.mdlQueueCursor = 0
		m.mdlBlockerFilter = false
		return m, nil
	}
	return m, nil
}

func (m Model) handleMDLDetailKey(key string, mdl insights.MDLBreakdown) (tea.Model, tea.Cmd) {
	q := mdl.Find(m.mdlTableSchema, m.mdlTableName)
	switch key {
	case "up", "k":
		if m.mdlQueueCursor > 0 {
			m.mdlQueueCursor--
		}
		return m, nil
	case "down", "j":
		if q != nil && m.mdlQueueCursor < len(q.Pending)-1 {
			m.mdlQueueCursor++
		}
		return m, nil
	case "g":
		m.mdlQueueCursor = 0
		return m, nil
	case "G":
		if q != nil && len(q.Pending) > 0 {
			m.mdlQueueCursor = len(q.Pending) - 1
		}
		return m, nil
	case "B":
		// Toggle the HOLDERS panel between "all granted" and
		// "filtered to those blocking the current QUEUE row".
		m.mdlBlockerFilter = !m.mdlBlockerFilter
		return m, nil
	case "K":
		if m.killer == nil || q == nil || m.mdlQueueCursor >= len(q.Pending) {
			return m, nil
		}
		pid := q.Pending[m.mdlQueueCursor].PID
		if pid == 0 {
			m.statusMsg = "selected waiter has no killable PID"
			return m, nil
		}
		m.confirmKill = true
		m.confirmPID = pid
		return m, nil
	}
	return m, nil
}
