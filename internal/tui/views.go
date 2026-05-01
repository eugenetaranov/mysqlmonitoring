package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
)

// reTableName matches the first table reference in a SQL statement.
// It handles three common shapes:
//   - bare:               FROM users
//   - backtick-quoted:    FROM `users`
//   - schema-qualified:   FROM `alice`.`hskp_message`  (or unquoted)
//
// The optional second capture group holds the table when the first
// is a schema qualifier; extractTableFromSQL stitches them.
var reTableName = regexp.MustCompile(
	"(?i)(?:FROM|INTO|UPDATE|JOIN|TABLE)\\s+" +
		"`?(\\w+)`?(?:\\s*\\.\\s*`?(\\w+)`?)?")

// extractTableFromSQL returns "schema.table" when the statement
// references a qualified table, "table" for an unqualified one, or
// "" when no table-shaped reference is found. Leading SQL comments
// are stripped first so sqlcommenter prefixes don't fool the regex.
func extractTableFromSQL(sql string) string {
	if sql == "" {
		return ""
	}
	sql = stripLeadingSQLComments(sql)
	if sql == "" {
		return ""
	}
	m := reTableName.FindStringSubmatch(sql)
	if len(m) == 0 {
		return ""
	}
	if len(m) > 2 && m[2] != "" {
		return m[1] + "." + m[2]
	}
	return m[1]
}

// stripLeadingSQLComments removes any number of leading SQL comments
// (block /* ... */, line --, hash #) plus whitespace so the verb
// extraction below sees the real statement. Returns an empty string
// if the input is nothing but comments.
func stripLeadingSQLComments(q string) string {
	for {
		s := strings.TrimLeft(q, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s, "*/")
			if end < 0 {
				return ""
			}
			q = s[end+2:]
		case strings.HasPrefix(s, "--"):
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return ""
			}
			q = s[nl+1:]
		case strings.HasPrefix(s, "#"):
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return ""
			}
			q = s[nl+1:]
		default:
			return s
		}
	}
}

// simplifyQuery condenses a SQL query into a short label like DB monitoring tools.
func simplifyQuery(q string) string {
	if q == "" {
		return ""
	}
	q = stripLeadingSQLComments(q)
	if q == "" {
		return ""
	}
	upper := strings.ToUpper(strings.TrimSpace(q))
	// For label use we want just the table — schema.table makes the
	// label too noisy for the lock-tree row format. Pick the
	// rightmost qualifier when present.
	tableName := ""
	if m := reTableName.FindStringSubmatch(q); len(m) > 1 {
		if len(m) > 2 && m[2] != "" {
			tableName = m[2]
		} else {
			tableName = m[1]
		}
	}

	switch {
	case strings.HasPrefix(upper, "SELECT") && strings.Contains(upper, "FOR UPDATE"):
		if tableName != "" {
			return "SELECT FOR UPDATE " + tableName
		}
		return "SELECT FOR UPDATE"
	case strings.HasPrefix(upper, "SELECT"):
		if tableName != "" {
			return "SELECT " + tableName
		}
		return "SELECT"
	case strings.HasPrefix(upper, "UPDATE"):
		if tableName != "" {
			return "UPDATE " + tableName
		}
		return "UPDATE"
	case strings.HasPrefix(upper, "INSERT"):
		if tableName != "" {
			return "INSERT " + tableName
		}
		return "INSERT"
	case strings.HasPrefix(upper, "DELETE"):
		if tableName != "" {
			return "DELETE " + tableName
		}
		return "DELETE"
	case strings.HasPrefix(upper, "ALTER"):
		if tableName != "" {
			return "ALTER TABLE " + tableName
		}
		return "ALTER TABLE"
	default:
		// First word + truncate
		parts := strings.Fields(q)
		if len(parts) == 0 {
			return ""
		}
		label := strings.ToUpper(parts[0])
		if len(label) > 30 {
			label = label[:27] + "..."
		}
		return label
	}
}

// queryLabel returns the best available display label for a query.
// It prefers the normalized DIGEST_TEXT from performance_schema,
// falling back to a comment-stripped simplifyQuery() for cases where
// digest is unavailable (perf_schema disabled, deadlocks from the
// InnoDB-status parser, rolled-back deadlock victims, etc.).
//
// When neither source yields anything, an empty string is returned so
// callers can render their own placeholder.
func queryLabel(digest, rawQuery string) string {
	digest = strings.TrimSpace(digest)
	if digest != "" {
		// Digest texts captured at the moment of deadlock can also
		// carry leading hint comments; strip them for consistency.
		if stripped := stripLeadingSQLComments(digest); stripped != "" {
			return stripped
		}
	}
	return simplifyQuery(rawQuery)
}

// treeEntry is a flattened entry in the lock tree for navigation.
type treeEntry struct {
	pid        uint64
	user       string
	host       string
	query      string
	digest     string
	waitMs     int64
	table      string // schema.table from LockWait.LockTable
	isBlocker  bool
	isDeadlock bool // deadlock participant
	isLast     bool // last waiter under a blocker (for └ vs ├)
}

// buildTreeEntries flattens lock waits into a deduplicated navigable tree.
// Each PID appears only once. Root blockers (not themselves waiting) are shown
// as blocker entries; all others are shown as waiters under their direct blocker.
// snapshot is used to enrich blocker entries with transaction duration and query.
func buildTreeEntries(lockWaits []db.LockWait, snapshot db.Snapshot) []treeEntry {
	type nodeInfo struct {
		pid    uint64
		user   string
		host   string
		query  string
		digest string
		table  string
	}

	// Collect blocker info and direct waiter relationships.
	// blockerOf[waiterPID] = blockerPID (direct parent)
	blockerOf := make(map[uint64]uint64)
	// children[blockerPID] = list of waiter PIDs (deduplicated, ordered)
	children := make(map[uint64][]uint64)
	childSet := make(map[uint64]map[uint64]bool)
	// info per PID (from first occurrence)
	info := make(map[uint64]nodeInfo)
	// waitMs per waiter PID (max across lock waits)
	waitMs := make(map[uint64]int64)
	// table/lockType per waiter PID
	waiterTable := make(map[uint64]string)

	for _, lw := range lockWaits {
		// Record blocker info
		if _, ok := info[lw.BlockingPID]; !ok {
			info[lw.BlockingPID] = nodeInfo{
				pid:    lw.BlockingPID,
				user:   lw.BlockingUser,
				host:   lw.BlockingHost,
				query:  lw.BlockingQuery,
				digest: lw.BlockingDigest,
				table:  lw.LockTable,
			}
		}
		// Record waiter info
		if _, ok := info[lw.WaitingPID]; !ok {
			info[lw.WaitingPID] = nodeInfo{
				pid:    lw.WaitingPID,
				user:   lw.WaitingUser,
				host:   lw.WaitingHost,
				query:  lw.WaitingQuery,
				digest: lw.WaitingDigest,
				table:  lw.LockTable,
			}
		}

		blockerOf[lw.WaitingPID] = lw.BlockingPID
		if childSet[lw.BlockingPID] == nil {
			childSet[lw.BlockingPID] = make(map[uint64]bool)
		}
		if !childSet[lw.BlockingPID][lw.WaitingPID] {
			childSet[lw.BlockingPID][lw.WaitingPID] = true
			children[lw.BlockingPID] = append(children[lw.BlockingPID], lw.WaitingPID)
		}

		if lw.WaitDurationMs > waitMs[lw.WaitingPID] {
			waitMs[lw.WaitingPID] = lw.WaitDurationMs
		}
		waiterTable[lw.WaitingPID] = lw.LockTable
	}

	// Build lookup maps from snapshot for enriching blocker entries.
	// Transaction duration by PID (thread ID).
	trxByPID := make(map[uint64]db.Transaction)
	for _, trx := range snapshot.Transactions {
		trxByPID[trx.ID] = trx
	}
	// Process info by PID for query fallback.
	procByPID := make(map[uint64]db.Process)
	for _, p := range snapshot.Processes {
		procByPID[p.ID] = p
	}

	// Find root blockers: PIDs that block others but are not themselves waiting.
	rootSet := make(map[uint64]bool)
	for _, lw := range lockWaits {
		rootSet[lw.BlockingPID] = true
	}
	for pid := range blockerOf {
		delete(rootSet, pid)
	}
	// Collect roots and compute their transaction duration for sorting.
	var roots []uint64
	seen := make(map[uint64]bool)
	rootDuration := make(map[uint64]int64)
	for _, lw := range lockWaits {
		if rootSet[lw.BlockingPID] && !seen[lw.BlockingPID] {
			roots = append(roots, lw.BlockingPID)
			seen[lw.BlockingPID] = true
			if trx, ok := trxByPID[lw.BlockingPID]; ok && !trx.TrxStarted.IsZero() {
				rootDuration[lw.BlockingPID] = snapshot.Time.Sub(trx.TrxStarted).Milliseconds()
			}
		}
	}
	// Sort roots by duration descending (oldest/longest first).
	sort.Slice(roots, func(i, j int) bool {
		return rootDuration[roots[i]] > rootDuration[roots[j]]
	})

	// DFS to build flat entries
	var entries []treeEntry
	visited := make(map[uint64]bool)

	walk := func(pid uint64, isRoot bool) {
		if visited[pid] {
			return
		}
		visited[pid] = true

		n := info[pid]
		if isRoot {
			blockerQuery := n.query
			blockerDigest := n.digest
			var blockerDurationMs int64
			// Enrich from transaction data (trx duration, digest)
			if trx, ok := trxByPID[n.pid]; ok {
				if !trx.TrxStarted.IsZero() {
					blockerDurationMs = snapshot.Time.Sub(trx.TrxStarted).Milliseconds()
				}
				if blockerQuery == "" && trx.Query != "" {
					blockerQuery = trx.Query
				}
				if blockerDigest == "" && trx.DigestText != "" {
					blockerDigest = trx.DigestText
				}
			}
			// Fallback to process info for query
			if blockerQuery == "" {
				if proc, ok := procByPID[n.pid]; ok && proc.Info != "" {
					blockerQuery = proc.Info
				}
			}
			// Last resort: check if any LockWait captured the blocker's query
			if blockerQuery == "" {
				for _, lw := range lockWaits {
					if lw.BlockingPID == n.pid && lw.BlockingQuery != "" {
						blockerQuery = lw.BlockingQuery
						break
					}
				}
			}
			entries = append(entries, treeEntry{
				pid:       n.pid,
				user:      n.user,
				host:      n.host,
				query:     blockerQuery,
				digest:    blockerDigest,
				waitMs:    blockerDurationMs,
				table:     n.table,
				isBlocker: true,
			})
		}
		kids := children[pid]
		for i, childPID := range kids {
			if visited[childPID] {
				continue
			}
			cn := info[childPID]
			entries = append(entries, treeEntry{
				pid:    cn.pid,
				user:   cn.user,
				host:   cn.host,
				query:  cn.query,
				digest: cn.digest,
				waitMs: waitMs[childPID],
				table:  waiterTable[childPID],
				isLast: i == len(kids)-1,
			})
			// Recurse into this child's children (for chains)
			if len(children[childPID]) > 0 {
				visited[childPID] = true
				for j, grandchild := range children[childPID] {
					if visited[grandchild] {
						continue
					}
					gn := info[grandchild]
					isLastGrand := j == len(children[childPID])-1
					entries = append(entries, treeEntry{
						pid:    gn.pid,
						user:   gn.user,
						host:   gn.host,
						query:  gn.query,
						digest: gn.digest,
						waitMs: waitMs[grandchild],
						table:  waiterTable[grandchild],
						isLast: isLastGrand,
					})
					visited[grandchild] = true
				}
			}
		}
	}

	for _, root := range roots {
		walk(root, true)
	}

	// Append deadlock participants. A deadlock has by definition >=2
	// transactions, so we render every parsed participant — even
	// rolled-back victims whose threads are gone from the process
	// list. Showing only the survivor would mislead the operator
	// into thinking it was a single-actor stall.
	//
	// All deadlock entries are rendered with blocker styling (equal
	// weight). First gets ┌, last gets └ when there are multiple
	// participants; a singleton gets a stand-alone connector.
	if dl := snapshot.InnoDBStatus.LatestDeadlock; dl != nil {
		var participants []db.DeadlockTransaction
		for _, dt := range dl.Transactions {
			if dt.ThreadID != 0 {
				participants = append(participants, dt)
			}
		}
		// Parse deadlock timestamp for duration fallback (rolled-back trx has no active transaction)
		var dlTime time.Time
		if dl.Timestamp != "" {
			dlTime, _ = time.Parse("2006-01-02 15:04:05", dl.Timestamp)
		}
		for i, dt := range participants {
			// Enrich with duration, query, and digest from live
			// transaction/process data. The InnoDB-status text in
			// dt.Query is least trustworthy: deadlock victims have
			// already been rolled back, so the captured statement is
			// often just a leading comment with no body. Prefer in
			// order: live digest → live trx query → process info →
			// raw InnoDB-status query.
			var digest string
			var query string
			var durationMs int64
			if trx, ok := trxByPID[dt.ThreadID]; ok {
				if !trx.TrxStarted.IsZero() {
					durationMs = snapshot.Time.Sub(trx.TrxStarted).Milliseconds()
				}
				if trx.DigestText != "" {
					digest = trx.DigestText
				}
				if query == "" && trx.Query != "" {
					query = trx.Query
				}
			}
			if durationMs == 0 && !dlTime.IsZero() {
				durationMs = snapshot.Time.Sub(dlTime).Milliseconds()
			}
			if query == "" {
				if proc, ok := procByPID[dt.ThreadID]; ok && proc.Info != "" {
					query = proc.Info
				}
			}
			if query == "" {
				query = dt.Query
			}
			table := dt.TableName
			entries = append(entries, treeEntry{
				pid:        dt.ThreadID,
				user:       dt.User,
				host:       dt.Host,
				query:      query,
				digest:     digest,
				waitMs:     durationMs,
				table:      table,
				isBlocker:  true,
				isDeadlock: true,
				// isLast marks the closing connector. A singleton
				// participant is its own first-and-last; multiple
				// participants close on the final entry.
				isLast: i == len(participants)-1,
			})
		}
	}

	return entries
}

// renderHeader produces the one-row chrome at the top of every view:
// title + tab bar on the left, compact context on the right (snapshot
// time, server version with variant tag, uptime, and an optional [cw]
// indicator when CloudWatch metrics are wired). The Server / counts /
// DB Load rows that lived here historically have moved into each
// view's body — the header is now strictly chrome, no data.
func renderHeader(m Model) string {
	var b strings.Builder

	left := titleStyle.Render("MySQL Lock Monitor") + " " + renderTabBar(m)
	right := renderHeaderContext(m)

	if m.width <= 0 {
		// Pre-WindowSizeMsg path (very early frames or tests): fall
		// back to a stacked layout instead of guessing widths.
		b.WriteString(left)
		if right != "" {
			b.WriteString("\n")
			b.WriteString(right)
		}
	} else {
		leftW := lipgloss.Width(left)
		rightW := lipgloss.Width(right)
		gap := m.width - leftW - rightW
		if gap < 1 {
			gap = 1
		}
		b.WriteString(left)
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(right)
	}
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", widthOr120(m.width)))
	b.WriteString("\n")
	return b.String()
}

// renderHeaderContext is the right-aligned compact line: time · version
// + variant · uptime · [cw] indicator. Each segment is suppressed when
// its data isn't available — operator never sees empty separators.
func renderHeaderContext(m Model) string {
	snap := m.result.Snapshot
	var parts []string

	if !snap.Time.IsZero() {
		parts = append(parts, snap.Time.Format("15:04:05"))
	}
	if snap.ServerInfo.Version != "" {
		v := compactVersion(snap.ServerInfo.Version)
		if snap.ServerInfo.IsMariaDB {
			v += " MariaDB"
		}
		if snap.ServerInfo.IsAurora {
			v += " Aurora"
		} else if snap.ServerInfo.IsRDS {
			v += " RDS"
		}
		parts = append(parts, v)
	}
	if uptime := serverUptime(m); uptime > 0 {
		parts = append(parts, "up "+humanUptime(uptime))
	}
	if cw := cloudWatchIndicator(m); cw != "" {
		parts = append(parts, cw)
	}

	if len(parts) == 0 {
		return ""
	}
	return dimStyle.Render(strings.Join(parts, " · "))
}

// compactVersion strips the build suffix MySQL adds (e.g. "8.0.45-0ubuntu0…")
// to keep the chrome tight.
func compactVersion(v string) string {
	if i := strings.Index(v, "-"); i > 0 {
		return v[:i]
	}
	return v
}

// serverUptime returns the latest reported uptime, or 0 if no health
// snapshot has landed yet.
func serverUptime(m Model) time.Duration {
	if m.insights == nil || m.insights.Health == nil {
		return 0
	}
	hv := m.insights.Health.Latest()
	if hv.UptimeSeconds == 0 {
		return 0
	}
	return time.Duration(hv.UptimeSeconds) * time.Second
}

// humanUptime renders durations as "14d 3h", "2h 17m", or "47s" — the
// longer the uptime, the coarser the granularity, since the operator
// doesn't care that a 14-day-old server is +47 seconds.
func humanUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) - days*24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}

// cloudWatchIndicator returns "[cw]●" (bright) when CloudWatch has
// produced at least one sample, "[cw]○" (dim) when configured but
// no sample yet, or "" when no CW context exists.
func cloudWatchIndicator(m Model) string {
	if m.insights == nil || m.insights.CloudWatch == nil {
		return ""
	}
	probe := m.insights.CloudWatch.Probe()
	if !probe.Available {
		return ""
	}
	latest := m.insights.CloudWatch.Latest()
	if latest.Time.IsZero() {
		// Configured, no sample yet — dim circle.
		return "[cw]" + dimStyle.Render("○")
	}
	// Bright dot reuses okStyle (green) so the "good" affordance is
	// consistent with the [HEALTHY] verdict.
	return "[cw]" + okStyle().Render("●")
}

func renderFooter(m Model) string {
	return statusBarStyle.Width(m.width).Render(renderStatusBar(m))
}

func renderView(m Model, body string) string {
	header := renderHeader(m)
	footer := renderFooter(m)

	headerLines := strings.Count(header, "\n")
	footerLines := strings.Count(footer, "\n") + 1
	bodyLines := strings.Count(body, "\n")

	// Pad body so footer sits at the bottom
	padding := m.height - headerLines - footerLines - bodyLines
	if padding < 0 {
		padding = 0
	}

	return header + body + strings.Repeat("\n", padding) + footer
}

func renderMain(m Model) string {
	switch m.view {
	case ViewOverview:
		return renderView(m, renderOverview(m))
	case ViewIssues:
		return renderView(m, renderIssuesView(m))
	case ViewIssueDetail:
		return renderView(m, renderIssueDetail(m))
	case ViewTables:
		return renderView(m, renderTablesView(m))
	case ViewMDL:
		return renderView(m, renderMDL(m))
	case ViewTop:
		return renderView(m, renderTopPanel(m))
	case ViewExplain:
		return renderView(m, renderExplainModal(m))
	case ViewHelp:
		return renderView(m, renderHelp(m))
	}

	// ViewLock (default fallback)
	var b strings.Builder
	entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
	if len(entries) == 0 {
		b.WriteString(dimStyle.Render("  No lock contention detected."))
		b.WriteString("\n")
	} else {
		b.WriteString(headerStyle.Render("Lock Tree:"))
		b.WriteString("\n")
		b.WriteString(renderLockTreeNav(m))
	}

	if m.confirmKill {
		b.WriteString("\n")
		b.WriteString(criticalStyle.Render(fmt.Sprintf("  Kill connection %d? [y/N] ", m.confirmPID)))
	}

	return renderView(m, b.String())
}

// renderIssuesView wraps the issues table and adds a kill-confirm
// footer if appropriate.
func renderIssuesView(m Model) string {
	var b strings.Builder
	b.WriteString(renderIssuesTable(m))
	if m.confirmKill {
		b.WriteString("\n")
		b.WriteString(criticalStyle.Render(fmt.Sprintf("  Kill connection %d? [y/N] ", m.confirmPID)))
	}
	return b.String()
}

func renderLockTreeNav(m Model) string {
	const maxWaiters = 5

	var b strings.Builder
	entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)

	waiterCount := 0 // waiters shown under current blocker
	totalWaiters := 0 // total waiters under current blocker (for "N more" message)

	// Pre-count waiters per blocker group to know totals
	blockerWaiterCounts := make(map[int]int) // index of blocker -> total waiter count
	currentBlockerIdx := -1
	for i, e := range entries {
		if e.isBlocker {
			currentBlockerIdx = i
		} else {
			blockerWaiterCounts[currentBlockerIdx]++
		}
	}

	for i, e := range entries {
		selected := i == m.cursor

		if e.isBlocker {
			waiterCount = 0
			totalWaiters = blockerWaiterCounts[i]
		} else {
			waiterCount++
			if waiterCount > maxWaiters {
				if waiterCount == maxWaiters+1 {
					remaining := totalWaiters - maxWaiters
					b.WriteString("  " + "└──" + dimStyle.Render(fmt.Sprintf(" ... %d more waiting", remaining)) + "\n")
				}
				continue
			}
		}

		// Build plain text parts
		var plainLine string
		if e.isBlocker {
			label := "BLOCKER"
			connector := "┌"
			if e.isDeadlock {
				label = "DEADLOCK"
				if e.isLast {
					connector = "└"
				}
			}
			plainLine = fmt.Sprintf("%s %s PID:%d %s@%s", connector, label, e.pid, e.user, stripPort(e.host))
			if e.waitMs > 0 {
				plainLine += " " + humanDuration(e.waitMs)
			}
			if e.table != "" {
				plainLine += " " + e.table
			}
			ql := queryLabel(e.digest, e.query)
			if ql == "" && e.isDeadlock {
				ql = "(deadlock victim, rolled back)"
			}
			if ql != "" {
				plainLine += " [" + ql + "]"
			}
		} else {
			connector := "├"
			if e.isLast || waiterCount == maxWaiters {
				connector = "└"
			}
			label := "WAITING"
			if e.isDeadlock {
				label = "DEADLOCK"
			}
			plainLine = fmt.Sprintf("%s── %s PID:%d %s@%s", connector, label, e.pid, e.user, stripPort(e.host))
			if e.waitMs > 0 {
				plainLine += " " + humanDuration(e.waitMs)
			}
			if e.table != "" {
				plainLine += " " + e.table
			}
			ql := queryLabel(e.digest, e.query)
			if ql == "" && e.isDeadlock {
				ql = "(deadlock victim, rolled back)"
			}
			if ql != "" {
				plainLine += " [" + ql + "]"
			}
		}

		prefix := "  "
		if selected && e.isBlocker {
			// Selected blocker: gray background, red highlights preserved
			prefix = "> "
			hs := selectedHeaderStyle
			ds := selectedDangerStyle
			label := "BLOCKER"
			connector := "┌"
			if e.isDeadlock {
				label = "DEADLOCK"
				if e.isLast {
					connector = "└"
				}
			}
			line := connector + hs.Render(" ") + ds.Render(label) + hs.Render(" "+fmt.Sprintf("PID:%d %s@%s", e.pid, e.user, stripPort(e.host)))
			if e.waitMs > 0 {
				line += ds.Render(" "+humanDuration(e.waitMs))
			}
			if e.table != "" {
				line += hs.Render(" "+e.table)
			}
			ql := queryLabel(e.digest, e.query)
			if ql == "" && e.isDeadlock {
				ql = "(deadlock victim, rolled back)"
			}
			if ql != "" {
				line += hs.Render(" [" + ql + "]")
			}
			b.WriteString(selectedStyle.Render(prefix) + line)
		} else if e.isBlocker {
			// Unselected blocker: red label + red duration, rest bold white
			label := "BLOCKER"
			connector := "┌"
			if e.isDeadlock {
				label = "DEADLOCK"
				if e.isLast {
					connector = "└"
				}
			}
			line := connector + " " + dangerStyle.Render(label) + " " + headerStyle.Render(fmt.Sprintf("PID:%d %s@%s", e.pid, e.user, stripPort(e.host)))
			if e.waitMs > 0 {
				line += " " + dangerStyle.Render(humanDuration(e.waitMs))
			}
			if e.table != "" {
				line += " " + headerStyle.Render(e.table)
			}
			ql := queryLabel(e.digest, e.query)
			if ql == "" && e.isDeadlock {
				ql = "(deadlock victim, rolled back)"
			}
			if ql != "" {
				line += " " + headerStyle.Render("[" + ql + "]")
			}
			b.WriteString(prefix + line)
		} else {
			// Waiter: unstyled connector, dim rest
			connector := "├──"
			if e.isLast || waiterCount == maxWaiters {
				connector = "└──"
			}
			// Build the dim portion (everything after the connector)
			dimPart := fmt.Sprintf(" %s PID:%d %s@%s", "WAITING", e.pid, e.user, stripPort(e.host))
			if e.isDeadlock {
				dimPart = fmt.Sprintf(" %s PID:%d %s@%s", "DEADLOCK", e.pid, e.user, stripPort(e.host))
			}
			if e.waitMs > 0 {
				dimPart += " " + humanDuration(e.waitMs)
			}
			if e.table != "" {
				dimPart += " " + e.table
			}
			ql := queryLabel(e.digest, e.query)
			if ql == "" && e.isDeadlock {
				ql = "(deadlock victim, rolled back)"
			}
			if ql != "" {
				dimPart += " [" + ql + "]"
			}
			b.WriteString(prefix + connector + dimStyle.Render(dimPart))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func renderStatusBar(m Model) string {
	status := "MySQL Lock Monitor"
	if m.statusMsg != "" {
		status += " | " + m.statusMsg
	}

	switch m.view {
	case ViewOverview:
		status += " | u/h/s:group j/k enter:drill I:issues B:tables L:lock t:top ?:help"
		return status
	case ViewIssues:
		if m.issuesTableFilter != "" {
			status += " | filter=" + m.issuesTableFilter
		}
		status += " | j/k enter:detail y:yank K:kill /:clear B:tables tab/L:lock ?:help"
		return status
	case ViewIssueDetail:
		status += " | y:yank K:kill esc:back ?:help"
		return status
	case ViewLock:
		status += " | j/k K:kill tab/I:issues B:tables ?:help"
		return status
	case ViewTables:
		status += " | j/k enter:drill K:kill-all I:issues L:lock ?:help"
		return status
	case ViewTop:
		status += " | j/k s:sort e:explain esc:back ?:help"
		if m.insights != nil {
			status += fmt.Sprintf(" | digests=%d evicted=%d",
				m.insights.Registry.Len(), m.insights.Registry.Evicted())
		}
		return status
	case ViewExplain:
		status += " | esc:back ?:help"
		return status
	case ViewHelp:
		status += " | press any key to dismiss"
		return status
	}

	status += " | q:quit"
	return status
}

func severityBadge(s detector.Severity) string {
	switch s {
	case detector.SeverityCritical:
		return criticalStyle.Render("[CRIT]")
	case detector.SeverityWarning:
		return warningStyle.Render("[WARN]")
	default:
		return infoStyle.Render("[INFO]")
	}
}

// stripPort removes the port suffix from a host string like "172.22.0.3:49018" -> "172.22.0.3".
func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// humanDuration formats milliseconds into a human-readable string like "12s", "2m30s", "1h5m".
func humanDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	secs := ms / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	remSecs := secs % 60
	if mins < 60 {
		if remSecs == 0 {
			return fmt.Sprintf("%dm", mins)
		}
		return fmt.Sprintf("%dm%ds", mins, remSecs)
	}
	hours := mins / 60
	remMins := mins % 60
	if remMins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, remMins)
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
