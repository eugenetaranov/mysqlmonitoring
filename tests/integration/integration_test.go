package integration

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type mysqlContainer struct {
	container testcontainers.Container
	dsn       string
	host      string
	port      string
}

func setupMySQL(t *testing.T, image string) *mysqlContainer {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "testpass",
			"MYSQL_DATABASE":     "testdb",
		},
		WaitingFor: wait.ForLog("ready for connections").
			WithOccurrence(2).
			WithStartupTimeout(120 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = container.Terminate(ctx)
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)

	mappedPort, err := container.MappedPort(ctx, "3306")
	require.NoError(t, err)

	dsn := fmt.Sprintf("root:testpass@tcp(%s:%s)/testdb?parseTime=true", host, mappedPort.Port())

	// Wait for MySQL to be fully ready
	require.Eventually(t, func() bool {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return false
		}
		defer db.Close()
		return db.Ping() == nil
	}, 60*time.Second, 1*time.Second, "MySQL did not become ready")

	return &mysqlContainer{
		container: container,
		dsn:       dsn,
		host:      host,
		port:      mappedPort.Port(),
	}
}

func setupTestTable(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS test_locks (
		id INT PRIMARY KEY AUTO_INCREMENT,
		name VARCHAR(100),
		value INT
	) ENGINE=InnoDB`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO test_locks (name, value) VALUES ('row1', 1), ('row2', 2), ('row3', 3) ON DUPLICATE KEY UPDATE name=name`)
	require.NoError(t, err)
}
