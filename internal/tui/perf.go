package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/eugenetaranov/mysqlmonitoring/internal/explain"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// sparkBlocks are the eight unicode block elements used to render the
// total-AAS sparkline. Index 0 is empty; index 8 is the tallest cell.
var sparkBlocks = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// classColors maps each WaitClass to a lipgloss-friendly 256-colour
// code. The TUI degrades to glyph-only legends when the terminal
// rejects ANSI colour, since lipgloss handles that itself.
var classColors = map[series.WaitClass]color.Color{
	series.WaitClassCPU:     lipgloss.Color("33"),  // blue
	series.WaitClassIO:      lipgloss.Color("214"), // orange
	series.WaitClassLock:    lipgloss.Color("196"), // red
	series.WaitClassSync:    lipgloss.Color("129"), // purple
	series.WaitClassNetwork: lipgloss.Color("38"),  // cyan
	series.WaitClassOther:   lipgloss.Color("244"), // grey
}

// sparkSamples is the maximum number of recent wait samples we keep
// in the sparkline trail. The trail re-flows on window resize.
const sparkSamples = 60

// renderSparklineHeader produces a single-line DB-load-by-wait-class
// header. The line consists of a "DB Load:" label, the sparkline
// trail of total AAS, and a same-line legend of the most recent
// per-class AAS values, colour-coded by class.
func renderSparklineHeader(width int, trail []float64, current insights.LoadBreakdown) string {
	if width <= 0 {
		width = 80
	}

	header := headerStyle.Render("DB Load:")
	legend := renderLegend(current)
	legendLen := lipgloss.Width(legend)
	labelLen := lipgloss.Width(header)

	// Reserve room for label, two spaces and the legend.
	available := width - labelLen - legendLen - 4
	if available < 4 {
		available = 4
	}

	bars := truncateTrail(trail, available)
	spark := renderSpark(bars)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(" ")
	b.WriteString(spark)
	b.WriteString("  ")
	b.WriteString(legend)
	return b.String()
}

func renderSpark(values []float64) string {
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		return strings.Repeat(string(sparkBlocks[0]), len(values))
	}
	var b strings.Builder
	for _, v := range values {
		idx := int((v/max)*float64(len(sparkBlocks)-1) + 0.5)
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkBlocks) {
			idx = len(sparkBlocks) - 1
		}
		b.WriteRune(sparkBlocks[idx])
	}
	return b.String()
}

func renderLegend(load insights.LoadBreakdown) string {
	var parts []string
	for _, c := range load.Classes {
		style := lipgloss.NewStyle().Foreground(classColors[c.Class])
		parts = append(parts, style.Render(fmt.Sprintf("%s %.2f", c.Class, c.AAS)))
	}
	parts = append(parts, dimStyle.Render(fmt.Sprintf("Σ %.2f", load.Total)))
	return strings.Join(parts, " ")
}

// truncateTrail keeps the most recent n entries of trail. When trail
// is shorter than n it is padded on the left with zeros so the
// rightmost cell remains "now".
func truncateTrail(trail []float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	if len(trail) >= n {
		return trail[len(trail)-n:]
	}
	out := make([]float64, n)
	copy(out[n-len(trail):], trail)
	return out
}

// computeTrail walks the wait series and returns the last sparkSamples
// total-AAS values, one per per-poll bin. Bins are ordered oldest →
// newest. The implementation collapses per-class WaitSamples that
// share the same Time into a single bin so the sparkline renders one
// cell per poll, not one per class.
func computeTrail(insightsRef *insights.Insights, now time.Time, window time.Duration) []float64 {
	if insightsRef == nil {
		return nil
	}
	type bin struct {
		t   time.Time
		aas float64
	}
	var bins []bin
	idx := make(map[time.Time]int)
	for _, cls := range series.AllWaitClasses {
		sink := insightsRef.Waits.Sink(cls)
		if sink == nil {
			continue
		}
		for s := range sink.Range(now, window) {
			var aas float64
			switch cls {
			case series.WaitClassCPU:
				if s.CPUTicks > 0 {
					aas = float64(s.CPUObservations) / float64(s.CPUTicks)
				}
			default:
				secs := s.Interval.Seconds()
				if secs > 0 {
					aas = float64(s.SumTimerWaitDelta) / 1e12 / secs
				}
			}
			if i, ok := idx[s.Time]; ok {
				bins[i].aas += aas
				continue
			}
			idx[s.Time] = len(bins)
			bins = append(bins, bin{t: s.Time, aas: aas})
		}
	}
	// bins are not in chronological order; sort by time.
	for i := 1; i < len(bins); i++ {
		for j := i; j > 0 && bins[j-1].t.After(bins[j].t); j-- {
			bins[j-1], bins[j] = bins[j], bins[j-1]
		}
	}
	if len(bins) > sparkSamples {
		bins = bins[len(bins)-sparkSamples:]
	}
	out := make([]float64, len(bins))
	for i, b := range bins {
		out[i] = b.aas
	}
	return out
}

// renderTopPanel is the digest table view reached by pressing 't'.
// It includes a one-line header and a footer reminding the user of
// the keybindings.
func renderTopPanel(m Model) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Top SQL"))
	b.WriteString(dimStyle.Render(fmt.Sprintf("  sort=%s  app=%s  schema=%s",
		sortKeyName(m.topSort), orDash(m.topApp), orDash(m.topSchema))))
	b.WriteString("\n\n")

	if len(m.topData) == 0 {
		b.WriteString(dimStyle.Render("  no digests yet — run for a few intervals"))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(dimStyle.Render(fmt.Sprintf(
		"  %-6s %-8s %-8s %-10s %-10s %-12s %s",
		"AAS", "Calls/s", "Calls", "Avg", "Rows ex.", "Schema", "Digest")))
	b.WriteString("\n")

	for i, s := range m.topData {
		row := fmt.Sprintf("  %-6.2f %-8.1f %-8d %-10s %-10d %-12s %s",
			s.AAS, s.CallsPerSec, s.Calls,
			formatLatencyShort(s.AvgLatency),
			s.RowsExamined,
			orDash(s.Schema),
			truncateStr(s.Text, max(20, m.width-80)),
		)
		if i == m.topCursor {
			b.WriteString(selectedStyle.Width(m.width).Render(">" + row[1:]))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func renderExplainModal(m Model) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("EXPLAIN"))
	b.WriteString("\n")

	if m.explainErr != nil {
		b.WriteString(criticalStyle.Render(fmt.Sprintf("  error: %v\n", m.explainErr)))
		return b.String()
	}
	if m.explainResult == nil {
		b.WriteString(dimStyle.Render("  (running)\n"))
		return b.String()
	}

	res := m.explainResult
	b.WriteString(dimStyle.Render(fmt.Sprintf("  digest %s\n", truncateStr(res.Digest, 32))))

	if res.Skipped {
		b.WriteString(warningStyle.Render("  skipped: " + res.SkipReason))
		b.WriteString("\n")
		return b.String()
	}

	if res.Flipped {
		b.WriteString(warningStyle.Render(fmt.Sprintf("  PLAN FLIP: %s → %s",
			truncateStr(res.PriorHash, 8), truncateStr(res.PlanHash, 8))))
		b.WriteString("\n")
	}

	if len(res.RedFlags) > 0 {
		b.WriteString(headerStyle.Render("  Red flags:"))
		b.WriteString("\n")
		for _, f := range res.RedFlags {
			b.WriteString("    ")
			b.WriteString(redFlagBadge(f.Kind))
			b.WriteString(" ")
			b.WriteString(f.Detail)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(headerStyle.Render("  Plan:"))
	b.WriteString("\n")
	for _, line := range strings.Split(res.PlanText, "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func redFlagBadge(kind string) string {
	switch kind {
	case explain.FlagFullScan:
		return criticalStyle.Render("[FULL SCAN]")
	case explain.FlagFilesort:
		return warningStyle.Render("[FILESORT]")
	case explain.FlagTempTable:
		return warningStyle.Render("[TEMP TABLE]")
	case explain.FlagBigScan:
		return warningStyle.Render("[BIG SCAN RATIO]")
	default:
		return dimStyle.Render("[" + kind + "]")
	}
}

func formatLatencyShort(d time.Duration) string {
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

func sortKeyName(k insights.SortKey) string {
	switch k {
	case insights.SortByCalls:
		return "calls"
	case insights.SortByAvgLatency:
		return "latency"
	case insights.SortByRowsExamined:
		return "rows-examined"
	default:
		return "aas"
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
