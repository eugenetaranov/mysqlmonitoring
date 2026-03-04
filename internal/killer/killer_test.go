package killer

import (
	"context"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDB struct {
	connID    uint64
	processes []db.Process
	killed    []uint64
}

func (m *mockDB) ServerInfo(ctx context.Context) (db.ServerInfo, error) {
	return db.ServerInfo{}, nil
}
func (m *mockDB) Transactions(ctx context.Context) ([]db.Transaction, error) { return nil, nil }
func (m *mockDB) LockWaits(ctx context.Context) ([]db.LockWait, error)       { return nil, nil }
func (m *mockDB) Processes(ctx context.Context) ([]db.Process, error) {
	return m.processes, nil
}
func (m *mockDB) MetadataLocks(ctx context.Context) ([]db.MetadataLock, error) { return nil, nil }
func (m *mockDB) InnoDBStatus(ctx context.Context) (db.InnoDBStatus, error) {
	return db.InnoDBStatus{}, nil
}
func (m *mockDB) KillConnection(ctx context.Context, id uint64) error {
	m.killed = append(m.killed, id)
	return nil
}
func (m *mockDB) ConnectionID(ctx context.Context) (uint64, error) { return m.connID, nil }
func (m *mockDB) Close() error                                     { return nil }

func TestKillSuccess(t *testing.T) {
	mock := &mockDB{
		connID: 1,
		processes: []db.Process{
			{ID: 2, User: "app", Host: "app1", Command: "Query"},
		},
	}

	k := New(mock)
	err := k.Kill(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, []uint64{2}, mock.killed)
}

func TestKillSelfRefused(t *testing.T) {
	mock := &mockDB{
		connID: 1,
		processes: []db.Process{
			{ID: 1, User: "root", Host: "localhost", Command: "Query"},
		},
	}

	k := New(mock)
	err := k.Kill(context.Background(), 1)
	require.Error(t, err)
	var safetyErr *SafetyError
	assert.ErrorAs(t, err, &safetyErr)
	assert.Contains(t, safetyErr.Reason, "own connection")
}

func TestKillSystemUserRefused(t *testing.T) {
	mock := &mockDB{
		connID: 1,
		processes: []db.Process{
			{ID: 2, User: "system user", Host: "", Command: "Connect"},
		},
	}

	k := New(mock)
	err := k.Kill(context.Background(), 2)
	require.Error(t, err)
	var safetyErr *SafetyError
	assert.ErrorAs(t, err, &safetyErr)
}

func TestKillReplicationRefused(t *testing.T) {
	mock := &mockDB{
		connID: 1,
		processes: []db.Process{
			{ID: 2, User: "repl", Host: "replica1", Command: "Binlog Dump"},
		},
	}

	k := New(mock)
	err := k.Kill(context.Background(), 2)
	require.Error(t, err)
	var safetyErr *SafetyError
	assert.ErrorAs(t, err, &safetyErr)
	assert.Contains(t, safetyErr.Reason, "replication")
}

func TestKillNotFound(t *testing.T) {
	mock := &mockDB{
		connID:    1,
		processes: []db.Process{},
	}

	k := New(mock)
	err := k.Kill(context.Background(), 99)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
