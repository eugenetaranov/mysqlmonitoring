package db

import (
	"context"
	"time"
)

// Transaction represents a running transaction.
type Transaction struct {
	ID        uint64
	User      string
	Host      string
	DB        string
	Command   string
	Time      int64 // seconds
	State     string
	Query      string
	DigestText string
	TrxID      string
	TrxState  string
	TrxStarted time.Time
}

// LockWait represents a lock wait relationship.
type LockWait struct {
	WaitingTrxID   string
	WaitingPID     uint64
	WaitingQuery   string
	WaitingDigest  string
	WaitingUser    string
	WaitingHost    string
	BlockingTrxID  string
	BlockingPID    uint64
	BlockingQuery  string
	BlockingDigest string
	BlockingUser   string
	BlockingHost   string
	LockTable      string
	LockIndex      string
	LockType       string
	WaitStarted    time.Time
	WaitDurationMs int64
}

// Process represents a MySQL process list entry.
type Process struct {
	ID      uint64
	User    string
	Host    string
	DB      string
	Command string
	Time    int64
	State   string
	Info    string
}

// MetadataLock represents a metadata lock.
type MetadataLock struct {
	ThreadID  uint64
	LockType  string
	Duration  string
	LockMode  string
	ObjectType string
	ObjectSchema string
	ObjectName string
}

// DeadlockInfo holds parsed deadlock information from INNODB STATUS.
type DeadlockInfo struct {
	Timestamp    string
	Transactions []DeadlockTransaction
}

// DeadlockTransaction represents one side of a deadlock.
type DeadlockTransaction struct {
	TrxID      string
	ThreadID   uint64
	Query      string
	User       string
	Host       string
	LockMode   string
	TableName  string
	IndexName  string
	WaitingFor string
}

// InnoDBStatus holds parsed SHOW ENGINE INNODB STATUS output.
type InnoDBStatus struct {
	Raw               string
	LatestDeadlock    *DeadlockInfo
	TransactionCount  int
	ActiveTrxCount    int
	LockWaitCount     int
	HistoryListLength uint64 // undo log entries waiting on purge
}

// ServerInfo holds MySQL server metadata.
type ServerInfo struct {
	Version       string
	VersionNumber int // e.g. 80032
	IsMariaDB     bool
	IsAurora      bool
	IsRDS         bool // AWS RDS (non-Aurora) — use mysql.rds_kill()
}

// HealthVitals is one snapshot of the cheap "is the DB OK?" gauges we
// pull from SHOW GLOBAL STATUS each poll. All fields are absolute
// values from the variable of the same name except AbortedClientsDelta,
// which is the increase since the previous snapshot.
//
// Replica is nil on standalone servers — Probe detects role once.
type HealthVitals struct {
	Time                          time.Time
	ThreadsRunning                uint64
	ThreadsConnected              uint64
	InnoDBBufferPoolPagesDirty    uint64
	InnoDBBufferPoolPagesTotal    uint64
	InnoDBBufferPoolReadRequests  uint64
	InnoDBBufferPoolReads         uint64
	AbortedClients                uint64 // raw counter
	AbortedClientsDelta           uint64 // delta since prior snapshot
	Replica                       *ReplicaStatus
}

// ReplicaStatus is the subset of SHOW REPLICA STATUS / SHOW SLAVE STATUS
// fields the Overview surfaces.
type ReplicaStatus struct {
	SourceHost          string
	Channel             string
	IOThreadRunning     bool
	SQLThreadRunning    bool
	SecondsBehindSource int64 // -1 when NULL on the wire
	GTIDExecuted        string
	GTIDPurged          string
	LastError           string
}

// ReplicaDialect distinguishes the SQL syntax used to query replica
// status; servers since MySQL 8.0.22 prefer SHOW REPLICA STATUS, while
// MariaDB and older MySQL only accept SHOW SLAVE STATUS.
type ReplicaDialect uint8

const (
	ReplicaDialectUnknown ReplicaDialect = iota
	ReplicaDialectReplica                // SHOW REPLICA STATUS
	ReplicaDialectSlave                  // SHOW SLAVE STATUS
)

// ReplicaProbe is the cached result of role detection, populated once
// at startup. Standalone servers carry Role=Standalone.
type ReplicaProbe struct {
	Role    ReplicaRole
	Dialect ReplicaDialect
}

// ReplicaRole identifies whether the server has any replica streams.
type ReplicaRole uint8

const (
	ReplicaRoleUnknown ReplicaRole = iota
	ReplicaRoleStandalone
	ReplicaRoleReplica
)

// Snapshot represents a point-in-time view of database state.
type Snapshot struct {
	Time          time.Time
	ServerInfo    ServerInfo
	Transactions  []Transaction
	LockWaits     []LockWait
	Processes     []Process
	MetadataLocks []MetadataLock
	InnoDBStatus  InnoDBStatus
}

// DB is the interface for all database operations.
type DB interface {
	// ServerInfo returns server version and variant info.
	ServerInfo(ctx context.Context) (ServerInfo, error)

	// Transactions returns currently running transactions.
	Transactions(ctx context.Context) ([]Transaction, error)

	// LockWaits returns current lock wait relationships.
	LockWaits(ctx context.Context) ([]LockWait, error)

	// Processes returns the process list.
	Processes(ctx context.Context) ([]Process, error)

	// MetadataLocks returns metadata locks (MySQL 5.7+ with performance_schema).
	MetadataLocks(ctx context.Context) ([]MetadataLock, error)

	// InnoDBStatus returns parsed SHOW ENGINE INNODB STATUS output.
	InnoDBStatus(ctx context.Context) (InnoDBStatus, error)

	// HealthVitals reads the cheap "is the DB OK?" gauges from
	// SHOW GLOBAL STATUS plus, when probe.Role is ReplicaRoleReplica,
	// the relevant SHOW REPLICA STATUS / SHOW SLAVE STATUS row.
	HealthVitals(ctx context.Context, probe ReplicaProbe, priorAborted uint64) (HealthVitals, error)

	// ProbeReplica detects whether this server has any replica
	// channel and which dialect (REPLICA vs SLAVE) to use. Cached
	// once at startup by the caller.
	ProbeReplica(ctx context.Context) (ReplicaProbe, error)

	// KillConnection kills a connection by ID.
	KillConnection(ctx context.Context, id uint64) error

	// ConnectionID returns the current connection ID.
	ConnectionID(ctx context.Context) (uint64, error)

	// Close closes the database connection.
	Close() error
}
