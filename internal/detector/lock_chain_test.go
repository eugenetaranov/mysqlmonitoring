package detector

import (
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildLockGraph(t *testing.T) {
	waits := []db.LockWait{
		{BlockingPID: 1, WaitingPID: 2, BlockingUser: "root", WaitingUser: "app", BlockingQuery: "UPDATE accounts SET balance=100", WaitingQuery: "SELECT * FROM accounts WHERE id=1", LockTable: "demodb.accounts"},
		{BlockingPID: 2, WaitingPID: 3, BlockingUser: "app", WaitingUser: "worker", BlockingQuery: "SELECT * FROM accounts WHERE id=1", WaitingQuery: "INSERT INTO logs VALUES(1)", LockTable: "demodb.accounts"},
	}

	graph := BuildLockGraph(waits)

	assert.Contains(t, graph.Edges, uint64(1))
	assert.Contains(t, graph.Edges[1], uint64(2))
	assert.Contains(t, graph.Edges, uint64(2))
	assert.Contains(t, graph.Edges[2], uint64(3))
}

func TestFindChains(t *testing.T) {
	waits := []db.LockWait{
		{BlockingPID: 1, WaitingPID: 2, BlockingQuery: "UPDATE t SET x=1", WaitingQuery: "SELECT * FROM t"},
		{BlockingPID: 2, WaitingPID: 3, BlockingQuery: "SELECT * FROM t", WaitingQuery: "INSERT INTO t VALUES(1)"},
	}

	graph := BuildLockGraph(waits)
	chains := graph.FindChains()

	require.Len(t, chains, 1)
	assert.Equal(t, []uint64{1, 2, 3}, chains[0])
}

func TestFindChainsBranching(t *testing.T) {
	waits := []db.LockWait{
		{BlockingPID: 1, WaitingPID: 2, BlockingQuery: "UPDATE t SET x=1", WaitingQuery: "SELECT * FROM t"},
		{BlockingPID: 1, WaitingPID: 3, BlockingQuery: "UPDATE t SET x=1", WaitingQuery: "DELETE FROM t WHERE id=2"},
	}

	graph := BuildLockGraph(waits)
	chains := graph.FindChains()

	assert.Len(t, chains, 2)
}

func TestFindCycles(t *testing.T) {
	waits := []db.LockWait{
		{BlockingPID: 1, WaitingPID: 2},
		{BlockingPID: 2, WaitingPID: 1},
	}

	graph := BuildLockGraph(waits)
	cycles := graph.FindCycles()

	require.NotEmpty(t, cycles)
}

func TestLockChainDetector(t *testing.T) {
	now := time.Now()
	d := NewLockChainDetector(10 * time.Second)

	t.Run("no locks", func(t *testing.T) {
		snapshot := db.Snapshot{Time: now}
		issues := d.Detect(snapshot)
		assert.Empty(t, issues)
	})

	t.Run("simple chain", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			LockWaits: []db.LockWait{
				{
					BlockingPID: 1, WaitingPID: 2,
					BlockingUser: "root", WaitingUser: "app",
					BlockingHost: "172.22.0.3:52228", WaitingHost: "172.22.0.3:52246",
					BlockingQuery: "UPDATE accounts SET balance=100 WHERE id=1",
					WaitingQuery:  "SELECT * FROM accounts WHERE id=1",
					LockTable:     "demodb.accounts",
					WaitDurationMs: 5000,
				},
			},
		}
		issues := d.Detect(snapshot)
		require.NotEmpty(t, issues)
		assert.Equal(t, "lock_chain", issues[0].Detector)
		assert.Contains(t, issues[0].Description, "demodb.accounts")
		assert.Contains(t, issues[0].Description, "UPDATE accounts SET balance=100 WHERE id=1")
		assert.Contains(t, issues[0].Description, "SELECT * FROM accounts WHERE id=1")
		assert.Equal(t, "UPDATE accounts SET balance=100 WHERE id=1", issues[0].Details["root_query"])
		assert.Equal(t, "SELECT * FROM accounts WHERE id=1", issues[0].Details["blocked_queries"])
	})

	t.Run("long wait becomes critical", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			LockWaits: []db.LockWait{
				{
					BlockingPID: 1, WaitingPID: 2,
					BlockingUser: "root", WaitingUser: "app",
					BlockingQuery: "ALTER TABLE big_table ADD INDEX idx_col(col)",
					WaitingQuery:  "INSERT INTO big_table VALUES(1,2,3)",
					LockTable:     "demodb.big_table",
					WaitDurationMs: 30000,
				},
			},
		}
		issues := d.Detect(snapshot)
		require.NotEmpty(t, issues)
		assert.Equal(t, SeverityCritical, issues[0].Severity)
		assert.NotEmpty(t, issues[0].Details["root_query"])
		assert.NotEmpty(t, issues[0].Details["blocked_queries"])
	})
}
