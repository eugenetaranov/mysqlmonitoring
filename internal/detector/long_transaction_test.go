package detector

import (
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
)

func TestLongTransactionDetector(t *testing.T) {
	now := time.Now()
	detector := NewLongTransactionDetector(30*time.Second, 5*time.Minute)

	tests := []struct {
		name     string
		snapshot db.Snapshot
		wantLen  int
		severity Severity
	}{
		{
			name: "no long transactions",
			snapshot: db.Snapshot{
				Time: now,
				Transactions: []db.Transaction{
					{ID: 1, User: "root", TrxID: "100", TrxStarted: now.Add(-10 * time.Second)},
				},
			},
			wantLen: 0,
		},
		{
			name: "warning level",
			snapshot: db.Snapshot{
				Time: now,
				Transactions: []db.Transaction{
					{ID: 1, User: "root", Host: "localhost", TrxID: "100", TrxStarted: now.Add(-60 * time.Second), Query: "SELECT * FROM users"},
				},
			},
			wantLen:  1,
			severity: SeverityWarning,
		},
		{
			name: "critical level",
			snapshot: db.Snapshot{
				Time: now,
				Transactions: []db.Transaction{
					{ID: 1, User: "root", Host: "localhost", TrxID: "100", TrxStarted: now.Add(-10 * time.Minute), Query: "ALTER TABLE users ADD COLUMN x INT"},
				},
			},
			wantLen:  1,
			severity: SeverityCritical,
		},
		{
			name: "multiple transactions mixed",
			snapshot: db.Snapshot{
				Time: now,
				Transactions: []db.Transaction{
					{ID: 1, User: "root", TrxID: "100", TrxStarted: now.Add(-5 * time.Second)},  // ok
					{ID: 2, User: "app", Host: "app1", TrxID: "101", TrxStarted: now.Add(-60 * time.Second)}, // warning
					{ID: 3, User: "app", Host: "app2", TrxID: "102", TrxStarted: now.Add(-10 * time.Minute)}, // critical
				},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := detector.Detect(tt.snapshot)
			assert.Len(t, issues, tt.wantLen)
			if tt.wantLen == 1 {
				assert.Equal(t, tt.severity, issues[0].Severity)
				assert.Equal(t, "long_transaction", issues[0].Detector)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	assert.Equal(t, "30s", formatDuration(30*time.Second))
	assert.Equal(t, "5m30s", formatDuration(5*time.Minute+30*time.Second))
	assert.Equal(t, "2h30m", formatDuration(2*time.Hour+30*time.Minute))
}

func TestTruncateQuery(t *testing.T) {
	assert.Equal(t, "short", truncateQuery("short", 100))
	assert.Equal(t, "12345...", truncateQuery("1234567890", 5))
}
