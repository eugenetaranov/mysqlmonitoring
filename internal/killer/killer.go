package killer

import (
	"context"
	"fmt"
	"strings"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// SafetyError is returned when a kill is refused for safety reasons.
type SafetyError struct {
	Reason string
}

func (e *SafetyError) Error() string {
	return fmt.Sprintf("refused to kill: %s", e.Reason)
}

// Killer handles killing MySQL connections with safety checks.
type Killer struct {
	database db.DB
}

// New creates a new Killer.
func New(database db.DB) *Killer {
	return &Killer{database: database}
}

// Kill kills a connection after performing safety checks.
func (k *Killer) Kill(ctx context.Context, connectionID uint64) error {
	// Safety check: don't kill ourselves
	selfID, err := k.database.ConnectionID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get own connection ID: %w", err)
	}
	if connectionID == selfID {
		return &SafetyError{Reason: "cannot kill own connection"}
	}

	// Safety check: find the process and check if it's safe to kill
	processes, err := k.database.Processes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get process list: %w", err)
	}

	var target *db.Process
	for _, p := range processes {
		if p.ID == connectionID {
			target = &p
			break
		}
	}

	if target == nil {
		return fmt.Errorf("connection %d not found", connectionID)
	}

	if err := checkSafety(target); err != nil {
		return err
	}

	return k.database.KillConnection(ctx, connectionID)
}

func checkSafety(p *db.Process) error {
	// Don't kill system users
	systemUsers := []string{"system user", "event_scheduler", "slave_worker", "slave_io", "slave_sql"}
	userLower := strings.ToLower(p.User)
	for _, su := range systemUsers {
		if userLower == su {
			return &SafetyError{Reason: fmt.Sprintf("system user '%s'", p.User)}
		}
	}

	// Don't kill replication threads
	replicationCommands := []string{"Binlog Dump", "Binlog Dump GTID"}
	for _, cmd := range replicationCommands {
		if p.Command == cmd {
			return &SafetyError{Reason: "replication thread"}
		}
	}

	// Don't kill threads running replication SQL
	replicationStates := []string{"Slave_IO_Running", "Slave_SQL_Running", "Waiting for master to send event"}
	for _, state := range replicationStates {
		if strings.Contains(p.State, state) {
			return &SafetyError{Reason: fmt.Sprintf("replication state '%s'", p.State)}
		}
	}

	return nil
}
