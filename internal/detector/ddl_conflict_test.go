package detector

import (
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDDL(t *testing.T) {
	assert.True(t, isDDL("ALTER TABLE users ADD COLUMN x INT"))
	assert.True(t, isDDL("CREATE TABLE test (id INT)"))
	assert.True(t, isDDL("DROP TABLE test"))
	assert.True(t, isDDL("RENAME TABLE test TO test2"))
	assert.True(t, isDDL("TRUNCATE TABLE test"))
	assert.False(t, isDDL("SELECT * FROM users"))
	assert.False(t, isDDL("UPDATE users SET name='test'"))
	assert.False(t, isDDL("INSERT INTO users VALUES (1)"))
	assert.False(t, isDDL("DELETE FROM users WHERE id=1"))
}

func TestDDLConflictDetector(t *testing.T) {
	now := time.Now()
	d := NewDDLConflictDetector(10 * time.Second)

	t.Run("no conflicts", func(t *testing.T) {
		snapshot := db.Snapshot{Time: now}
		issues := d.Detect(snapshot)
		assert.Empty(t, issues)
	})

	t.Run("DDL waiting for metadata lock", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			Processes: []db.Process{
				{ID: 1, User: "root", Host: "localhost", DB: "mydb", Command: "Query", Time: 15, State: "Waiting for table metadata lock", Info: "ALTER TABLE users ADD COLUMN x INT"},
			},
			Transactions: []db.Transaction{
				{ID: 2, User: "app", Host: "app1", DB: "mydb", TrxID: "100", TrxStarted: now.Add(-60 * time.Second)},
			},
		}
		issues := d.Detect(snapshot)
		require.Len(t, issues, 1)
		assert.Equal(t, "ddl_conflict", issues[0].Detector)
		assert.Equal(t, SeverityCritical, issues[0].Severity)
		assert.Contains(t, issues[0].Description, "ALTER TABLE")
	})

	t.Run("DML blocked by DDL", func(t *testing.T) {
		snapshot := db.Snapshot{
			Time: now,
			Processes: []db.Process{
				{ID: 1, User: "root", Host: "localhost", DB: "mydb", Command: "Query", Time: 5, State: "altering table", Info: "ALTER TABLE users ADD COLUMN x INT"},
				{ID: 2, User: "app", Host: "app1", DB: "mydb", Command: "Query", Time: 3, State: "Waiting for table metadata lock", Info: "SELECT * FROM users WHERE id=1"},
			},
		}
		issues := d.Detect(snapshot)
		require.NotEmpty(t, issues)
		found := false
		for _, issue := range issues {
			if issue.Title == "DML blocked by DDL" {
				found = true
				assert.Contains(t, issue.Description, "SELECT")
			}
		}
		assert.True(t, found)
	})
}
