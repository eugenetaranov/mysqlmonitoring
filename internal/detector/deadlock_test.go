package detector

import (
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeadlockDetector(t *testing.T) {
	now := time.Now()
	d := NewDeadlockDetector()

	t.Run("no deadlock", func(t *testing.T) {
		snapshot := db.Snapshot{Time: now}
		issues := d.Detect(snapshot)
		assert.Empty(t, issues)
	})

	t.Run("deadlock detected", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			InnoDBStatus: db.InnoDBStatus{
				LatestDeadlock: &db.DeadlockInfo{
					Timestamp: "2024-01-15 10:25:30",
					Transactions: []db.DeadlockTransaction{
						{TrxID: "12345", ThreadID: 100, Query: "UPDATE users SET name='test' WHERE id=1", User: "root", Host: "localhost"},
						{TrxID: "12346", ThreadID: 101, Query: "UPDATE orders SET status='done' WHERE user_id=1", User: "app", Host: "app1"},
					},
				},
			},
		}
		issues := d.Detect(snapshot)
		require.Len(t, issues, 1)
		assert.Equal(t, SeverityCritical, issues[0].Severity)
		assert.Equal(t, "deadlock", issues[0].Detector)
		assert.Contains(t, issues[0].Title, "2 transactions")
	})

	t.Run("same deadlock not reported twice", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			InnoDBStatus: db.InnoDBStatus{
				LatestDeadlock: &db.DeadlockInfo{
					Timestamp: "2024-01-15 10:25:30",
					Transactions: []db.DeadlockTransaction{
						{TrxID: "12345", ThreadID: 100},
					},
				},
			},
		}
		// Already seen this timestamp from previous test
		issues := d.Detect(snapshot)
		assert.Empty(t, issues)
	})

	t.Run("new deadlock reported", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			InnoDBStatus: db.InnoDBStatus{
				LatestDeadlock: &db.DeadlockInfo{
					Timestamp: "2024-01-15 10:30:00",
					Transactions: []db.DeadlockTransaction{
						{TrxID: "12350", ThreadID: 105, Query: "DELETE FROM logs WHERE id=5"},
					},
				},
			},
		}
		issues := d.Detect(snapshot)
		require.Len(t, issues, 1)
	})
}
