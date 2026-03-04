package detector

import (
	"fmt"
	"strings"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// DDLConflictDetector detects DDL statements blocked by or blocking DML.
type DDLConflictDetector struct {
	WaitThreshold time.Duration
}

// NewDDLConflictDetector creates a DDL conflict detector.
func NewDDLConflictDetector(waitThreshold time.Duration) *DDLConflictDetector {
	return &DDLConflictDetector{WaitThreshold: waitThreshold}
}

func (d *DDLConflictDetector) Name() string {
	return "ddl_conflict"
}

func (d *DDLConflictDetector) Detect(snapshot db.Snapshot) []Issue {
	var issues []Issue

	// Check for DDL processes that are waiting for metadata locks
	ddlProcesses := findDDLProcesses(snapshot.Processes)
	waitingProcesses := findWaitingProcesses(snapshot.Processes)

	for _, ddl := range ddlProcesses {
		if ddl.State == "Waiting for table metadata lock" {
			severity := SeverityWarning
			if time.Duration(ddl.Time)*time.Second >= d.WaitThreshold {
				severity = SeverityCritical
			}

			// Find what might be blocking this DDL
			blockers := findPotentialBlockers(ddl, snapshot.Transactions, snapshot.MetadataLocks)

			issues = append(issues, Issue{
				Detector: d.Name(),
				Severity: severity,
				Title:    "DDL blocked by metadata lock",
				Description: fmt.Sprintf(
					"DDL on %s by %s@%s waiting for metadata lock for %ds. Query: %s. Potential blockers: %s",
					ddl.DB, ddl.User, ddl.Host, ddl.Time, truncateQuery(ddl.Info, 80), blockers,
				),
				Details: map[string]string{
					"ddl_pid":      fmt.Sprintf("%d", ddl.ID),
					"user":         ddl.User,
					"host":         ddl.Host,
					"database":     ddl.DB,
					"wait_seconds": fmt.Sprintf("%d", ddl.Time),
					"query":        ddl.Info,
					"blockers":     blockers,
				},
			})
		}
	}

	// Check for DML processes blocked by DDL (waiting for metadata lock while DDL is running)
	for _, proc := range waitingProcesses {
		if proc.State == "Waiting for table metadata lock" && !isDDL(proc.Info) {
			// Check if a DDL is running on the same table
			for _, ddl := range ddlProcesses {
				if ddl.ID == proc.ID {
					continue
				}
				if sameTable(ddl, proc) {
					severity := SeverityWarning
					if time.Duration(proc.Time)*time.Second >= d.WaitThreshold {
						severity = SeverityCritical
					}

					issues = append(issues, Issue{
						Detector: d.Name(),
						Severity: severity,
						Title:    "DML blocked by DDL",
						Description: fmt.Sprintf(
							"DML by %s@%s blocked by DDL (PID:%d) for %ds. Query: %s",
							proc.User, proc.Host, ddl.ID, proc.Time, truncateQuery(proc.Info, 80),
						),
						Details: map[string]string{
							"blocked_pid":  fmt.Sprintf("%d", proc.ID),
							"ddl_pid":      fmt.Sprintf("%d", ddl.ID),
							"user":         proc.User,
							"wait_seconds": fmt.Sprintf("%d", proc.Time),
							"query":        proc.Info,
						},
					})
				}
			}
		}
	}

	return issues
}

func isDDL(query string) bool {
	q := strings.TrimSpace(strings.ToUpper(query))
	ddlPrefixes := []string{"ALTER ", "CREATE ", "DROP ", "RENAME ", "TRUNCATE "}
	for _, prefix := range ddlPrefixes {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}

func findDDLProcesses(processes []db.Process) []db.Process {
	var result []db.Process
	for _, p := range processes {
		if isDDL(p.Info) {
			result = append(result, p)
		}
	}
	return result
}

func findWaitingProcesses(processes []db.Process) []db.Process {
	var result []db.Process
	for _, p := range processes {
		if strings.Contains(p.State, "Waiting") {
			result = append(result, p)
		}
	}
	return result
}

func findPotentialBlockers(ddl db.Process, txns []db.Transaction, mdlocks []db.MetadataLock) string {
	var blockerIDs []string

	// Active transactions on the same database could be holding metadata locks
	for _, trx := range txns {
		if trx.DB == ddl.DB && trx.ID != ddl.ID {
			blockerIDs = append(blockerIDs, fmt.Sprintf("PID:%d(%s@%s)", trx.ID, trx.User, trx.Host))
		}
	}

	if len(blockerIDs) == 0 {
		return "unknown"
	}
	return strings.Join(blockerIDs, ", ")
}

func sameTable(a, b db.Process) bool {
	return a.DB != "" && a.DB == b.DB
}
