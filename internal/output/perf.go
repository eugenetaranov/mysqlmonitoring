package output

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// digestTextMaxRunes is the per-row truncation length for digest
// text in the top table. Long digests get a trailing ellipsis.
const digestTextMaxRunes = 80

// FormatTop writes a tab-aligned summary of digest summaries.
func FormatTop(w io.Writer, summaries []insights.DigestSummary) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AAS\tCalls/s\tCalls\tAvg\tRows ex.\tSchema\tDigest")
	for _, s := range summaries {
		fmt.Fprintf(tw, "%.2f\t%.1f\t%d\t%s\t%d\t%s\t%s\n",
			s.AAS,
			s.CallsPerSec,
			s.Calls,
			formatLatency(s.AvgLatency),
			s.RowsExamined,
			truncateOrDash(s.Schema),
			truncateRunes(s.Text, digestTextMaxRunes),
		)
	}
	tw.Flush()
}

// FormatTopJSON writes one NDJSON object per summary.
func FormatTopJSON(w io.Writer, summaries []insights.DigestSummary) error {
	enc := json.NewEncoder(w)
	for _, s := range summaries {
		obj := map[string]any{
			"schema":              s.Schema,
			"digest":              s.Digest,
			"digest_text":         s.Text,
			"aas":                 s.AAS,
			"calls":               s.Calls,
			"calls_per_sec":       s.CallsPerSec,
			"avg_latency_ms":      s.AvgLatency.Milliseconds(),
			"rows_examined":       s.RowsExamined,
			"rows_examined_per_call": s.RowsExamPerCall,
			"no_index_used_calls": s.NoIndexUsedCalls,
		}
		if err := enc.Encode(obj); err != nil {
			return err
		}
	}
	return nil
}

// FormatLoad writes a per-class AAS table plus a total row.
func FormatLoad(w io.Writer, load insights.LoadBreakdown) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Class\tAAS")
	for _, c := range load.Classes {
		fmt.Fprintf(tw, "%s\t%.3f\n", className(c.Class), c.AAS)
	}
	fmt.Fprintf(tw, "%s\t%.3f\n", "total", load.Total)
	tw.Flush()
}

// FormatLoadJSON writes a single JSON object describing the
// per-class AAS breakdown and the total. Window is in seconds.
func FormatLoadJSON(w io.Writer, load insights.LoadBreakdown) error {
	classes := make(map[string]float64, len(load.Classes))
	for _, c := range load.Classes {
		classes[className(c.Class)] = c.AAS
	}
	enc := json.NewEncoder(w)
	return enc.Encode(map[string]any{
		"window_seconds": load.Window.Seconds(),
		"classes":        classes,
		"total":          load.Total,
	})
}

func className(c series.WaitClass) string { return c.String() }

func formatLatency(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d >= time.Microsecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	default:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func truncateOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
