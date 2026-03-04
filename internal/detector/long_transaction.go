package detector

import (
	"fmt"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// LongTransactionDetector detects transactions running longer than a threshold.
type LongTransactionDetector struct {
	WarningThreshold  time.Duration
	CriticalThreshold time.Duration
}

// NewLongTransactionDetector creates a detector with the given thresholds.
func NewLongTransactionDetector(warning, critical time.Duration) *LongTransactionDetector {
	return &LongTransactionDetector{
		WarningThreshold:  warning,
		CriticalThreshold: critical,
	}
}

func (d *LongTransactionDetector) Name() string {
	return "long_transaction"
}

func (d *LongTransactionDetector) Detect(snapshot db.Snapshot) []Issue {
	var issues []Issue

	for _, trx := range snapshot.Transactions {
		if trx.TrxStarted.IsZero() {
			continue
		}

		duration := snapshot.Time.Sub(trx.TrxStarted)
		if duration < d.WarningThreshold {
			continue
		}

		severity := SeverityWarning
		if duration >= d.CriticalThreshold {
			severity = SeverityCritical
		}

		queryDisplay := trx.DigestText
		if queryDisplay == "" {
			queryDisplay = trx.Query
		}

		issues = append(issues, Issue{
			Detector: d.Name(),
			Severity: severity,
			Title:    fmt.Sprintf("Long-running transaction (%s)", formatDuration(duration)),
			Description: fmt.Sprintf(
				"Transaction %s by %s@%s has been running for %s. Query: %s",
				trx.TrxID, trx.User, trx.Host, formatDuration(duration), truncateQuery(queryDisplay, 100),
			),
			Details: map[string]string{
				"trx_id":    trx.TrxID,
				"thread_id": fmt.Sprintf("%d", trx.ID),
				"user":      trx.User,
				"host":      trx.Host,
				"database":  trx.DB,
				"duration":  formatDuration(duration),
				"state":     trx.TrxState,
				"query":     queryDisplay,
			},
		})
	}

	return issues
}

func formatDuration(d time.Duration) string {
	totalSeconds := int(d.Seconds())
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds", totalSeconds)
	}
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

func truncateQuery(q string, maxLen int) string {
	if len(q) <= maxLen {
		return q
	}
	return q[:maxLen] + "..."
}
