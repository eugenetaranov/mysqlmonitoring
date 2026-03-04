package output

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testResult() monitor.Result {
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	return monitor.Result{
		Snapshot: db.Snapshot{
			Time:       now,
			ServerInfo: db.ServerInfo{Version: "8.0.32"},
			Transactions: []db.Transaction{
				{ID: 1, User: "root", Host: "localhost", DB: "testdb", TrxID: "100", TrxState: "RUNNING", Time: 60, Query: "SELECT * FROM users"},
			},
			LockWaits: []db.LockWait{
				{WaitingPID: 2, WaitingUser: "app", WaitingHost: "app1", BlockingPID: 1, BlockingUser: "root", BlockingHost: "localhost", LockTable: "users", WaitDurationMs: 5000},
			},
			Processes: []db.Process{
				{ID: 1, User: "root", Host: "localhost", Command: "Query", Time: 60},
			},
		},
		Issues: []detector.Issue{
			{Detector: "long_transaction", Severity: detector.SeverityWarning, Title: "Long-running transaction (60s)", Description: "Transaction 100 by root@localhost"},
		},
	}
}

func TestFormatText(t *testing.T) {
	var buf bytes.Buffer
	FormatText(&buf, testResult())

	output := buf.String()
	assert.Contains(t, output, "MySQL Lock Monitor")
	assert.Contains(t, output, "8.0.32")
	assert.Contains(t, output, "Issues (1)")
	assert.Contains(t, output, "Long-running transaction")
	assert.Contains(t, output, "Lock Waits:")
	assert.Contains(t, output, "Active Transactions:")
}

func TestFormatJSON(t *testing.T) {
	var buf bytes.Buffer
	err := FormatJSON(&buf, testResult())
	require.NoError(t, err)

	var output JSONOutput
	err = json.Unmarshal(buf.Bytes(), &output)
	require.NoError(t, err)

	assert.Equal(t, "8.0.32", output.Server)
	assert.Equal(t, 1, output.Transactions)
	assert.Equal(t, 1, output.LockWaits)
	assert.Len(t, output.Issues, 1)
	assert.Equal(t, "WARNING", output.MaxSeverity)
	assert.Equal(t, "long_transaction", output.Issues[0].Detector)
}

func TestFormatTextNoIssues(t *testing.T) {
	var buf bytes.Buffer
	r := monitor.Result{
		Snapshot: db.Snapshot{
			Time:       time.Now(),
			ServerInfo: db.ServerInfo{Version: "8.0.32"},
		},
	}
	FormatText(&buf, r)
	assert.Contains(t, buf.String(), "No issues detected")
}
