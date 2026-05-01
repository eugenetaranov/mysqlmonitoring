package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/eugenetaranov/mysqlmonitoring/internal/collector"
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
//
// Field order is "host first, MySQL second": when CloudWatch is wired,
// the leftmost gauge cluster after the verdict word is CPU% / Mem /
// IOPS, so the operator sees host-level signal before MySQL-level
// signal. Snapshot time is no longer rendered here — it lives in the
// chrome's right-aligned context block (Phase 1).
func renderOverviewVerdictLine(m Model) string {
	v := computeVerdict(m)
	parts := []string{v.style.Render("[" + v.word + "]")}
	snap := m.result.Snapshot

	// CloudWatch host-level gauges first — they're the new signal in
	// this redesign and the eye lands here before MySQL-side gauges.
	if cw := latestCloudWatch(m); cw != nil {
		parts = append(parts, cwVerdictParts(cw)...)
	}

	// Threads_running and buffer-pool stats from the health collector.
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

	return wrapVerdictParts(parts, widthOr120(m.width))
}

// cwVerdictParts renders the CloudWatch gauge cluster for the verdict
// line. Each gauge self-colours by its own threshold so the operator
// sees red on the bad number even when the verdict word stays HEALTHY.
func cwVerdictParts(cw *collector.CWMetrics) []string {
	var out []string

	cpu := fmt.Sprintf("CPU %.0f%%", cw.CPUPct)
	switch {
	case cw.CPUPct > 95:
		cpu = criticalStyle.Render(cpu)
	case cw.CPUPct > 80:
		cpu = warningStyle.Render(cpu)
	}
	out = append(out, cpu)

	if cw.FreeableBytes > 0 {
		out = append(out, fmt.Sprintf("Mem %s free", formatBytes(cw.FreeableBytes)))
	}

	if cw.ReadIOPS > 0 || cw.WriteIOPS > 0 {
		out = append(out, fmt.Sprintf("IOPS %s/%s",
			formatRate(cw.ReadIOPS), formatRate(cw.WriteIOPS)))
	}

	if cw.DBLoad > 0 {
		out = append(out, fmt.Sprintf("DBLoad %.1f", cw.DBLoad))
	}

	return out
}

// wrapVerdictParts joins parts with a two-space separator, breaking
// onto a new line (with a two-space indent so it visually nests under
// the previous line) whenever appending would exceed width. Uses
// lipgloss.Width so ANSI colour codes don't fool the math.
func wrapVerdictParts(parts []string, width int) string {
	if len(parts) == 0 {
		return ""
	}
	const indent = " "
	var b strings.Builder
	b.WriteString(indent)
	used := lipgloss.Width(indent)
	for i, p := range parts {
		pw := lipgloss.Width(p)
		if i == 0 {
			b.WriteString(p)
			used += pw
			continue
		}
		// Two-space gap between gauges. Break to next line if adding
		// this part overflows the terminal.
		if used+2+pw > width {
			b.WriteString("\n")
			b.WriteString("  ")
			used = 2
		} else {
			b.WriteString("  ")
			used += 2
		}
		b.WriteString(p)
		used += pw
	}
	return b.String()
}

// latestCloudWatch returns the most recent CW snapshot, or nil if no
// sample has landed yet (or CloudWatch is unwired).
func latestCloudWatch(m Model) *collector.CWMetrics {
	if m.insights == nil || m.insights.CloudWatch == nil {
		return nil
	}
	cw := m.insights.CloudWatch.Latest()
	if cw.Time.IsZero() {
		return nil
	}
	return &cw
}

// formatBytes renders a byte count as a short human string (KB, MB,
// GB). Used by the verdict line's free-memory gauge.
func formatBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// formatRate renders an IOPS rate compactly (e.g. 1234 → "1.2k").
func formatRate(r float64) string {
	switch {
	case r >= 1000:
		return fmt.Sprintf("%.1fk", r/1000)
	case r >= 100:
		return fmt.Sprintf("%.0f", r)
	default:
		return fmt.Sprintf("%.0f", r)
	}
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

// overviewWindow is the rolling window for the three top-N panels on
// the Overview. 60 seconds keeps the panels reactive during incidents
// — spikes that started a minute ago aren't averaged against an hour
// of quiet baseline. Operators wanting longer windows use the
// dedicated Top SQL tab (`t`).
const overviewWindow = 60 * time.Second

// renderOverviewMiddleBand draws three side-by-side top-N panels with
// a 60s window: Top AAS queries, Top AAS users, Top busiest tables.
// Equal widths; no conditional collapse — these three are always
// available from the existing perf-insights collectors.
func renderOverviewMiddleBand(m Model) string {
	w := widthOr120(m.width)
	gap := 2
	col := (w - 2*gap - 2) / 3
	colW := []int{col, col, w - 2 - 2*gap - col - col}

	cols := []string{
		padPanel(renderTopAASQueries(m, colW[0]), colW[0]),
		padPanel(renderTopAASUsers(m, colW[1]), colW[1]),
		padPanel(renderTopBusiestTables(m, colW[2]), colW[2]),
	}
	return joinHorizontal(cols, gap) + "\n"
}

// renderOverviewBottomBand draws Long Transactions + Replication.
// Replication panel is removed entirely on standalone servers and the
// Long Transactions panel widens to full width.
func renderOverviewBottomBand(m Model) string {
	hv := latestHealth(m)
	hasReplica := hv != nil && hv.Replica != nil

	w := widthOr120(m.width)
	gap := 2
	if !hasReplica {
		body := renderLongTransactions(m, w-2)
		return padPanel(body, w-2) + "\n"
	}

	col := (w - gap - 2) / 2
	leftW, rightW := col, w-2-gap-col
	cols := []string{
		padPanel(renderLongTransactions(m, leftW), leftW),
		padPanel(renderReplicationPanel(hv.Replica, rightW), rightW),
	}
	return joinHorizontal(cols, gap) + "\n"
}

// renderTopAASQueries shows the top digests by AAS over the Overview
// window (60s by default). Compact: AAS + truncated digest text +
// no_idx flag when the digest scanned without an index.
func renderTopAASQueries(m Model, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Top AAS queries (60s)"))
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
			Window: overviewWindow,
			Limit:  5,
			Sort:   insights.SortByAAS,
		})
	if len(digests) == 0 {
		b.WriteString(dimStyle.Render("  gathering samples…"))
		return b.String()
	}
	for _, d := range digests {
		flag := ""
		if d.NoIndexUsedCalls > 0 {
			flag = warningStyle.Render(" no_idx")
		}
		room := width - 14
		if room < 12 {
			room = 12
		}
		b.WriteString(fmt.Sprintf("  AAS %5.2f  %s%s\n",
			d.AAS, truncateStr(d.Text, room), flag))
	}
	return b.String()
}

// renderTopAASUsers attributes load to MySQL users via LoadByGroup
// over the 60s window. Renders horizontal bars proportional to the
// highest AAS in the panel. The cursor on this panel drives the `u`
// drill into Top SQL.
func renderTopAASUsers(m Model, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Top AAS users (60s)"))
	b.WriteString("\n")

	if m.insights == nil || m.insights.Sessions == nil {
		b.WriteString(dimStyle.Render("  load attribution unavailable"))
		return b.String()
	}
	if caps := m.insights.Capabilities(); !caps.WaitsAvailable {
		b.WriteString(dimStyle.Render("  performance_schema waits disabled"))
		return b.String()
	}
	rows := insights.LoadByGroup(m.insights.Sessions, time.Now(), overviewWindow, insights.GroupKeyUser)
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  gathering samples…"))
		return b.String()
	}
	const maxRows = 5
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	nameW := 14
	numW := 6
	barW := width - nameW - numW - 5
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

// renderTopBusiestTables aggregates digest activity over 60s by
// extracting table names from each digest's SQL text. Activity-based
// — distinct from the Tables tab which groups detector issues. Shows
// the top-5 tables by total AAS over the window with calls/sec
// alongside.
func renderTopBusiestTables(m Model, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Top busiest tables (60s)"))
	b.WriteString("\n")

	if m.insights == nil {
		b.WriteString(dimStyle.Render("  perf-insights disabled"))
		return b.String()
	}
	if caps := m.insights.Capabilities(); !caps.DigestAvailable {
		b.WriteString(dimStyle.Render("  performance_schema digest disabled"))
		return b.String()
	}

	rows := topBusiestTables(m.insights, time.Now(), overviewWindow, 5)
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  gathering samples…"))
		return b.String()
	}
	nameW := width - 22
	if nameW < 12 {
		nameW = 12
	}
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-*s  %5.2f AAS  %5s qps\n",
			nameW, truncateStr(r.Name, nameW),
			r.AAS, formatRate(r.CallsPerSec)))
	}
	return b.String()
}

// tableActivity is one row in the top-busiest-tables aggregation.
type tableActivity struct {
	Name        string  // schema.table or just table when unqualified
	AAS         float64 // sum of digest AAS that reference this table
	CallsPerSec float64
}

// topBusiestTables walks the digest aggregator over the supplied
// window, extracts table names from each digest's SQL text, and sums
// per-table totals. Pure in-memory — reuses the existing TopSQL API
// rather than a new collection path.
func topBusiestTables(ins *insights.Insights, now time.Time, window time.Duration, limit int) []tableActivity {
	digests := insights.TopSQL(ins.Registry, ins.Sessions, now,
		insights.TopSQLOptions{
			Window: window,
			Limit:  500, // pull all then re-rank by table
			Sort:   insights.SortByAAS,
		})
	byTable := make(map[string]*tableActivity)
	for _, d := range digests {
		t := extractTableFromSQL(d.Text)
		if t == "" {
			continue
		}
		key := t
		if d.Schema != "" && !strings.Contains(t, ".") {
			key = d.Schema + "." + t
		}
		a, ok := byTable[key]
		if !ok {
			a = &tableActivity{Name: key}
			byTable[key] = a
		}
		a.AAS += d.AAS
		a.CallsPerSec += d.CallsPerSec
	}
	out := make([]tableActivity, 0, len(byTable))
	for _, a := range byTable {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AAS > out[j].AAS })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// renderLongTransactions surfaces the slowest open transactions to
// catch the silent-wedger pattern (idle in transaction holding the
// world). Filter ≥ 30s, sorted by Time desc, top-5.
func renderLongTransactions(m Model, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Long transactions (≥30s)"))
	b.WriteString("\n")

	all := m.result.Snapshot.Transactions
	rows := make([]db.Transaction, 0, len(all))
	for _, t := range all {
		if t.Time >= 30 {
			rows = append(rows, t)
		}
	}
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  none"))
		return b.String()
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time > rows[j].Time })
	if len(rows) > 5 {
		rows = rows[:5]
	}
	queryW := width - 38
	if queryW < 12 {
		queryW = 12
	}
	for _, t := range rows {
		age := humanDuration(t.Time * 1000)
		userHost := truncateStr(t.User, 10)
		query := strings.TrimSpace(t.Query)
		if query == "" {
			query = "(idle in trx)"
		} else {
			query = simplifyQuery(query)
		}
		b.WriteString(fmt.Sprintf("  %-7s pid %-6d %-10s %s\n",
			age, t.ID, userHost, truncateStr(query, queryW)))
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

	// CloudWatch CPU% — host-level signal we can't otherwise see.
	// Tier thresholds match the design D2 numbers.
	if cw := latestCloudWatch(m); cw != nil {
		switch {
		case cw.CPUPct > 95:
			level = bumpSeverity(level, detector.SeverityCritical)
		case cw.CPUPct > 80:
			level = bumpSeverity(level, detector.SeverityWarning)
		}
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
