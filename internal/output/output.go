package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
)

// FormatText formats a monitor result as human-readable text.
func FormatText(w io.Writer, result monitor.Result) {
	snap := result.Snapshot

	fmt.Fprintf(w, "MySQL Lock Monitor - %s\n", snap.Time.Format("2006-01-02 15:04:05"))
	if result.Error != nil {
		fmt.Fprintf(w, "Error: %v\n", result.Error)
	}
	fmt.Fprintf(w, "Server: %s\n", snap.ServerInfo.Version)
	fmt.Fprintf(w, "Transactions: %d | Lock Waits: %d | Processes: %d\n",
		len(snap.Transactions), len(snap.LockWaits), len(snap.Processes))
	fmt.Fprintln(w, strings.Repeat("-", 60))

	if len(result.Issues) == 0 {
		fmt.Fprintln(w, "No issues detected.")
		return
	}

	fmt.Fprintf(w, "Issues (%d):\n", len(result.Issues))
	for i, issue := range result.Issues {
		severity := colorSeverity(issue.Severity)
		fmt.Fprintf(w, "  %d. [%s] %s\n", i+1, severity, issue.Title)
		fmt.Fprintf(w, "     %s\n", issue.Description)
	}

	// Lock waits detail
	if len(snap.LockWaits) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Lock Waits:")
		for _, lw := range snap.LockWaits {
			fmt.Fprintf(w, "  PID:%d [%s@%s] waiting for PID:%d [%s@%s] on %s [%s]\n",
				lw.WaitingPID, lw.WaitingUser, lw.WaitingHost,
				lw.BlockingPID, lw.BlockingUser, lw.BlockingHost,
				lw.LockTable, formatDurationMs(lw.WaitDurationMs))
		}
	}

	// Active transactions
	if len(snap.Transactions) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Active Transactions:")
		for _, trx := range snap.Transactions {
			fmt.Fprintf(w, "  TrxID:%s PID:%d %s@%s DB:%s Time:%ds State:%s\n",
				trx.TrxID, trx.ID, trx.User, trx.Host, trx.DB, trx.Time, trx.TrxState)
			if trx.Query != "" {
				fmt.Fprintf(w, "    Query: %s\n", truncate(trx.Query, 100))
			}
		}
	}
}

func colorSeverity(s detector.Severity) string {
	switch s {
	case detector.SeverityCritical:
		return "\033[31mCRITICAL\033[0m"
	case detector.SeverityWarning:
		return "\033[33mWARNING\033[0m"
	default:
		return "\033[36mINFO\033[0m"
	}
}

func formatDurationMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	totalSeconds := ms / 1000
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
