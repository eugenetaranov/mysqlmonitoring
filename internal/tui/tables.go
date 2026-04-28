package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
)

// Bucket labels for issues whose query yields no extractable table.
// We categorise instead of lumping everything under "(no table)" so
// the operator can see *why* a row is unattributable — admin SQL
// has no FROM by design, while a truncated digest is a perf_schema
// limit the operator can fix by raising performance_schema_max_digest_length.
const (
	bucketIdle           = "(idle: no SQL captured)"
	bucketSessionAdmin   = "(session admin: SET / SELECT @@ / SHOW)"
	bucketTxnControl     = "(transaction control: BEGIN / COMMIT)"
	bucketDigestTrunc    = "(digest truncated by perf_schema)"
	bucketNoTableRef     = "(no table reference in SQL)"

	// noTableLabel is kept for backward compatibility with tests
	// that grep for it; new code should prefer the categorised
	// buckets above. It maps to bucketNoTableRef in practice.
	noTableLabel = bucketNoTableRef
)

// bucketForUntableable classifies a query that produced no table
// match. The classification is purely textual — it inspects the
// leading verb after stripping SQL comments and a few telltales.
func bucketForUntableable(query string) string {
	if strings.TrimSpace(query) == "" {
		return bucketIdle
	}
	s := stripLeadingSQLComments(query)
	if s == "" {
		return bucketIdle
	}
	trimmed := strings.TrimSpace(s)
	upper := strings.ToUpper(trimmed)

	switch {
	case strings.HasPrefix(upper, "BEGIN"),
		strings.HasPrefix(upper, "COMMIT"),
		strings.HasPrefix(upper, "ROLLBACK"),
		strings.HasPrefix(upper, "SAVEPOINT"),
		strings.HasPrefix(upper, "RELEASE SAVEPOINT"),
		strings.HasPrefix(upper, "START TRANSACTION"):
		return bucketTxnControl
	case strings.HasPrefix(upper, "SET "),
		strings.HasPrefix(upper, "SET\t"),
		strings.HasPrefix(upper, "SHOW "),
		strings.HasPrefix(upper, "USE "),
		strings.HasPrefix(upper, "SELECT @@"):
		return bucketSessionAdmin
	}

	// performance_schema digest truncation appends "..." to the
	// digest text once it hits performance_schema_max_digest_length.
	// A query that ends in literal "..." with no FROM/UPDATE/etc
	// keyword almost always means we ran out of bytes before the
	// table reference.
	if strings.HasSuffix(trimmed, "...") {
		return bucketDigestTrunc
	}
	return bucketNoTableRef
}

// tableRow aggregates every issueRow targeting one schema.table.
type tableRow struct {
	name       string
	issueCount int
	bySeverity [3]int // [info, warning, critical]
	oldestAge  time.Duration
	oldestStr  string
	maxSev     detector.Severity
	pids       []uint64
	rows       []issueRow
}

// buildTableRows aggregates issues by their resolved table. Severity
// counts use the indices of detector.SeverityInfo / Warning /
// Critical so the rendering can index directly without a switch.
func buildTableRows(issues []detector.Issue, snap db.Snapshot) []tableRow {
	rows := buildIssueRows(issues, snap)

	bucket := make(map[string]*tableRow)
	for _, r := range rows {
		name := r.table
		if name == "" {
			name = bucketForUntableable(r.query)
		}
		t, ok := bucket[name]
		if !ok {
			t = &tableRow{name: name}
			bucket[name] = t
		}
		t.issueCount++
		if int(r.severity) >= 0 && int(r.severity) < len(t.bySeverity) {
			t.bySeverity[int(r.severity)]++
		}
		if r.severity > t.maxSev {
			t.maxSev = r.severity
		}
		if r.age > t.oldestAge {
			t.oldestAge = r.age
			t.oldestStr = r.ageStr
		}
		if r.killable() && !containsUint64(t.pids, r.pid) {
			t.pids = append(t.pids, r.pid)
		}
		t.rows = append(t.rows, r)
	}

	out := make([]tableRow, 0, len(bucket))
	for _, t := range bucket {
		out = append(out, *t)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].maxSev != out[j].maxSev {
			return out[i].maxSev > out[j].maxSev
		}
		if out[i].issueCount != out[j].issueCount {
			return out[i].issueCount > out[j].issueCount
		}
		return out[i].oldestAge > out[j].oldestAge
	})

	return out
}

func containsUint64(s []uint64, v uint64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// allPIDs returns the union of every killable PID across the
// supplied tableRows. Used by the bulk-kill confirmation modal.
func (t tableRow) killPIDs() []uint64 {
	out := make([]uint64, len(t.pids))
	copy(out, t.pids)
	return out
}

// renderTablesView renders the Tables panel with a header row, one
// row per aggregated table, and a hint footer when the cursor is
// off-screen due to a long list.
func renderTablesView(m Model) string {
	rows := buildTableRows(m.result.Issues, m.result.Snapshot)
	if len(rows) == 0 {
		return "  " + infoStyle.Render("No issues to group by table.") + "\n"
	}

	cw := computeTablesColWidths(m.width)

	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf("Tables (%d)", len(rows))))
	b.WriteString("\n\n")

	b.WriteString(dimStyle.Render(formatTablesHeader(cw)))
	b.WriteString("\n")

	maxRows := m.height - 8
	if maxRows < 5 {
		maxRows = len(rows)
	}
	start := 0
	if m.tablesCursor >= maxRows {
		start = m.tablesCursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(rows) {
		end = len(rows)
	}

	for i := start; i < end; i++ {
		selected := i == m.tablesCursor
		line := formatTableRow(rows[i], cw, selected)
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

	if m.confirmKillBatch != nil && m.confirmKillTarget != "" {
		b.WriteString("\n")
		b.WriteString(criticalStyle.Render(fmt.Sprintf(
			"  Kill %d connection(s) on %s? [y/N] ",
			len(m.confirmKillBatch), m.confirmKillTarget)))
	}

	return b.String()
}

// tableColWidths is the Tables view equivalent of colWidths in
// issues.go — independent layout so the columns we surface here can
// evolve separately from the issues table.
type tableColWidths struct {
	sev, count, breakdown, oldest, pids, table int
}

const (
	wTablesSev       = 5
	wTablesCount     = 7
	wTablesBreakdown = 14
	wTablesOldest    = 8
	wTablesPIDs      = 5
)

func computeTablesColWidths(width int) tableColWidths {
	w := tableColWidths{
		sev:       wTablesSev,
		count:     wTablesCount,
		breakdown: wTablesBreakdown,
		oldest:    wTablesOldest,
		pids:      wTablesPIDs,
	}
	gap := 2
	used := w.sev + w.count + w.breakdown + w.oldest + w.pids + 5*gap
	w.table = width - used - 2 // leading "  " indent
	if w.table < 24 {
		w.table = 24
	}
	return w
}

func formatTablesHeader(cw tableColWidths) string {
	return strings.Join([]string{
		padRight("SEV", cw.sev),
		padRight("ISSUES", cw.count),
		padRight("CRIT/WARN/INFO", cw.breakdown),
		padRight("OLDEST", cw.oldest),
		padRight("PIDS", cw.pids),
		"TABLE",
	}, "  ")
}

func formatTableRow(t tableRow, cw tableColWidths, selected bool) string {
	breakdown := fmt.Sprintf("%d/%d/%d",
		t.bySeverity[detector.SeverityCritical],
		t.bySeverity[detector.SeverityWarning],
		t.bySeverity[detector.SeverityInfo])
	parts := []string{
		padRight(severityShort(t.maxSev, selected), cw.sev),
		padRight(strconv.Itoa(t.issueCount), cw.count),
		padRight(breakdown, cw.breakdown),
		padRight(orDashStr(t.oldestStr), cw.oldest),
		padRight(strconv.Itoa(len(t.pids)), cw.pids),
		truncateStr(t.name, cw.table),
	}
	return strings.Join(parts, "  ")
}
