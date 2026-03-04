package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockDetection_MySQL80(t *testing.T) {
	mc := setupMySQL(t, "mysql:8.0")
	testLockDetection(t, mc)
}

func TestLockDetection_MySQL57(t *testing.T) {
	mc := setupMySQL(t, "mysql:5.7")
	testLockDetection(t, mc)
}

func TestLockDetection_MariaDB(t *testing.T) {
	mc := setupMySQL(t, "mariadb:10")
	testLockDetection(t, mc)
}

func testLockDetection(t *testing.T, mc *mysqlContainer) {
	t.Helper()
	setupTestTable(t, mc.dsn)

	t.Run("detect long transaction", func(t *testing.T) {
		conn, err := sql.Open("mysql", mc.dsn)
		require.NoError(t, err)
		defer conn.Close()

		// Start a long transaction
		tx, err := conn.Begin()
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		_, err = tx.Exec("SELECT * FROM test_locks WHERE id=1 FOR UPDATE")
		require.NoError(t, err)

		// Give it a moment
		time.Sleep(2 * time.Second)

		// Monitor should detect it
		database, err := db.NewMySQL(mc.dsn)
		require.NoError(t, err)
		defer database.Close()

		cfg := monitor.DefaultConfig()
		cfg.LongQueryThreshold = 1 * time.Second
		cfg.CriticalTrxThreshold = 30 * time.Second

		mon := monitor.New(database, cfg)
		result := mon.Snapshot(context.Background())

		require.NoError(t, result.Error)
		assert.NotEmpty(t, result.Snapshot.Transactions, "should detect active transactions")
	})

	t.Run("detect lock wait", func(t *testing.T) {
		conn1, err := sql.Open("mysql", mc.dsn)
		require.NoError(t, err)
		defer conn1.Close()

		conn2, err := sql.Open("mysql", mc.dsn)
		require.NoError(t, err)
		defer conn2.Close()

		// Connection 1: lock a row
		tx1, err := conn1.Begin()
		require.NoError(t, err)
		defer func() { _ = tx1.Rollback() }()

		_, err = tx1.Exec("SELECT * FROM test_locks WHERE id=1 FOR UPDATE")
		require.NoError(t, err)

		// Connection 2: try to lock the same row (will block)
		done := make(chan error, 1)
		go func() {
			tx2, err := conn2.Begin()
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = tx2.Rollback() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err = tx2.ExecContext(ctx, "SELECT * FROM test_locks WHERE id=1 FOR UPDATE")
			done <- err
		}()

		// Wait briefly for lock wait to establish
		time.Sleep(2 * time.Second)

		// Monitor should detect the lock wait
		database, err := db.NewMySQL(mc.dsn)
		require.NoError(t, err)
		defer database.Close()

		mon := monitor.New(database, monitor.DefaultConfig())
		result := mon.Snapshot(context.Background())

		require.NoError(t, result.Error)
		assert.NotEmpty(t, result.Snapshot.LockWaits, "should detect lock waits")

		// Release locks
		_ = tx1.Rollback()
		<-done
	})

	t.Run("server info", func(t *testing.T) {
		database, err := db.NewMySQL(mc.dsn)
		require.NoError(t, err)
		defer database.Close()

		info, err := database.ServerInfo(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, info.Version)
		assert.Greater(t, info.VersionNumber, 0)
	})

	t.Run("innodb status", func(t *testing.T) {
		database, err := db.NewMySQL(mc.dsn)
		require.NoError(t, err)
		defer database.Close()

		status, err := database.InnoDBStatus(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, status.Raw)
	})

	t.Run("kill connection", func(t *testing.T) {
		database, err := db.NewMySQL(mc.dsn)
		require.NoError(t, err)
		defer database.Close()

		// Create a connection to kill
		victim, err := sql.Open("mysql", mc.dsn)
		require.NoError(t, err)
		defer victim.Close()

		var victimID uint64
		err = victim.QueryRow("SELECT CONNECTION_ID()").Scan(&victimID)
		require.NoError(t, err)

		err = database.KillConnection(context.Background(), victimID)
		require.NoError(t, err)
	})
}
