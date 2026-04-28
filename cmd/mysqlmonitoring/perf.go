package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/eugenetaranov/mysqlmonitoring/internal/collector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/output"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
)

// perfFlags holds all flags shared by the perf subcommands.
type perfFlags struct {
	intervalSeconds int
	limit           int
	sort            string
	app             string
	schema          string
	jsonOutput      bool
}

func registerPerfCommands(root *cobra.Command, f *flags) {
	var p perfFlags

	topCmd := &cobra.Command{
		Use:   "top",
		Short: "Show top SQL by AAS / calls / rows examined for the recent interval",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTop(*f, p, cmd)
		},
	}
	topCmd.Flags().IntVar(&p.intervalSeconds, "interval", 10, "Seconds between the two diff polls")
	topCmd.Flags().IntVar(&p.limit, "limit", 20, "Maximum rows to print")
	topCmd.Flags().StringVar(&p.sort, "sort", "aas", "Sort key: aas, calls, latency, rows-examined")
	topCmd.Flags().StringVar(&p.app, "app", "", "Filter to digests with sessions tagged with this app")
	topCmd.Flags().StringVar(&p.schema, "schema", "", "Filter to digests in this schema")
	topCmd.Flags().BoolVar(&p.jsonOutput, "json", false, "Emit one NDJSON object per row instead of the table")

	var lp perfFlags
	loadCmd := &cobra.Command{
		Use:   "load",
		Short: "Show DB load broken down by wait class for the recent interval",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLoad(*f, lp, cmd)
		},
	}
	loadCmd.Flags().IntVar(&lp.intervalSeconds, "interval", 10, "Seconds between the two diff polls")
	loadCmd.Flags().StringVar(&lp.app, "app", "", "(reserved) filter sessions sampled for CPU AAS to this app")
	loadCmd.Flags().BoolVar(&lp.jsonOutput, "json", false, "Emit a single JSON object instead of the table")

	root.AddCommand(topCmd, loadCmd)
}

// runTop performs the two-poll diff for digest stats and prints the
// resulting top-SQL ranking. It bypasses the long-running insights
// orchestrator: a fresh registry is built for the duration of the
// command and discarded on exit.
func runTop(f flags, p perfFlags, cmd *cobra.Command) error {
	if p.intervalSeconds < 1 {
		return fmt.Errorf("--interval must be >= 1 second")
	}

	database, err := connect(f, cmd)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer database.Close()

	ctx := context.Background()
	caps, err := database.ProbeCapabilities(ctx)
	if err != nil {
		return fmt.Errorf("probe perf-insights: %w", err)
	}
	if !caps.DigestAvailable {
		printWarnings(caps)
		return fmt.Errorf("digest sampling unavailable; cannot compute top SQL")
	}

	interval := time.Duration(p.intervalSeconds) * time.Second
	registry, sessions, err := pollDigestsTwice(ctx, database, interval)
	if err != nil {
		return err
	}

	summaries := insights.TopSQL(registry, sessions, time.Now(), insights.TopSQLOptions{
		Window: 2 * interval,
		Sort:   insights.ParseSortKey(p.sort),
		Limit:  p.limit,
		App:    p.app,
		Schema: p.schema,
	})

	if p.jsonOutput {
		return output.FormatTopJSON(os.Stdout, summaries)
	}
	output.FormatTop(os.Stdout, summaries)
	return nil
}

// pollDigestsTwice runs DigestCollector through two polls separated
// by interval. The first poll seeds; the second emits the deltas
// that will be summarised. CurrentStatements is sampled once to
// populate the session sink so the --app filter has data to match.
func pollDigestsTwice(ctx context.Context, src db.PerfInsightsDB, interval time.Duration) (*series.Registry, *series.RingSink[series.SessionSample], error) {
	reg := series.NewRegistry(series.RegistryConfig{MaxDigests: 4096, SampleCapacity: 4})
	sessions := series.NewRingSink[series.SessionSample](2048)

	dc := collector.NewDigestCollector(src, reg)

	// First poll seeds the digest baseline.
	if _, err := dc.Poll(ctx, time.Now()); err != nil {
		return nil, nil, fmt.Errorf("first poll: %w", err)
	}

	// Drop into a sample tick to populate session app tags.
	stmts, err := src.CurrentStatements(ctx)
	if err == nil {
		now := time.Now()
		for _, s := range stmts {
			if !s.Executing {
				continue
			}
			sessions.Append(series.SessionSample{
				Time:          now,
				ProcesslistID: s.ProcesslistID,
				Digest:        s.Digest,
				Schema:        s.Schema,
				AppTag:        collector.ResolveAppTag(s),
				EventName:     s.CurrentWait,
				Executing:     true,
			})
		}
	}

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(interval):
	}

	if _, err := dc.Poll(ctx, time.Now()); err != nil {
		return nil, nil, fmt.Errorf("second poll: %w", err)
	}
	return reg, sessions, nil
}

// runLoad performs two wait polls and a few CPU samples between them
// to produce a single load breakdown.
func runLoad(f flags, p perfFlags, cmd *cobra.Command) error {
	if p.intervalSeconds < 1 {
		return fmt.Errorf("--interval must be >= 1 second")
	}

	database, err := connect(f, cmd)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer database.Close()

	ctx := context.Background()
	caps, err := database.ProbeCapabilities(ctx)
	if err != nil {
		return fmt.Errorf("probe perf-insights: %w", err)
	}
	if !caps.WaitsAvailable {
		printWarnings(caps)
		return fmt.Errorf("wait sampling unavailable; cannot compute load")
	}

	interval := time.Duration(p.intervalSeconds) * time.Second
	w := collector.NewWaitSeries(4)
	wc := collector.NewWaitCollector(database, w)
	cpu := collector.NewCPUSampler(database, w, nil, time.Now())

	if _, err := wc.Poll(ctx, time.Now()); err != nil {
		return fmt.Errorf("first wait poll: %w", err)
	}

	// Sample CPU at ~1 Hz for the interval.
	stop := time.Now().Add(interval)
	for time.Now().Before(stop) {
		_ = cpu.Sample(ctx, time.Now())
		time.Sleep(time.Second)
	}
	cpu.Flush(time.Now())

	end := time.Now()
	if _, err := wc.Poll(ctx, end); err != nil {
		return fmt.Errorf("second wait poll: %w", err)
	}

	load := insights.Load(w, end, 2*interval)

	if p.jsonOutput {
		return output.FormatLoadJSON(os.Stdout, load)
	}
	output.FormatLoad(os.Stdout, load)
	return nil
}

func printWarnings(caps db.PerfCapabilities) {
	for _, w := range caps.Warnings {
		fmt.Fprintln(os.Stderr, "perf-insights:", w)
	}
}
