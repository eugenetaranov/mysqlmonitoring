package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
)

// issueRow is the table-friendly view of one detector.Issue. Fields
// are sourced from Issue.Details when populated by the detector;
// otherwise they fall back to defaults that render as "-".
type issueRow struct {
	severity detector.Severity
	age      time.Duration
	ageStr   string
	pid      uint64
	user     string
	host     string
	kind     string
	table    string
	query    string
	raw      detector.Issue
}

// killable reports whether this row carries a PID we can kill.
func (r issueRow) killable() bool { return r.pid != 0 }

// buildIssueRows transforms detector results plus the live snapshot
// into a sortable table. Severity is preserved verbatim; age is
// derived from each detector's known fields.
func buildIssueRows(issues []detector.Issue, snap db.Snapshot) []issueRow {
	procByPID := make(map[uint64]db.Process, len(snap.Processes))
	for _, p := range snap.Processes {
		procByPID[p.ID] = p
	}
	trxByPID := make(map[uint64]db.Transaction, len(snap.Transactions))
	for _, t := range snap.Transactions {
		trxByPID[t.ID] = t
	}

	out := make([]issueRow, 0, len(issues))
	for _, iss := range issues {
		// Deadlocks emit one logical issue carrying N participants.
		// Fan them out so the table has one selectable row per
		// killable connection.
		if iss.Detector == "deadlock" {
			out = append(out, expandDeadlock(iss, snap, procByPID, trxByPID)...)
			continue
		}

		row := issueRow{
			severity: iss.Severity,
			kind:     friendlyKind(iss.Detector),
			raw:      iss,
		}
		populateFromDetails(&row, iss.Details, procByPID, trxByPID)
		out = append(out, row)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].severity != out[j].severity {
			return out[i].severity > out[j].severity
		}
		return out[i].age > out[j].age
	})
	return out
}

// expandDeadlock emits one issueRow per participant in a deadlock
// issue. Each row reuses the same raw Issue so the detail modal can
// still show the global context (timestamp, full participants list).
func expandDeadlock(iss detector.Issue, snap db.Snapshot, procs map[uint64]db.Process, trxs map[uint64]db.Transaction) []issueRow {
	count := 0
	if v, ok := iss.Details["participants"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			count = n
		}
	}
	if count == 0 {
		// Fall back to single placeholder so we don't drop the issue.
		row := issueRow{severity: iss.Severity, kind: friendlyKind(iss.Detector), raw: iss}
		return []issueRow{row}
	}

	dlAge := deadlockAge(iss, snap.Time)

	out := make([]issueRow, 0, count)
	for i := 1; i <= count; i++ {
		prefix := fmt.Sprintf("trx%d_", i)
		row := issueRow{
			severity: iss.Severity,
			kind:     friendlyKind(iss.Detector),
			raw:      iss,
			user:     iss.Details[prefix+"user"],
			host:     iss.Details[prefix+"host"],
			query:    iss.Details[prefix+"query"],
			table:    iss.Details[prefix+"table"],
		}
		if v := iss.Details[prefix+"thread_id"]; v != "" {
			if pid, err := strconv.ParseUint(v, 10, 64); err == nil {
				row.pid = pid
			}
		}

		// Prefer the live transaction's age over the deadlock timestamp:
		// the live trx is what we'll kill, and its TrxStarted is precise.
		if row.pid != 0 {
			if trx, ok := trxs[row.pid]; ok && !trx.TrxStarted.IsZero() {
				row.age = snap.Time.Sub(trx.TrxStarted)
				row.ageStr = humanDuration(row.age.Milliseconds())
			}
			// Backfill missing user/host/query from snapshot.
			if proc, ok := procs[row.pid]; ok {
				if row.user == "" {
					row.user = proc.User
				}
				if row.host == "" {
					row.host = proc.Host
				}
				if row.query == "" {
					row.query = proc.Info
				}
			}
		}
		if row.age == 0 && dlAge > 0 {
			row.age = dlAge
			row.ageStr = humanDuration(dlAge.Milliseconds())
		}
		if row.table == "" {
			row.table = extractTableFromSQL(row.query)
		}
		out = append(out, row)
	}
	return out
}

// deadlockAge returns the wall-clock age of a deadlock issue based
// on its parsed timestamp ("2006-01-02 15:04:05"). Returns zero on
// any parse error. The timestamp comes from `SHOW ENGINE INNODB
// STATUS` and is expressed in the server's local time; we parse it
// in the same location as `now` so subtraction is timezone-correct.
func deadlockAge(iss detector.Issue, now time.Time) time.Duration {
	ts := iss.Details["timestamp"]
	if ts == "" {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, now.Location())
	if err != nil {
		return 0
	}
	if t.After(now) {
		return 0
	}
	return now.Sub(t)
}

func friendlyKind(name string) string {
	switch name {
	case "long_transaction":
		return "long-trx"
	case "lock_chain":
		return "lock-chain"
	case "ddl_conflict":
		return "ddl"
	case "deadlock":
		return "deadlock"
	default:
		return name
	}
}

func populateFromDetails(row *issueRow, details map[string]string, procs map[uint64]db.Process, trxs map[uint64]db.Transaction) {
	if details == nil {
		// Even without details we can still try to extract the
		// table from the query the caller may set later.
		row.table = extractTableFromSQL(row.query)
		return
	}

	for _, key := range []string{"thread_id", "ddl_pid", "blocked_pid", "root_blocker"} {
		if v, ok := details[key]; ok && v != "" {
			if pid, err := strconv.ParseUint(v, 10, 64); err == nil && pid != 0 {
				row.pid = pid
				break
			}
		}
	}
	row.user = details["user"]
	row.host = details["host"]
	if v, ok := details["query"]; ok {
		row.query = v
	} else if v, ok := details["root_query"]; ok {
		row.query = v
	}

	// Prefer detector-known tables, fall back to SQL parsing. Lock
	// chains and DDL conflicts know their table directly; long
	// transactions and freeform issues only carry the query text.
	if v := details["table"]; v != "" {
		row.table = v
	} else if v := details["lock_table"]; v != "" {
		row.table = v
	}

	if v, ok := details["duration"]; ok && v != "" {
		row.ageStr = v
		row.age = parseDuration(v)
	} else if v, ok := details["wait_seconds"]; ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			row.age = time.Duration(n) * time.Second
			row.ageStr = humanDuration(n * 1000)
		}
	}

	// Fill missing user/host/query from snapshot when available.
	if row.pid != 0 {
		if row.query == "" {
			if proc, ok := procs[row.pid]; ok && proc.Info != "" {
				row.query = proc.Info
			}
		}
		if (row.user == "" || row.host == "") {
			if proc, ok := procs[row.pid]; ok {
				if row.user == "" {
					row.user = proc.User
				}
				if row.host == "" {
					row.host = proc.Host
				}
			}
		}
		if row.age == 0 {
			if trx, ok := trxs[row.pid]; ok && !trx.TrxStarted.IsZero() {
				row.age = time.Since(trx.TrxStarted)
				row.ageStr = humanDuration(row.age.Milliseconds())
			}
		}
	}

	// Final fallback for table: regex over the (possibly snapshot-
	// backfilled) query text.
	if row.table == "" {
		row.table = extractTableFromSQL(row.query)
	}
}

// parseDuration parses formatDuration's output ("19h14m", "44m53s",
// "13s") back into a time.Duration. Returns zero on failure.
func parseDuration(s string) time.Duration {
	var (
		total time.Duration
		num   int64
		err   error
	)
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			return 0
		}
		num, err = strconv.ParseInt(s[i:j], 10, 64)
		if err != nil {
			return 0
		}
		if j >= len(s) {
			return 0
		}
		switch s[j] {
		case 'h':
			total += time.Duration(num) * time.Hour
		case 'm':
			total += time.Duration(num) * time.Minute
		case 's':
			total += time.Duration(num) * time.Second
		default:
			return 0
		}
		i = j + 1
	}
	return total
}

// columnWidths returns severity / age / pid / user@host / kind
// widths, leaving the remainder for the query column. On narrow
// terminals (< 120 cols) the user@host column is hidden by setting
// its width to 0.
type colWidths struct {
	sev, age, pid, userHost, kind, query int
}

const (
	wSev  = 5
	wAge  = 8
	wPID  = 13 // up to 12-digit thread ids
	wKind = 11
)

func computeColWidths(width int, hasUserHost bool) colWidths {
	w := colWidths{sev: wSev, age: wAge, pid: wPID, kind: wKind}
	gap := 2 // padding between columns
	used := w.sev + w.age + w.pid + w.kind + 4*gap
	if hasUserHost && width >= 120 {
		w.userHost = 32
		used += w.userHost + gap
	}
	w.query = width - used - 2 // 2-space leading indent
	if w.query < 20 {
		w.query = 20
	}
	return w
}

func renderIssuesTable(m Model) string {
	rows := visibleIssueRows(m.result.Issues, m.result.Snapshot, m.issuesTableFilter)
	if len(rows) == 0 {
		var hint string
		if m.issuesTableFilter != "" {
			hint = fmt.Sprintf("No issues matching table=%s. Press '/' to clear filter.", m.issuesTableFilter)
		} else {
			hint = "No issues detected."
		}
		return "  " + infoStyle.Render(hint) + "\n"
	}

	cw := computeColWidths(m.width, true)

	var b strings.Builder
	headerLine := fmt.Sprintf("Issues (%d)", len(rows))
	if m.issuesTableFilter != "" {
		headerLine += "  " + dimStyle.Render(
			fmt.Sprintf("filter: table=%s  (press / to clear)", m.issuesTableFilter))
	}
	b.WriteString(headerStyle.Render(headerLine))
	b.WriteString("\n\n")

	header := formatIssueHeader(cw)
	b.WriteString(dimStyle.Render(header))
	b.WriteString("\n")

	maxRows := m.height - 8
	if maxRows < 5 {
		maxRows = len(rows)
	}
	start := 0
	if m.issuesCursor >= maxRows {
		start = m.issuesCursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(rows) {
		end = len(rows)
	}

	for i := start; i < end; i++ {
		selected := i == m.issuesCursor
		// On selected rows we render the severity as plain text and
		// then paint the entire line in selectedStyle. Embedding a
		// pre-coloured badge inside selectedStyle would emit a
		// `reset` escape mid-row that wipes the highlight background.
		line := formatIssueRow(rows[i], cw, selected)
		if selected {
			b.WriteString(selectedStyle.Width(m.width).Render("> " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}

	if start > 0 || end < len(rows) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  rows %d–%d of %d", start+1, end, len(rows))))
		b.WriteString("\n")
	}
	return b.String()
}

func formatIssueHeader(cw colWidths) string {
	parts := []string{
		padRight("SEV", cw.sev),
		padRight("AGE", cw.age),
		padRight("PID", cw.pid),
	}
	if cw.userHost > 0 {
		parts = append(parts, padRight("USER@HOST", cw.userHost))
	}
	parts = append(parts, padRight("KIND", cw.kind), "QUERY")
	return strings.Join(parts, "  ")
}

func formatIssueRow(r issueRow, cw colWidths, selected bool) string {
	pidStr := "-"
	if r.pid != 0 {
		pidStr = strconv.FormatUint(r.pid, 10)
	}
	parts := []string{
		padRight(severityShort(r.severity, selected), cw.sev),
		padRight(orDashStr(r.ageStr), cw.age),
		padRight(pidStr, cw.pid),
	}
	if cw.userHost > 0 {
		uh := "-"
		if r.user != "" || r.host != "" {
			uh = fmt.Sprintf("%s@%s", r.user, stripPort(r.host))
		}
		parts = append(parts, padRight(truncateStr(uh, cw.userHost), cw.userHost))
	}
	parts = append(parts, padRight(r.kind, cw.kind))
	q := r.query
	if q == "" {
		q = r.raw.Title
	}
	parts = append(parts, truncateStr(collapseWhitespace(q), cw.query))
	return strings.Join(parts, "  ")
}

// severityShort returns the per-severity short label. When selected
// is true the label is plain text so the surrounding selectedStyle
// background paints uniformly across the row; coloured severity
// badges include their own ANSI reset which would punch a hole in
// the highlight.
func severityShort(s detector.Severity, selected bool) string {
	switch s {
	case detector.SeverityCritical:
		if selected {
			return "CRIT"
		}
		return criticalStyle.Render("CRIT")
	case detector.SeverityWarning:
		if selected {
			return "WARN"
		}
		return warningStyle.Render("WARN")
	default:
		if selected {
			return "INFO"
		}
		return infoStyle.Render("INFO")
	}
}

func padRight(s string, n int) string {
	w := visibleWidth(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// visibleWidth approximates the on-screen rune width of s, ignoring
// ANSI escape sequences emitted by lipgloss. Good enough for column
// alignment of severity badges.
func visibleWidth(s string) int {
	in := false
	w := 0
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case !in:
			w++
		}
	}
	return w
}

func collapseWhitespace(s string) string {
	out := make([]byte, 0, len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\r' || c == '\t' {
			c = ' '
		}
		if c == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, c)
	}
	return string(out)
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renderIssueDetail draws the modal view triggered by Enter / 'v' on
// the Issues table. It shows the full query text wrapped to terminal
// width plus the surrounding metadata for context.
func renderIssueDetail(m Model) string {
	rows := visibleIssueRows(m.result.Issues, m.result.Snapshot, m.issuesTableFilter)
	if len(rows) == 0 || m.issuesCursor >= len(rows) {
		return "  " + dimStyle.Render("(no issue selected)\n")
	}
	r := rows[m.issuesCursor]

	var b strings.Builder
	b.WriteString(headerStyle.Render("Issue detail"))
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("  %s  %s\n", severityShort(r.severity, false), r.kind))
	b.WriteString(fmt.Sprintf("  %s\n", r.raw.Title))
	if r.raw.Description != "" {
		b.WriteString("  ")
		b.WriteString(dimStyle.Render(r.raw.Description))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if r.pid != 0 {
		b.WriteString(fmt.Sprintf("  PID:    %d\n", r.pid))
	}
	if r.user != "" || r.host != "" {
		b.WriteString(fmt.Sprintf("  Conn:   %s@%s\n", r.user, stripPort(r.host)))
	}
	if r.ageStr != "" {
		b.WriteString(fmt.Sprintf("  Age:    %s\n", r.ageStr))
	}
	b.WriteString("\n")

	b.WriteString(headerStyle.Render("  Query:"))
	b.WriteString("\n")
	wrapWidth := m.width - 4
	if wrapWidth < 40 {
		wrapWidth = 40
	}
	for _, line := range wrapText(r.query, wrapWidth) {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}

	if len(r.raw.Details) > 0 {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("  Details:"))
		b.WriteString("\n")
		// Stable iteration order so the modal doesn't shimmer.
		keys := make([]string, 0, len(r.raw.Details))
		for k := range r.raw.Details {
			if k == "query" || k == "user" || k == "host" || k == "thread_id" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("    %-16s %s\n", k+":", truncateStr(r.raw.Details[k], wrapWidth-20)))
		}
	}
	return b.String()
}

// wrapText breaks s on whitespace boundaries to fit within width
// columns. Leading/trailing whitespace is trimmed and runs of
// whitespace collapse to single spaces.
func wrapText(s string, width int) []string {
	if width <= 0 {
		width = 80
	}
	s = collapseWhitespace(strings.TrimSpace(s))
	if s == "" {
		return []string{""}
	}
	var out []string
	for len(s) > width {
		// Look for a space near the right edge to break on.
		cut := strings.LastIndexByte(s[:width], ' ')
		if cut <= 0 {
			cut = width
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// yankToClipboard writes payload to the terminal as an OSC 52
// clipboard sequence and returns a status string suitable for the
// status bar. We cannot detect whether the terminal honoured the
// sequence; the caller's status bar tells the user what we attempted.
func yankToClipboard(payload string) string {
	if payload == "" {
		return "nothing to copy"
	}
	enc := base64.StdEncoding.EncodeToString([]byte(payload))
	// "c" selects the system clipboard. Some terminals only allow
	// pre-configured selections — those will silently ignore us.
	_, err := fmt.Fprintf(os.Stderr, "\x1b]52;c;%s\x07", enc)
	if err != nil {
		return "clipboard unavailable"
	}
	return fmt.Sprintf("copied %d chars to clipboard", len(payload))
}
