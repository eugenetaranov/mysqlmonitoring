package detector

import (
	"fmt"
	"strings"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// DeadlockDetector detects deadlocks from InnoDB status.
type DeadlockDetector struct {
	// LastSeenTimestamp tracks the last deadlock we reported to avoid duplicates.
	LastSeenTimestamp string
}

// NewDeadlockDetector creates a new deadlock detector.
func NewDeadlockDetector() *DeadlockDetector {
	return &DeadlockDetector{}
}

func (d *DeadlockDetector) Name() string {
	return "deadlock"
}

func (d *DeadlockDetector) Detect(snapshot db.Snapshot) []Issue {
	dl := snapshot.InnoDBStatus.LatestDeadlock
	if dl == nil {
		return nil
	}

	// Skip if we already reported this deadlock AND none of its
	// participants are still alive in the process list.
	if dl.Timestamp == d.LastSeenTimestamp {
		aliveProcs := make(map[uint64]bool)
		for _, p := range snapshot.Processes {
			aliveProcs[p.ID] = true
		}
		anyAlive := false
		for _, trx := range dl.Transactions {
			if aliveProcs[trx.ThreadID] {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return nil
		}
	}
	d.LastSeenTimestamp = dl.Timestamp

	// Build lookup for live transaction digest by PID
	trxByPID := make(map[uint64]db.Transaction)
	for _, t := range snapshot.Transactions {
		trxByPID[t.ID] = t
	}

	var participants []string
	for _, trx := range dl.Transactions {
		info := fmt.Sprintf("PID:%d", trx.ThreadID)
		if trx.User != "" || trx.Host != "" {
			info += fmt.Sprintf("(%s@%s)", trx.User, trx.Host)
		}
		if trx.TableName != "" {
			info += " " + trx.TableName
		}
		// Prefer digest from live transaction data over raw InnoDB status query
		queryDisplay := trx.Query
		if liveTrx, ok := trxByPID[trx.ThreadID]; ok && liveTrx.DigestText != "" {
			queryDisplay = liveTrx.DigestText
		}
		if queryDisplay != "" {
			info += fmt.Sprintf(" [%s]", truncateQuery(queryDisplay, 40))
		}
		participants = append(participants, info)
	}

	details := map[string]string{
		"timestamp":    dl.Timestamp,
		"participants": fmt.Sprintf("%d", len(dl.Transactions)),
	}
	for i, trx := range dl.Transactions {
		prefix := fmt.Sprintf("trx%d_", i+1)
		details[prefix+"id"] = trx.TrxID
		details[prefix+"thread_id"] = fmt.Sprintf("%d", trx.ThreadID)
		details[prefix+"query"] = trx.Query
		details[prefix+"user"] = trx.User
		details[prefix+"host"] = trx.Host
		details[prefix+"table"] = trx.TableName
	}

	return []Issue{
		{
			Detector:    d.Name(),
			Severity:    SeverityCritical,
			Title:       fmt.Sprintf("Deadlock detected (%d transactions)", len(dl.Transactions)),
			Description: fmt.Sprintf("Deadlock at %s: %s", dl.Timestamp, strings.Join(participants, " <-> ")),
			Details:     details,
		},
	}
}
