package output

import (
	"encoding/json"
	"io"

	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
)

// JSONOutput represents the JSON output structure.
type JSONOutput struct {
	Timestamp    string      `json:"timestamp"`
	Server       string      `json:"server"`
	Transactions int         `json:"transactions"`
	LockWaits    int         `json:"lock_waits"`
	Processes    int         `json:"processes"`
	MaxSeverity  string      `json:"max_severity"`
	Issues       []JSONIssue `json:"issues"`
}

// JSONIssue represents a single issue in JSON output.
type JSONIssue struct {
	Detector    string            `json:"detector"`
	Severity    string            `json:"severity"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Details     map[string]string `json:"details,omitempty"`
}

// FormatJSON writes the monitor result as JSON.
func FormatJSON(w io.Writer, result monitor.Result) error {
	snap := result.Snapshot

	output := JSONOutput{
		Timestamp:    snap.Time.Format("2006-01-02T15:04:05Z"),
		Server:       snap.ServerInfo.Version,
		Transactions: len(snap.Transactions),
		LockWaits:    len(snap.LockWaits),
		Processes:    len(snap.Processes),
		MaxSeverity:  result.MaxSeverity().String(),
	}

	for _, issue := range result.Issues {
		output.Issues = append(output.Issues, JSONIssue{
			Detector:    issue.Detector,
			Severity:    issue.Severity.String(),
			Title:       issue.Title,
			Description: issue.Description,
			Details:     issue.Details,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}
