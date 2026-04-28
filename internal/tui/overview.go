package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
)

// Verdict band thresholds. These reuse the lock-wait threshold as the
// time-based knob so we don't add new flags now; they can move to
// configurable values later.
const (
	overviewWarnRunningRatio = 0.5
	overviewPageRunningRatio = 0.8
	overviewWarnHLL          = uint64(1_000_000)
	overviewPageHLL          = uint64(5_000_000)
)

// verdict captures the worst severity across all gauges and the
// short reason text for the status line. WORD pairs with COLOR so
// screenshots / colorblind operators don't lose severity.
type verdict struct {
	level   detector.Severity
	word    string
	style   lipgloss.Style
}

// renderOverview is the new default view. The panels degrade
// independently when their data source is missing.
func renderOverview(m Model) string {
	var b strings.Builder

	b.WriteString(renderOverviewVerdictLine(m))
	b.WriteString("\n")
	b.WriteString(renderOverviewSparkline(m))
	b.WriteString("\n")

	b.WriteString(strings.Repeat("─", widthOr120(m.width)))
	b.WriteString("\n")
	b.WriteString(renderOverviewMiddleBand(m))
	b.WriteString(strings.Repeat("─", widthOr120(m.width)))
	b.WriteString("\n")
	b.WriteString(renderOverviewBottomBand(m))

	return b.String()
}

// widthOr120 falls back to a 120-col floor when the terminal hasn't
// reported a size yet (e.g. very early frames or test contexts).
func widthOr120(w int) int {
	if w <= 0 {
		return 120
	}
	if w < 60 {
		return 60
	}
	return w
}

// renderOverviewVerdictLine produces the single status line. The first
// token is always [HEALTHY] / [WARN] / [PAGE] paired with a colour.
func renderOverviewVerdictLine(m Model) string {
	v := computeVerdict(m)

	parts := []string{v.style.Render("[" + v.word + "]")}
	snap := m.result.Snapshot

	if !snap.Time.IsZero() && snap.ServerInfo.Version != "" {
		// We don't carry uptime as a vital today; show snapshot time
		// instead so the operator at least knows the line is live.
		parts = append(parts, dimStyle.Render(snap.Time.Format("15:04:05")))
	}

	// Threads_running and buffer-pool stats come from the health
	// collector; the HLL is parsed from the lock-monitor's existing
	// SHOW ENGINE INNODB STATUS so it renders regardless of whether
	// perf-insights is wired.
	if hv := latestHealth(m); hv != nil {
		runningCol := fmt.Sprintf("running %d", hv.ThreadsRunning)
		if hv.ThreadsConnected > 0 {
			runningCol += fmt.Sprintf("/%d", hv.ThreadsConnected)
		}
		parts = append(parts, runningCol)

		if hv.InnoDBBufferPoolReadRequests > 0 {
			ratio := bufferPoolHitRatio(*hv)
			parts = append(parts, fmt.Sprintf("bp_hit %.0f%%", ratio*100))
		}
	}

	if hll := snap.InnoDBStatus.HistoryListLength; hll > 0 {
		parts = append(parts, "HLL "+formatBigCount(hll))
	}

	if hv := latestHealth(m); hv != nil {
		if hv.Replica != nil && hv.Replica.SecondsBehindSource >= 0 {
			parts = append(parts, fmt.Sprintf("repl +%ds", hv.Replica.SecondsBehindSource))
		}
		if hv.AbortedClientsDelta > 0 {
			parts = append(parts, warningStyle.Render(fmt.Sprintf("aborted +%d", hv.AbortedClientsDelta)))
		}
	}

	// Lock waits + deadlock count come from the snapshot regardless
	// of perf_schema availability.
	if n := len(snap.LockWaits); n > 0 {
		parts = append(parts, criticalStyle.Render(fmt.Sprintf("locks %d", n)))
	}
	if dlCount := countDeadlocks(m.result.Issues); dlCount > 0 {
		parts = append(parts, criticalStyle.Render(fmt.Sprintf("dl %d", dlCount)))
	}

	return " " + strings.Join(parts, "  ")
}

// renderOverviewSparkline shows the AAS-by-wait-class sparkline header
// when perf-insights is wired and has at least one sample, otherwise
// degrades to a one-line notice.
func renderOverviewSparkline(m Model) string {
	if m.insights == nil {
		return " " + dimStyle.Render(
			"DB Load: perf-insights disabled — start with --enable-perf-insights for load attribution")
	}
	if len(m.sparkTrail) == 0 {
		return " " + dimStyle.Render("DB Load: gathering samples…")
	}
	return " " + renderSparklineHeader(widthOr120(m.width)-2, m.sparkTrail, m.currentLoad)
}

// renderOverviewMiddleBand draws the three side-by-side panels:
// Load-by-X, Replication, Live Issues. Width budget is split into
// thirds; if Replication is absent (standalone server) the freed
// width goes to the other two.
func renderOverviewMiddleBand(m Model) string {
	hv := latestHealth(m)
	hasReplica := hv != nil && hv.Replica != nil

	w := widthOr120(m.width)
	gap := 2
	var loadW, replW, issuesW int
	if hasReplica {
		col := (w - 2*gap - 2) / 3
		loadW, replW, issuesW = col, col, w-2-2*gap-loadW-replW
	} else {
		col := (w - gap - 2) / 2
		loadW, issuesW = col, w-2-gap-loadW
	}

	loadCol := renderLoadPanel(m, loadW)
	issuesCol := renderIssuesPanel(m.result.Issues, m.result.Snapshot, 5, issuesW)

	var cols []string
	cols = append(cols, padPanel(loadCol, loadW))
	if hasReplica {
		cols = append(cols, padPanel(renderReplicationPanel(hv.Replica, replW), replW))
	}
	cols = append(cols, padPanel(issuesCol, issuesW))

	return joinHorizontal(cols, gap) + "\n"
}

// renderOverviewBottomBand draws Hottest Queries + Hottest Tables.
func renderOverviewBottomBand(m Model) string {
	w := widthOr120(m.width)
	gap := 2
	col := (w - gap - 2) / 2
	left := renderHottestQueries(m, col)
	right := renderHottestTables(m, col)
	return joinHorizontal([]string{padPanel(left, col), padPanel(right, col)}, gap) + "\n"
}

// renderLoadPanel is the Load-by-USER/HOST/SCHEMA panel.
func renderLoadPanel(m Model, width int) string {
	var b strings.Builder
	title := loadGroupingTitle(m.loadGrouping)
	b.WriteString(headerStyle.Render(title))
	b.WriteString(dimStyle.Render("  (u/h/s)"))
	b.WriteString("\n")

	if m.insights == nil || m.insights.Sessions == nil {
		b.WriteString(dimStyle.Render("  load attribution unavailable"))
		return b.String()
	}
	// performance_schema disabled / wait+session collectors won't
	// run, so there will never be samples to attribute. Say so
	// instead of letting the panel sit on "gathering samples…".
	if caps := m.insights.Capabilities(); !caps.WaitsAvailable {
		b.WriteString(dimStyle.Render("  performance_schema waits disabled"))
		return b.String()
	}

	rows := m.overviewLoadRows()
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  gathering samples…"))
		return b.String()
	}

	// Cap to top 5 to keep the band compact.
	const maxRows = 5
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}

	// Bar width: column width minus name (16) and number (6) and gap.
	nameW := 16
	numW := 6
	barW := width - nameW - numW - 4
	if barW < 4 {
		barW = 4
	}

	maxAAS := 0.0
	for _, r := range rows {
		if r.AAS > maxAAS {
			maxAAS = r.AAS
		}
	}

	for i, r := range rows {
		name := truncateStr(r.Group, nameW)
		bar := renderHBar(r.AAS, maxAAS, barW)
		line := fmt.Sprintf("%-*s %s %5.2f", nameW, name, bar, r.AAS)
		if i == m.overviewCursor && m.view == ViewOverview {
			b.WriteString("> ")
			b.WriteString(selectedStyle.Render(line))
		} else {
			b.WriteString("  ")
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderReplicationPanel renders the replica panel when the server is
// a replica. Returns empty when the caller has already decided to
// suppress the panel.
func renderReplicationPanel(r *db.ReplicaStatus, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Replication"))
	b.WriteString("\n")

	source := r.SourceHost
	if source == "" {
		source = "(unknown)"
	}
	b.WriteString(fmt.Sprintf("  source=%s\n", truncateStr(source, max(8, width-10))))

	io := boolStr(r.IOThreadRunning)
	sql := boolStr(r.SQLThreadRunning)
	thStyle := dimStyle
	if !r.IOThreadRunning || !r.SQLThreadRunning {
		thStyle = warningStyle
	}
	b.WriteString("  ")
	b.WriteString(thStyle.Render(fmt.Sprintf("IO=%s SQL=%s", io, sql)))
	b.WriteString("\n")

	if r.SecondsBehindSource >= 0 {
		lagStyle := dimStyle
		if r.SecondsBehindSource > 5 {
			lagStyle = warningStyle
		}
		if r.SecondsBehindSource > 30 {
			lagStyle = criticalStyle
		}
		b.WriteString("  ")
		b.WriteString(lagStyle.Render(fmt.Sprintf("lag %ds", r.SecondsBehindSource)))
		b.WriteString("\n")
	}

	if r.LastError != "" {
		b.WriteString(criticalStyle.Render("  err: " + truncateStr(r.LastError, max(8, width-8))))
		b.WriteString("\n")
	}
	return b.String()
}

// renderIssuesPanel is a compact issues block for the Overview's middle
// band. Mirrors renderIssuesTable's selection / pagination but with a
// fixed maxRows budget and minimal columns.
func renderIssuesPanel(issues []detector.Issue, snap db.Snapshot, maxRows, width int) string {
	rows := buildIssueRows(issues, snap)

	var b strings.Builder
	hdr := "Live issues"
	if len(rows) > 0 {
		hdr = fmt.Sprintf("Live issues (%d)", len(rows))
	}
	b.WriteString(headerStyle.Render(hdr))
	b.WriteString("\n")

	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  System healthy."))
		if dl := snap.InnoDBStatus.LatestDeadlock; dl != nil && dl.Timestamp != "" {
			b.WriteString("\n")
			b.WriteString(dimStyle.Render("  Last deadlock: " + dl.Timestamp))
		}
		return b.String()
	}

	shown := rows
	if len(rows) > maxRows {
		shown = rows[:maxRows]
	}
	for _, r := range shown {
		badge := severityBadge(r.severity)
		// Prefer the friendly kind label + query so the line reads
		// like "long-trx UPDATE shop.orders ..." rather than just SQL.
		text := r.kind
		if r.query != "" {
			text += " " + r.query
		}
		// Allow for 2-space indent + badge + space; severityBadge
		// uses fixed-length tokens like [CRIT].
		room := width - 9
		if room < 8 {
			room = 8
		}
		b.WriteString("  ")
		b.WriteString(badge)
		b.WriteString(" ")
		b.WriteString(truncateStr(text, room))
		b.WriteString("\n")
	}
	if len(rows) > maxRows {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  + %d more — press I", len(rows)-maxRows)))
		b.WriteString("\n")
	}
	return b.String()
}

// renderHottestQueries shows the top digests by AAS in a compact form.
// Drill is via the existing Top SQL view (press t).
func renderHottestQueries(m Model, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Hottest queries (5m)"))
	b.WriteString("\n")

	if m.insights == nil {
		b.WriteString(dimStyle.Render("  perf-insights disabled"))
		return b.String()
	}
	if caps := m.insights.Capabilities(); !caps.DigestAvailable {
		b.WriteString(dimStyle.Render("  performance_schema digest disabled"))
		return b.String()
	}
	digests := insights.TopSQL(m.insights.Registry, m.insights.Sessions, time.Now(),
		insights.TopSQLOptions{
			Window: m.loadWindow,
			Limit:  5,
			Sort:   insights.SortByAAS,
		})
	if len(digests) == 0 {
		b.WriteString(dimStyle.Render("  gathering samples…"))
		return b.String()
	}
	for _, d := range digests {
		flags := ""
		if d.NoIndexUsedCalls > 0 {
			flags = warningStyle.Render(" no_idx")
		}
		room := width - 18
		if room < 12 {
			room = 12
		}
		b.WriteString(fmt.Sprintf("  AAS %5.2f  %s%s\n",
			d.AAS, truncateStr(d.Text, room), flags))
	}
	return b.String()
}

// renderHottestTables aggregates issue rows by table name. Reuses
// buildTableRows from the existing Tables view to stay consistent.
func renderHottestTables(m Model, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Hottest tables"))
	b.WriteString("\n")

	rows := buildTableRows(m.result.Issues, m.result.Snapshot)
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  no contention"))
		return b.String()
	}
	const maxRows = 5
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	for _, r := range rows {
		flag := ""
		if r.maxSev >= detector.SeverityCritical {
			flag = criticalStyle.Render(" ⚠")
		} else if r.maxSev >= detector.SeverityWarning {
			flag = warningStyle.Render(" ⚠")
		}
		room := width - 22
		if room < 12 {
			room = 12
		}
		b.WriteString(fmt.Sprintf("  %s  iss %-3d pids %-3d%s\n",
			truncateStr(r.name, room), r.issueCount, len(r.pids), flag))
	}
	return b.String()
}

// computeVerdict walks the gauges and returns the worst severity
// observed. Word pairs with colour so the operator can read severity
// in monochrome too.
func computeVerdict(m Model) verdict {
	level := detector.SeverityInfo
	hv := latestHealth(m)

	// Threads_running ratio
	if hv != nil && hv.ThreadsConnected > 0 {
		ratio := float64(hv.ThreadsRunning) / float64(hv.ThreadsConnected)
		if ratio > overviewPageRunningRatio {
			level = bumpSeverity(level, detector.SeverityCritical)
		} else if ratio > overviewWarnRunningRatio {
			level = bumpSeverity(level, detector.SeverityWarning)
		}
	}

	// HLL
	if hll := m.result.Snapshot.InnoDBStatus.HistoryListLength; hll > overviewPageHLL {
		level = bumpSeverity(level, detector.SeverityCritical)
	} else if hll > overviewWarnHLL {
		level = bumpSeverity(level, detector.SeverityWarning)
	}

	// Replica lag
	if hv != nil && hv.Replica != nil && hv.Replica.SecondsBehindSource > 0 {
		if hv.Replica.SecondsBehindSource > 30 {
			level = bumpSeverity(level, detector.SeverityCritical)
		} else if hv.Replica.SecondsBehindSource > 5 {
			level = bumpSeverity(level, detector.SeverityWarning)
		}
	}

	// Aborted clients delta — single-window spike is a warn signal.
	if hv != nil && hv.AbortedClientsDelta > 0 {
		level = bumpSeverity(level, detector.SeverityWarning)
	}

	// Detector severity
	for _, iss := range m.result.Issues {
		level = bumpSeverity(level, iss.Severity)
	}

	switch level {
	case detector.SeverityCritical:
		return verdict{level, "PAGE", criticalStyle}
	case detector.SeverityWarning:
		return verdict{level, "WARN", warningStyle}
	default:
		return verdict{level, "HEALTHY", okStyle()}
	}
}

func bumpSeverity(cur, candidate detector.Severity) detector.Severity {
	if candidate > cur {
		return candidate
	}
	return cur
}

// okStyle is a green variant of headerStyle for the [HEALTHY] badge.
// We keep it local because the rest of the TUI never paints things
// green — only Overview's verdict word does.
func okStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("82"))
}

// latestHealth returns the most recent health snapshot or nil if
// either insights is disabled or no poll has completed yet.
func latestHealth(m Model) *db.HealthVitals {
	if m.insights == nil || m.insights.Health == nil {
		return nil
	}
	hv := m.insights.Health.Latest()
	if hv.Time.IsZero() {
		return nil
	}
	return &hv
}

func bufferPoolHitRatio(hv db.HealthVitals) float64 {
	req := hv.InnoDBBufferPoolReadRequests
	if req == 0 {
		return 0
	}
	hits := req
	if hv.InnoDBBufferPoolReads <= req {
		hits = req - hv.InnoDBBufferPoolReads
	}
	return float64(hits) / float64(req)
}

func boolStr(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func loadGroupingTitle(k insights.GroupKey) string {
	switch k {
	case insights.GroupKeyHost:
		return "Load by HOST (5m)"
	case insights.GroupKeySchema:
		return "Load by SCHEMA (5m)"
	default:
		return "Load by USER (5m)"
	}
}

// renderHBar produces a horizontal bar of length width. The filled
// portion uses ▰ and the rest ░ so the bars remain readable on
// terminals without truecolor.
func renderHBar(value, peak float64, width int) string {
	if width <= 0 {
		return ""
	}
	if peak <= 0 {
		return strings.Repeat("░", width)
	}
	filled := int(value/peak*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("▰", filled) + strings.Repeat("░", width-filled)
}

// formatBigCount formats large integer counters (HLL, aborted_clients
// delta) with a k/M/B suffix so the status line stays compact.
func formatBigCount(n uint64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func countDeadlocks(issues []detector.Issue) int {
	n := 0
	for _, i := range issues {
		if i.Detector == "deadlock" {
			n++
		}
	}
	return n
}

// padPanel makes every panel have the same number of lines as the
// tallest one so the joinHorizontal output stays rectangular. It also
// right-pads each line to the panel width so dividers and gaps line up.
func padPanel(s string, width int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		w := lipgloss.Width(ln)
		if w < width {
			ln += strings.Repeat(" ", width-w)
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// joinHorizontal concatenates panels side by side, padding the shorter
// ones with blank lines.
func joinHorizontal(panels []string, gap int) string {
	if len(panels) == 0 {
		return ""
	}
	rows := make([][]string, len(panels))
	maxLines := 0
	for i, p := range panels {
		rows[i] = strings.Split(p, "\n")
		if len(rows[i]) > maxLines {
			maxLines = len(rows[i])
		}
	}
	gapStr := strings.Repeat(" ", gap)
	var b strings.Builder
	for line := 0; line < maxLines; line++ {
		for i, lines := range rows {
			if line < len(lines) {
				b.WriteString(lines[line])
			}
			if i < len(rows)-1 {
				b.WriteString(gapStr)
			}
		}
		if line < maxLines-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
