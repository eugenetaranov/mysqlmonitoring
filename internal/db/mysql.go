package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

func init() {
	// Silence the driver's stderr logger — its packet/IO timeout messages
	// otherwise bleed into the TUI. Errors still surface via returned error
	// values from QueryContext.
	_ = mysql.SetLogger(log.New(io.Discard, "", 0))
}

// queryWithRetry runs QueryContext and retries once on a transient connection
// error. The retry uses a fresh context with the same deadline so a stuck
// connection can't double the caller's budget.
func (m *MySQLDB) queryWithRetry(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err == nil || !isTransientConnErr(err) {
		return rows, err
	}
	return m.db.QueryContext(ctx, query, args...)
}

// queryRowWithRetry is the QueryRowContext analogue.
func (m *MySQLDB) queryRowWithRetry(ctx context.Context, query string, args ...any) *sql.Row {
	row := m.db.QueryRowContext(ctx, query, args...)
	if err := row.Err(); err != nil && isTransientConnErr(err) {
		return m.db.QueryRowContext(ctx, query, args...)
	}
	return row
}

// isTransientConnErr reports whether err is the kind of connection-level
// failure that's safe to retry once on a fresh pool connection.
func isTransientConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "invalid connection") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection reset")
}

// queryTimeoutHint caps server-side execution at 5s for monitoring SELECTs.
// Recognized by MySQL 5.7.4+; silently ignored as a comment elsewhere
// (MariaDB, older MySQL), where the per-query context timeout still applies.
const queryTimeoutHint = "/*+ MAX_EXECUTION_TIME(5000) */"

// MySQLDB implements the DB interface for MySQL/MariaDB.
type MySQLDB struct {
	db         *sql.DB
	serverInfo *ServerInfo
}

// NewMySQL creates a new MySQL connection.
func NewMySQL(dsn string) (*MySQLDB, error) {
	parsed, err := ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN: %w", err)
	}

	sqlDB, err := sql.Open("mysql", parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	sqlDB.SetMaxOpenConns(3)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	// Close idle connections before the server's wait_timeout (default 28800s,
	// but proxies/load balancers commonly drop idle conns much sooner) so we
	// avoid the "invalid connection" failure on the first poll after idle.
	sqlDB.SetConnMaxIdleTime(30 * time.Second)

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &MySQLDB{db: sqlDB}, nil
}

func (m *MySQLDB) Close() error {
	return m.db.Close()
}

func (m *MySQLDB) ServerInfo(ctx context.Context) (ServerInfo, error) {
	if m.serverInfo != nil {
		return *m.serverInfo, nil
	}

	var version string
	if err := m.queryRowWithRetry(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return ServerInfo{}, fmt.Errorf("failed to get version: %w", err)
	}

	info := ServerInfo{
		Version:   version,
		IsMariaDB: strings.Contains(strings.ToLower(version), "mariadb"),
		IsAurora:  strings.Contains(strings.ToLower(version), "aurora"),
	}
	info.VersionNumber = parseVersionNumber(version)

	// Detect RDS by checking for mysql.rds_kill procedure
	if !info.IsMariaDB {
		var count int
		err := m.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = 'mysql' AND ROUTINE_NAME = 'rds_kill'").Scan(&count)
		if err == nil && count > 0 {
			info.IsRDS = true
		}
	}

	m.serverInfo = &info
	return info, nil
}

func parseVersionNumber(version string) int {
	// Strip MariaDB suffix etc.
	v := version
	if idx := strings.Index(v, "-"); idx > 0 {
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return 0
	}

	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(parts[2])

	return major*10000 + minor*100 + patch
}

func (m *MySQLDB) ConnectionID(ctx context.Context) (uint64, error) {
	var id uint64
	err := m.db.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
	return id, err
}

func (m *MySQLDB) Transactions(ctx context.Context) ([]Transaction, error) {
	query := `
		SELECT ` + queryTimeoutHint + `
			p.ID, p.USER, p.HOST, COALESCE(p.DB, ''), p.COMMAND, COALESCE(p.TIME, 0),
			COALESCE(p.STATE, ''), COALESCE(p.INFO, ''),
			COALESCE(esc.DIGEST_TEXT, ''),
			COALESCE(t.trx_id, ''), COALESCE(t.trx_state, ''),
			COALESCE(t.trx_started, NOW())
		FROM information_schema.PROCESSLIST p
		LEFT JOIN information_schema.INNODB_TRX t ON p.ID = t.trx_mysql_thread_id
		LEFT JOIN performance_schema.threads pt ON pt.PROCESSLIST_ID = p.ID
		LEFT JOIN performance_schema.events_statements_current esc ON esc.THREAD_ID = pt.THREAD_ID
		WHERE t.trx_id IS NOT NULL
		ORDER BY t.trx_started ASC`

	rows, err := m.queryWithRetry(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query transactions: %w", err)
	}
	defer rows.Close()

	var txns []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.User, &t.Host, &t.DB, &t.Command, &t.Time,
			&t.State, &t.Query, &t.DigestText, &t.TrxID, &t.TrxState, &t.TrxStarted); err != nil {
			return nil, fmt.Errorf("failed to scan transaction: %w", err)
		}
		txns = append(txns, t)
	}
	return txns, rows.Err()
}

func (m *MySQLDB) LockWaits(ctx context.Context) ([]LockWait, error) {
	info, err := m.ServerInfo(ctx)
	if err != nil {
		return nil, err
	}

	if info.IsMariaDB || info.VersionNumber < 80000 {
		return m.lockWaits57(ctx)
	}
	return m.lockWaits80(ctx)
}

func (m *MySQLDB) lockWaits57(ctx context.Context) ([]LockWait, error) {
	query := `
		SELECT ` + queryTimeoutHint + `
			COALESCE(r.trx_id, ''), COALESCE(r.trx_mysql_thread_id, 0),
			COALESCE(r.trx_query, ''),
			COALESCE(res.DIGEST_TEXT, ''),
			COALESCE(rp.USER, ''), COALESCE(rp.HOST, ''),
			COALESCE(b.trx_id, ''), COALESCE(b.trx_mysql_thread_id, 0),
			COALESCE(b.trx_query, COALESCE(bes.SQL_TEXT, '')),
			COALESCE(bes.DIGEST_TEXT, ''),
			COALESCE(bp.USER, ''), COALESCE(bp.HOST, ''),
			COALESCE(bl.lock_table, ''), COALESCE(bl.lock_index, ''),
			COALESCE(bl.lock_mode, ''),
			COALESCE(r.trx_wait_started, NOW()),
			CAST(COALESCE(TIMESTAMPDIFF(MICROSECOND, r.trx_wait_started, NOW()) / 1000, 0) AS SIGNED)
		FROM information_schema.INNODB_LOCK_WAITS w
		JOIN information_schema.INNODB_TRX r ON r.trx_id = w.requesting_trx_id
		JOIN information_schema.INNODB_TRX b ON b.trx_id = w.blocking_trx_id
		JOIN information_schema.INNODB_LOCKS bl ON bl.lock_id = w.blocking_lock_id
		LEFT JOIN information_schema.PROCESSLIST rp ON rp.ID = r.trx_mysql_thread_id
		LEFT JOIN information_schema.PROCESSLIST bp ON bp.ID = b.trx_mysql_thread_id
		LEFT JOIN performance_schema.threads rt ON rt.PROCESSLIST_ID = r.trx_mysql_thread_id
		LEFT JOIN performance_schema.events_statements_current res ON res.THREAD_ID = rt.THREAD_ID
		LEFT JOIN performance_schema.threads bt ON bt.PROCESSLIST_ID = b.trx_mysql_thread_id
		LEFT JOIN performance_schema.events_statements_current bes ON bes.THREAD_ID = bt.THREAD_ID`

	return m.queryLockWaits(ctx, query)
}

func (m *MySQLDB) lockWaits80(ctx context.Context) ([]LockWait, error) {
	query := `
		SELECT ` + queryTimeoutHint + `
			COALESCE(r.trx_id, ''), COALESCE(r.trx_mysql_thread_id, 0),
			COALESCE(r.trx_query, ''),
			COALESCE(res.DIGEST_TEXT, ''),
			COALESCE(rp.USER, ''), COALESCE(rp.HOST, ''),
			COALESCE(b.trx_id, ''), COALESCE(b.trx_mysql_thread_id, 0),
			COALESCE(b.trx_query, COALESCE(bes.SQL_TEXT, '')),
			COALESCE(bes.DIGEST_TEXT, ''),
			COALESCE(bp.USER, ''), COALESCE(bp.HOST, ''),
			CONCAT(COALESCE(bl.OBJECT_SCHEMA, ''), '.', COALESCE(bl.OBJECT_NAME, '')), COALESCE(bl.INDEX_NAME, ''),
			COALESCE(bl.LOCK_MODE, ''),
			COALESCE(r.trx_wait_started, NOW()),
			CAST(COALESCE(TIMESTAMPDIFF(MICROSECOND, r.trx_wait_started, NOW()) / 1000, 0) AS SIGNED)
		FROM performance_schema.data_lock_waits w
		JOIN information_schema.INNODB_TRX r ON r.trx_id = w.REQUESTING_ENGINE_TRANSACTION_ID
		JOIN information_schema.INNODB_TRX b ON b.trx_id = w.BLOCKING_ENGINE_TRANSACTION_ID
		JOIN performance_schema.data_locks bl ON bl.ENGINE_LOCK_ID = w.BLOCKING_ENGINE_LOCK_ID
		LEFT JOIN information_schema.PROCESSLIST rp ON rp.ID = r.trx_mysql_thread_id
		LEFT JOIN information_schema.PROCESSLIST bp ON bp.ID = b.trx_mysql_thread_id
		LEFT JOIN performance_schema.threads rt ON rt.PROCESSLIST_ID = r.trx_mysql_thread_id
		LEFT JOIN performance_schema.events_statements_current res ON res.THREAD_ID = rt.THREAD_ID
		LEFT JOIN performance_schema.threads bt ON bt.PROCESSLIST_ID = b.trx_mysql_thread_id
		LEFT JOIN performance_schema.events_statements_current bes ON bes.THREAD_ID = bt.THREAD_ID`

	return m.queryLockWaits(ctx, query)
}

func (m *MySQLDB) queryLockWaits(ctx context.Context, query string) ([]LockWait, error) {
	rows, err := m.queryWithRetry(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query lock waits: %w", err)
	}
	defer rows.Close()

	var waits []LockWait
	for rows.Next() {
		var lw LockWait
		if err := rows.Scan(
			&lw.WaitingTrxID, &lw.WaitingPID, &lw.WaitingQuery,
			&lw.WaitingDigest,
			&lw.WaitingUser, &lw.WaitingHost,
			&lw.BlockingTrxID, &lw.BlockingPID, &lw.BlockingQuery,
			&lw.BlockingDigest,
			&lw.BlockingUser, &lw.BlockingHost,
			&lw.LockTable, &lw.LockIndex, &lw.LockType,
			&lw.WaitStarted, &lw.WaitDurationMs,
		); err != nil {
			return nil, fmt.Errorf("failed to scan lock wait: %w", err)
		}
		waits = append(waits, lw)
	}
	return waits, rows.Err()
}

func (m *MySQLDB) Processes(ctx context.Context) ([]Process, error) {
	query := `SELECT ` + queryTimeoutHint + ` ID, USER, HOST, COALESCE(DB, ''), COMMAND, COALESCE(TIME, 0), COALESCE(STATE, ''), COALESCE(INFO, '') FROM information_schema.PROCESSLIST ORDER BY TIME DESC`

	rows, err := m.queryWithRetry(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query processes: %w", err)
	}
	defer rows.Close()

	var procs []Process
	for rows.Next() {
		var p Process
		if err := rows.Scan(&p.ID, &p.User, &p.Host, &p.DB, &p.Command, &p.Time, &p.State, &p.Info); err != nil {
			return nil, fmt.Errorf("failed to scan process: %w", err)
		}
		procs = append(procs, p)
	}
	return procs, rows.Err()
}

// MetadataLocks returns one row per entry in
// performance_schema.metadata_locks for tables, joined to
// performance_schema.threads to pick up the matching session's
// processlist user/host/info/time so the TUI can show who holds (or
// who is waiting for) each lock without a second round-trip.
//
// The query orders by schema/name, then GRANTED-before-PENDING, then
// wait-age descending so consumers can iterate the queue oldest-first.
//
// Errors are surfaced rather than swallowed: the previous behaviour of
// silently returning nil, nil on any error hid a long-standing bug
// where the SELECT referenced a non-existent LOCK_MODE column.
func (m *MySQLDB) MetadataLocks(ctx context.Context) ([]MetadataLock, error) {
	query := `
		SELECT ` + queryTimeoutHint + `
			COALESCE(ml.OBJECT_TYPE, ''),
			COALESCE(ml.OBJECT_SCHEMA, ''),
			COALESCE(ml.OBJECT_NAME, ''),
			COALESCE(ml.LOCK_TYPE, ''),
			COALESCE(ml.LOCK_DURATION, ''),
			COALESCE(ml.LOCK_STATUS, ''),
			COALESCE(ml.OWNER_THREAD_ID, 0),
			COALESCE(t.PROCESSLIST_ID, 0),
			COALESCE(t.PROCESSLIST_USER, ''),
			COALESCE(t.PROCESSLIST_HOST, ''),
			COALESCE(t.PROCESSLIST_TIME, 0),
			COALESCE(t.PROCESSLIST_INFO, '')
		FROM performance_schema.metadata_locks ml
		LEFT JOIN performance_schema.threads t ON t.THREAD_ID = ml.OWNER_THREAD_ID
		WHERE ml.OBJECT_TYPE = 'TABLE'
		ORDER BY ml.OBJECT_SCHEMA, ml.OBJECT_NAME,
		         FIELD(ml.LOCK_STATUS, 'GRANTED', 'PENDING'),
		         t.PROCESSLIST_TIME DESC`

	rows, err := m.queryWithRetry(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("metadata locks: %w", err)
	}
	defer rows.Close()

	var locks []MetadataLock
	for rows.Next() {
		var ml MetadataLock
		if err := rows.Scan(
			&ml.ObjectType, &ml.ObjectSchema, &ml.ObjectName,
			&ml.LockType, &ml.Duration, &ml.LockStatus,
			&ml.ThreadID, &ml.PID, &ml.User, &ml.Host,
			&ml.TimeSeconds, &ml.Info,
		); err != nil {
			return nil, fmt.Errorf("failed to scan metadata lock: %w", err)
		}
		locks = append(locks, ml)
	}
	return locks, rows.Err()
}

func (m *MySQLDB) InnoDBStatus(ctx context.Context) (InnoDBStatus, error) {
	var typ, name, status string
	err := m.queryRowWithRetry(ctx, "SHOW ENGINE INNODB STATUS").Scan(&typ, &name, &status)
	if err != nil {
		return InnoDBStatus{}, fmt.Errorf("failed to get innodb status: %w", err)
	}

	return ParseInnoDBStatus(status), nil
}

func (m *MySQLDB) KillConnection(ctx context.Context, id uint64) error {
	info, err := m.ServerInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get server info: %w", err)
	}

	if info.IsRDS || info.IsAurora {
		_, err = m.db.ExecContext(ctx, "CALL mysql.rds_kill(?)", id)
	} else {
		_, err = m.db.ExecContext(ctx, fmt.Sprintf("KILL %d", id))
	}
	return err
}

// healthVitalNames are the SHOW GLOBAL STATUS variable names we pull
// every health-collector poll. Pulled together in a single WHERE-IN
// query so the cost is one round-trip per poll.
var healthVitalNames = []string{
	"Threads_running",
	"Threads_connected",
	"Innodb_buffer_pool_pages_dirty",
	"Innodb_buffer_pool_pages_total",
	"Innodb_buffer_pool_read_requests",
	"Innodb_buffer_pool_reads",
	"Aborted_clients",
}

// HealthVitals reads the cheap "is the DB OK?" gauges from
// SHOW GLOBAL STATUS plus, when probe.Role is ReplicaRoleReplica,
// the relevant SHOW REPLICA STATUS / SHOW SLAVE STATUS row.
//
// AbortedClientsDelta requires the caller to provide the prior
// snapshot's AbortedClients raw counter via priorAborted. On a
// counter reset (server restart) the delta is reported as 0 so a
// reset doesn't masquerade as an enormous spike.
func (m *MySQLDB) HealthVitals(ctx context.Context, probe ReplicaProbe, priorAborted uint64) (HealthVitals, error) {
	v := HealthVitals{Time: time.Now()}

	// One placeholder per name so the prepared form stays simple even
	// with interpolateParams=true on the DSN.
	placeholders := strings.TrimRight(strings.Repeat("?,", len(healthVitalNames)), ",")
	q := "SHOW GLOBAL STATUS WHERE Variable_name IN (" + placeholders + ")"
	args := make([]any, 0, len(healthVitalNames))
	for _, n := range healthVitalNames {
		args = append(args, n)
	}

	rows, err := m.queryWithRetry(ctx, q, args...)
	if err != nil {
		return v, fmt.Errorf("health vitals: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return v, fmt.Errorf("scan health vital: %w", err)
		}
		n, _ := strconv.ParseUint(value, 10, 64)
		switch name {
		case "Threads_running":
			v.ThreadsRunning = n
		case "Threads_connected":
			v.ThreadsConnected = n
		case "Innodb_buffer_pool_pages_dirty":
			v.InnoDBBufferPoolPagesDirty = n
		case "Innodb_buffer_pool_pages_total":
			v.InnoDBBufferPoolPagesTotal = n
		case "Innodb_buffer_pool_read_requests":
			v.InnoDBBufferPoolReadRequests = n
		case "Innodb_buffer_pool_reads":
			v.InnoDBBufferPoolReads = n
		case "Aborted_clients":
			v.AbortedClients = n
		}
	}
	if err := rows.Err(); err != nil {
		return v, fmt.Errorf("health vitals rows: %w", err)
	}

	if v.AbortedClients >= priorAborted {
		v.AbortedClientsDelta = v.AbortedClients - priorAborted
	}

	if probe.Role == ReplicaRoleReplica {
		rs, err := m.replicaStatus(ctx, probe.Dialect)
		if err == nil {
			v.Replica = rs
		}
		// Errors on replica-status are non-fatal; the rest of the
		// vitals snapshot is still useful.
	}

	return v, nil
}

// ProbeReplica detects whether the server has at least one replica
// channel and which dialect to use. Empty result → standalone.
//
// MySQL 8.0.22+ accepts both SHOW REPLICA STATUS and SHOW SLAVE STATUS;
// older MySQL and MariaDB only accept SHOW SLAVE STATUS. We try the
// modern one first and fall back on syntax errors.
func (m *MySQLDB) ProbeReplica(ctx context.Context) (ReplicaProbe, error) {
	for _, dialect := range []ReplicaDialect{ReplicaDialectReplica, ReplicaDialectSlave} {
		query := "SHOW REPLICA STATUS"
		if dialect == ReplicaDialectSlave {
			query = "SHOW SLAVE STATUS"
		}
		rows, err := m.db.QueryContext(ctx, query)
		if err != nil {
			// Syntax / privilege errors → try the other dialect.
			continue
		}
		hasRow := rows.Next()
		// Drain to avoid leaving a connection mid-result.
		_ = rows.Close()
		if hasRow {
			return ReplicaProbe{Role: ReplicaRoleReplica, Dialect: dialect}, nil
		}
		// Dialect parsed but no replica rows → standalone, but we still
		// know which dialect this server speaks.
		return ReplicaProbe{Role: ReplicaRoleStandalone, Dialect: dialect}, nil
	}
	return ReplicaProbe{Role: ReplicaRoleStandalone, Dialect: ReplicaDialectUnknown}, nil
}

// replicaStatus pulls the subset of SHOW REPLICA STATUS / SHOW SLAVE
// STATUS columns the Overview surfaces. Column count varies between
// versions, so we scan into a generic []sql.RawBytes and pick by name.
func (m *MySQLDB) replicaStatus(ctx context.Context, dialect ReplicaDialect) (*ReplicaStatus, error) {
	query := "SHOW REPLICA STATUS"
	if dialect == ReplicaDialectSlave {
		query = "SHOW SLAVE STATUS"
	}
	rows, err := m.queryWithRetry(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, nil
	}

	raw := make([]sql.RawBytes, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range raw {
		scanArgs[i] = &raw[i]
	}
	if err := rows.Scan(scanArgs...); err != nil {
		return nil, err
	}

	byName := make(map[string]string, len(cols))
	for i, c := range cols {
		byName[c] = string(raw[i])
	}

	rs := &ReplicaStatus{
		SecondsBehindSource: -1,
	}
	rs.SourceHost = pickReplicaCol(byName, "Source_Host", "Master_Host")
	rs.Channel = pickReplicaCol(byName, "Channel_Name")
	rs.IOThreadRunning = strings.EqualFold(pickReplicaCol(byName, "Replica_IO_Running", "Slave_IO_Running"), "Yes")
	rs.SQLThreadRunning = strings.EqualFold(pickReplicaCol(byName, "Replica_SQL_Running", "Slave_SQL_Running"), "Yes")
	if v := pickReplicaCol(byName, "Seconds_Behind_Source", "Seconds_Behind_Master"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			rs.SecondsBehindSource = n
		}
	}
	rs.GTIDExecuted = pickReplicaCol(byName, "Executed_Gtid_Set")
	rs.GTIDPurged = pickReplicaCol(byName, "Retrieved_Gtid_Set")
	rs.LastError = pickReplicaCol(byName, "Last_Error", "Last_SQL_Error")
	return rs, nil
}

// pickReplicaCol returns the first non-empty column value from names,
// allowing us to support both Replica_/Source_ (8.0.22+) and Slave_/
// Master_ (everything else) without two queries.
func pickReplicaCol(byName map[string]string, names ...string) string {
	for _, n := range names {
		if v, ok := byName[n]; ok && v != "" {
			return v
		}
	}
	return ""
}
