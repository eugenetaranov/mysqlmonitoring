package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
)

var reTableName = regexp.MustCompile(`(?i)(?:FROM|INTO|UPDATE|TABLE)\s+` + "`?" + `(\w+)` + "`?")

// simplifyQuery condenses a SQL query into a short label like DB monitoring tools.
func simplifyQuery(q string) string {
	if q == "" {
		return ""
	}
	upper := strings.ToUpper(strings.TrimSpace(q))
	tableName := ""
	if m := reTableName.FindStringSubmatch(q); len(m) > 1 {
		tableName = m[1]
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
// It prefers the normalized DIGEST_TEXT from performance_schema, falling back
// to the naive simplifyQuery() for cases where digest is unavailable
// (perf_schema disabled, deadlocks from InnoDB status parser, etc).
func queryLabel(digest, rawQuery string) string {
	if digest != "" {
		return digest
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

	// Append deadlock participants (if any) as killable entries.
	// Only show if the participant is still alive in the process list.
	// All deadlock entries are rendered with blocker styling (equal weight).
	// First gets ┌, last gets └ to visually group them.
	if dl := snapshot.InnoDBStatus.LatestDeadlock; dl != nil {
		aliveProcs := make(map[uint64]bool)
		for _, p := range snapshot.Processes {
			aliveProcs[p.ID] = true
		}
		var alive []db.DeadlockTransaction
		for _, dt := range dl.Transactions {
			if dt.ThreadID != 0 && aliveProcs[dt.ThreadID] {
				alive = append(alive, dt)
			}
		}
		// Parse deadlock timestamp for duration fallback (rolled-back trx has no active transaction)
		var dlTime time.Time
		if dl.Timestamp != "" {
			dlTime, _ = time.Parse("2006-01-02 15:04:05", dl.Timestamp)
		}
		for i, dt := range alive {
			// Enrich with duration, query, and digest from live transaction/process data
			query := dt.Query
			var digest string
			var durationMs int64
			if trx, ok := trxByPID[dt.ThreadID]; ok {
				if !trx.TrxStarted.IsZero() {
					durationMs = snapshot.Time.Sub(trx.TrxStarted).Milliseconds()
				}
				if query == "" && trx.Query != "" {
					query = trx.Query
				}
				if trx.DigestText != "" {
					digest = trx.DigestText
				}
			}
			// Fallback: time since deadlock detection
			if durationMs == 0 && !dlTime.IsZero() {
				durationMs = snapshot.Time.Sub(dlTime).Milliseconds()
			}
			if query == "" {
				if proc, ok := procByPID[dt.ThreadID]; ok && proc.Info != "" {
					query = proc.Info
				}
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
				isLast:     i == len(alive)-1 && i > 0,
			})
		}
	}

	return entries
}

func renderHeader(m Model) string {
	var b strings.Builder
	snap := m.result.Snapshot

	b.WriteString(titleStyle.Render("MySQL Lock Monitor"))
	b.WriteString("\n\n")

	if snap.ServerInfo.Version != "" {
		b.WriteString(headerStyle.Render("Server: "))
		b.WriteString(snap.ServerInfo.Version)
		if snap.ServerInfo.IsMariaDB {
			b.WriteString(" (MariaDB)")
		}
		if snap.ServerInfo.IsAurora {
			b.WriteString(" (Aurora)")
		} else if snap.ServerInfo.IsRDS {
			b.WriteString(" (RDS)")
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("Transactions: %d | Lock Waits: %d | Processes: %d\n",
		len(snap.Transactions), len(snap.LockWaits), len(snap.Processes)))
	b.WriteString(strings.Repeat("─", min(60, m.width)))
	b.WriteString("\n\n")

	return b.String()
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
	var b strings.Builder

	// Issues section
	if len(m.result.Issues) == 0 {
		b.WriteString(infoStyle.Render("No issues detected."))
		b.WriteString("\n")
	} else {
		b.WriteString(headerStyle.Render(fmt.Sprintf("Issues (%d):", len(m.result.Issues))))
		b.WriteString("\n")
		for _, issue := range m.result.Issues {
			b.WriteString("  ")
			b.WriteString(severityBadge(issue.Severity))
			b.WriteString(" ")
			b.WriteString(issue.Title)
			b.WriteString("\n")
			b.WriteString("    ")
			b.WriteString(dimStyle.Render(truncateStr(issue.Description, m.width-6)))
			b.WriteString("\n")
		}
	}

	// Lock tree section (only if there are entries to show)
	entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
	if len(entries) > 0 {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Lock Tree:"))
		b.WriteString("\n")
		b.WriteString(renderLockTreeNav(m))
	}

	// Kill confirmation popup
	if m.confirmKill {
		b.WriteString("\n")
		b.WriteString(criticalStyle.Render(fmt.Sprintf("  Kill connection %d? [y/N] ", m.confirmPID)))
	}

	return renderView(m, b.String())
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
					b.WriteString("  " + connectorStyle.Render("└──") + dimStyle.Render(fmt.Sprintf(" ... %d more waiting", remaining)) + "\n")
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
			queryLabel := queryLabel(e.digest, e.query)
			if queryLabel != "" {
				plainLine += " [" + queryLabel + "]"
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
			queryLabel := queryLabel(e.digest, e.query)
			if queryLabel != "" {
				plainLine += " [" + queryLabel + "]"
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
			line := hs.Render(connector+" ") + ds.Render(label) + hs.Render(" "+fmt.Sprintf("PID:%d %s@%s", e.pid, e.user, stripPort(e.host)))
			if e.waitMs > 0 {
				line += ds.Render(" "+humanDuration(e.waitMs))
			}
			if e.table != "" {
				line += hs.Render(" "+e.table)
			}
			queryLabel := queryLabel(e.digest, e.query)
			if queryLabel != "" {
				line += hs.Render(" ["+queryLabel+"]")
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
			queryLabel := queryLabel(e.digest, e.query)
			if queryLabel != "" {
				line += " " + headerStyle.Render("["+queryLabel+"]")
			}
			b.WriteString(prefix + line)
		} else {
			// Waiter: bright white connector, dim rest
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
			if ql != "" {
				dimPart += " [" + ql + "]"
			}
			b.WriteString(prefix + connectorStyle.Render(connector) + dimStyle.Render(dimPart))
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
	if len(m.result.Snapshot.LockWaits) > 0 {
		status += " | j/k:navigate K:kill"
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
