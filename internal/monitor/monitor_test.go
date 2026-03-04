package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDB struct {
	serverInfo   db.ServerInfo
	transactions []db.Transaction
	lockWaits    []db.LockWait
	processes    []db.Process
	innodbStatus db.InnoDBStatus
	connID       uint64
}

func (m *mockDB) ServerInfo(ctx context.Context) (db.ServerInfo, error) {
	return m.serverInfo, nil
}

func (m *mockDB) Transactions(ctx context.Context) ([]db.Transaction, error) {
	return m.transactions, nil
}

func (m *mockDB) LockWaits(ctx context.Context) ([]db.LockWait, error) {
	return m.lockWaits, nil
}

func (m *mockDB) Processes(ctx context.Context) ([]db.Process, error) {
	return m.processes, nil
}

func (m *mockDB) MetadataLocks(ctx context.Context) ([]db.MetadataLock, error) {
	return nil, nil
}

func (m *mockDB) InnoDBStatus(ctx context.Context) (db.InnoDBStatus, error) {
	return m.innodbStatus, nil
}

func (m *mockDB) KillConnection(ctx context.Context, id uint64) error {
	return nil
}

func (m *mockDB) ConnectionID(ctx context.Context) (uint64, error) {
	return m.connID, nil
}

func (m *mockDB) Close() error {
	return nil
}

func TestMonitorSnapshot(t *testing.T) {
	now := time.Now()
	mock := &mockDB{
		serverInfo: db.ServerInfo{Version: "8.0.32", VersionNumber: 80032},
		transactions: []db.Transaction{
			{ID: 1, User: "root", Host: "localhost", TrxID: "100", TrxStarted: now.Add(-60 * time.Second), Query: "SELECT 1"},
		},
		processes: []db.Process{
			{ID: 1, User: "root", Host: "localhost", Command: "Query", Time: 60},
		},
	}

	cfg := DefaultConfig()
	mon := New(mock, cfg)

	result := mon.Snapshot(context.Background())
	require.NoError(t, result.Error)
	assert.Equal(t, "8.0.32", result.Snapshot.ServerInfo.Version)
	assert.Len(t, result.Snapshot.Transactions, 1)
	assert.Len(t, result.Snapshot.Processes, 1)
	// Should detect long transaction (60s > 30s threshold)
	assert.NotEmpty(t, result.Issues)
}

func TestMonitorRun(t *testing.T) {
	mock := &mockDB{
		serverInfo: db.ServerInfo{Version: "8.0.32", VersionNumber: 80032},
	}

	cfg := DefaultConfig()
	cfg.Interval = 50 * time.Millisecond
	mon := New(mock, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ch := mon.Run(ctx)

	var results []Result
	for r := range ch {
		results = append(results, r)
	}

	assert.GreaterOrEqual(t, len(results), 2, "should receive at least 2 results")
}

func TestResultMaxSeverity(t *testing.T) {
	r := Result{
		Issues: []detector.Issue{
			{Severity: detector.SeverityInfo},
			{Severity: detector.SeverityCritical},
			{Severity: detector.SeverityWarning},
		},
	}
	assert.Equal(t, detector.SeverityCritical, r.MaxSeverity())
}
