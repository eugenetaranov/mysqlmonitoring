package detector

import (
	"fmt"
	"strings"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// LockChainDetector detects lock dependency chains and cycles.
type LockChainDetector struct {
	WaitThreshold time.Duration
}

// NewLockChainDetector creates a lock chain detector.
func NewLockChainDetector(waitThreshold time.Duration) *LockChainDetector {
	return &LockChainDetector{WaitThreshold: waitThreshold}
}

func (d *LockChainDetector) Name() string {
	return "lock_chain"
}

// LockGraph represents lock dependencies as an adjacency list.
// Key: blocker PID, Value: list of waiter PIDs.
type LockGraph struct {
	Edges    map[uint64][]uint64
	WaitInfo map[uint64]db.LockWait // keyed by waiting PID
}

// BuildLockGraph constructs a lock dependency graph from lock waits.
func BuildLockGraph(waits []db.LockWait) LockGraph {
	g := LockGraph{
		Edges:    make(map[uint64][]uint64),
		WaitInfo: make(map[uint64]db.LockWait),
	}

	for _, w := range waits {
		g.Edges[w.BlockingPID] = append(g.Edges[w.BlockingPID], w.WaitingPID)
		g.WaitInfo[w.WaitingPID] = w
	}

	return g
}

// FindChains returns all lock chains starting from root blockers.
func (g LockGraph) FindChains() [][]uint64 {
	// Find root blockers (PIDs that block others but aren't waiting)
	waiters := make(map[uint64]bool)
	for _, w := range g.WaitInfo {
		waiters[w.WaitingPID] = true
	}

	var roots []uint64
	for blocker := range g.Edges {
		if !waiters[blocker] {
			roots = append(roots, blocker)
		}
	}

	var chains [][]uint64
	for _, root := range roots {
		chains = append(chains, g.dfsChains(root, []uint64{root}, make(map[uint64]bool))...)
	}

	return chains
}

func (g LockGraph) dfsChains(node uint64, path []uint64, visited map[uint64]bool) [][]uint64 {
	visited[node] = true
	waiters := g.Edges[node]

	if len(waiters) == 0 {
		chain := make([]uint64, len(path))
		copy(chain, path)
		return [][]uint64{chain}
	}

	var chains [][]uint64
	for _, waiter := range waiters {
		if visited[waiter] {
			// Cycle detected - include it
			chain := make([]uint64, len(path)+1)
			copy(chain, path)
			chain[len(path)] = waiter
			chains = append(chains, chain)
			continue
		}
		chains = append(chains, g.dfsChains(waiter, append(path, waiter), visited)...)
	}

	delete(visited, node)
	return chains
}

// FindCycles returns cycles in the lock graph.
func (g LockGraph) FindCycles() [][]uint64 {
	var cycles [][]uint64
	visited := make(map[uint64]int) // 0=unvisited, 1=in-stack, 2=done

	for node := range g.Edges {
		if visited[node] == 0 {
			g.dfsCycles(node, []uint64{node}, visited, &cycles)
		}
	}

	return cycles
}

func (g LockGraph) dfsCycles(node uint64, path []uint64, visited map[uint64]int, cycles *[][]uint64) {
	visited[node] = 1

	for _, next := range g.Edges[node] {
		if visited[next] == 1 {
			// Found a cycle - extract it
			cycleStart := -1
			for i, n := range path {
				if n == next {
					cycleStart = i
					break
				}
			}
			if cycleStart >= 0 {
				cycle := make([]uint64, len(path)-cycleStart)
				copy(cycle, path[cycleStart:])
				*cycles = append(*cycles, cycle)
			}
		} else if visited[next] == 0 {
			g.dfsCycles(next, append(path, next), visited, cycles)
		}
	}

	visited[node] = 2
}

func (d *LockChainDetector) Detect(snapshot db.Snapshot) []Issue {
	if len(snapshot.LockWaits) == 0 {
		return nil
	}

	var issues []Issue
	graph := BuildLockGraph(snapshot.LockWaits)

	// Report lock chains
	chains := graph.FindChains()
	for _, chain := range chains {
		if len(chain) < 2 {
			continue
		}

		severity := SeverityWarning
		if len(chain) >= 4 {
			severity = SeverityCritical
		}

		// Check if any wait exceeds threshold
		for _, pid := range chain[1:] {
			if w, ok := graph.WaitInfo[pid]; ok {
				if w.WaitDurationMs > d.WaitThreshold.Milliseconds() {
					severity = SeverityCritical
				}
			}
		}

		chainDesc := formatChain(chain, graph)

		// Extract root blocker query and blocked queries
		rootQuery := ""
		var blockedQueries []string
		if len(chain) > 1 {
			if w, ok := graph.WaitInfo[chain[1]]; ok {
				rootQuery = preferDigest(w.BlockingDigest, w.BlockingQuery, 50)
			}
		}
		for _, pid := range chain[1:] {
			if w, ok := graph.WaitInfo[pid]; ok {
				q := preferDigest(w.WaitingDigest, w.WaitingQuery, 50)
				if q != "" {
					blockedQueries = append(blockedQueries, q)
				}
			}
		}

		issues = append(issues, Issue{
			Detector:    d.Name(),
			Severity:    severity,
			Title:       fmt.Sprintf("Lock chain detected (depth %d)", len(chain)),
			Description: chainDesc,
			Details: map[string]string{
				"chain_depth":     fmt.Sprintf("%d", len(chain)),
				"root_blocker":    fmt.Sprintf("%d", chain[0]),
				"chain":           chainDesc,
				"root_query":      rootQuery,
				"blocked_queries": strings.Join(blockedQueries, ", "),
			},
		})
	}

	// Report cycles
	cycles := graph.FindCycles()
	for _, cycle := range cycles {
		issues = append(issues, Issue{
			Detector:    d.Name(),
			Severity:    SeverityCritical,
			Title:       fmt.Sprintf("Lock cycle detected (%d participants)", len(cycle)),
			Description: fmt.Sprintf("Circular lock dependency: %v", cycle),
			Details: map[string]string{
				"cycle_size": fmt.Sprintf("%d", len(cycle)),
			},
		})
	}

	return issues
}

func formatChain(chain []uint64, graph LockGraph) string {
	var parts []string
	for i, pid := range chain {
		info := fmt.Sprintf("PID:%d", pid)
		if i == 0 {
			if w, ok := graph.WaitInfo[chain[1]]; ok {
				info = fmt.Sprintf("PID:%d(%s@%s)", pid, w.BlockingUser, w.BlockingHost)
				if w.LockTable != "" {
					info += " " + w.LockTable
				}
				if q := preferDigest(w.BlockingDigest, w.BlockingQuery, 50); q != "" {
					info += " [" + q + "]"
				}
			}
		} else {
			if w, ok := graph.WaitInfo[pid]; ok {
				info = fmt.Sprintf("PID:%d(%s@%s)", pid, w.WaitingUser, w.WaitingHost)
				if w.LockTable != "" {
					info += " " + w.LockTable
				}
				if q := preferDigest(w.WaitingDigest, w.WaitingQuery, 50); q != "" {
					info += " [" + q + "]"
				}
			}
		}
		parts = append(parts, info)
	}
	return strings.Join(parts, " -> ")
}

// preferDigest returns the digest if available, otherwise the raw query, truncated.
func preferDigest(digest, rawQuery string, maxLen int) string {
	if digest != "" {
		return truncateQuery(digest, maxLen)
	}
	return truncateQuery(rawQuery, maxLen)
}
