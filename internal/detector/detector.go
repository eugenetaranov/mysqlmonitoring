package detector

import (
	"fmt"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// Severity indicates the severity of a detected issue.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "INFO"
	case SeverityWarning:
		return "WARNING"
	case SeverityCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// Issue represents a detected problem.
type Issue struct {
	Detector    string
	Severity    Severity
	Title       string
	Description string
	Details     map[string]string
}

func (i Issue) String() string {
	return fmt.Sprintf("[%s] %s: %s", i.Severity, i.Detector, i.Title)
}

// Detector detects issues from a database snapshot.
type Detector interface {
	Name() string
	Detect(snapshot db.Snapshot) []Issue
}
