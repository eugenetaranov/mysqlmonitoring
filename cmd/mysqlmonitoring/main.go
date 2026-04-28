package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/explain"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/killer"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/eugenetaranov/mysqlmonitoring/internal/output"
	"github.com/eugenetaranov/mysqlmonitoring/internal/tui"

)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type flags struct {
	dsn                string
	host               string
	port               int
	user               string
	password           string
	database           string
	defaultsFile       string
	interval           int
	lockWaitThreshold  int
	longQueryThreshold int
	outputFormat       string
	logFile            string

	// Perf-insights flags. Defaults match insights.DefaultConfig().
	enablePerfInsights  bool
	perfInterval        int // seconds
	perfWindow          int // seconds
	perfMaxDigests      int
	perfCPUSampleMillis int
}

func main() {
	var f flags

	rootCmd := &cobra.Command{
		Use:     "mysqlmonitoring",
		Short:   "MySQL lock monitor - detect lock contention, long transactions, and deadlocks",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
	}

	// Common flags
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&f.dsn, "dsn", "", "MySQL DSN (e.g. user:pass@host:port/db)")
	pf.StringVar(&f.host, "host", "localhost", "MySQL host")
	pf.IntVar(&f.port, "port", 3306, "MySQL port")
	pf.StringVar(&f.user, "user", "root", "MySQL user")
	pf.StringVar(&f.password, "password", "", "MySQL password")
	pf.StringVar(&f.database, "database", "", "MySQL database")
	pf.StringVar(&f.defaultsFile, "defaults-file", "", "Path to .my.cnf defaults file (default: ~/.my.cnf)")

	monitorCmd := &cobra.Command{
		Use:   "monitor",
		Short: "Continuously monitor for lock issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("output") {
				f.outputFormat = "tui"
			}
			return runMonitor(f, cmd)
		},
	}
	monitorCmd.Flags().IntVar(&f.interval, "interval", 2, "Poll interval in seconds")
	monitorCmd.Flags().IntVar(&f.lockWaitThreshold, "lock-wait-threshold", 10, "Lock wait warning threshold in seconds")
	monitorCmd.Flags().IntVar(&f.longQueryThreshold, "long-query-threshold", 30, "Long query warning threshold in seconds")
	monitorCmd.Flags().StringVar(&f.outputFormat, "output", "tui", "Output format: tui, text, json")
	monitorCmd.Flags().StringVar(&f.logFile, "log-file", "", "Append JSON log of each snapshot to this file (default: ~/.mysqlmonitoring/<host>_<timestamp>.log; pass empty string to disable)")
	monitorCmd.Flags().BoolVar(&f.enablePerfInsights, "enable-perf-insights", false, "Collect query digests, wait events and CPU AAS into in-memory series")
	monitorCmd.Flags().IntVar(&f.perfInterval, "perf-interval", 10, "Perf-insights poll interval in seconds")
	monitorCmd.Flags().IntVar(&f.perfWindow, "perf-window", 3600, "Perf-insights in-memory retention window in seconds")
	monitorCmd.Flags().IntVar(&f.perfMaxDigests, "perf-max-digests", 2000, "Maximum number of distinct digests held in memory")
	monitorCmd.Flags().IntVar(&f.perfCPUSampleMillis, "perf-cpu-sample-ms", 1000, "CPU AAS sampling interval in milliseconds")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "One-shot status check (exit code: 0=ok, 1=warning, 2=critical)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("output") {
				f.outputFormat = "text"
			}
			return runStatus(f, cmd)
		},
	}
	statusCmd.Flags().IntVar(&f.lockWaitThreshold, "lock-wait-threshold", 10, "Lock wait warning threshold in seconds")
	statusCmd.Flags().IntVar(&f.longQueryThreshold, "long-query-threshold", 30, "Long query warning threshold in seconds")
	statusCmd.Flags().StringVar(&f.outputFormat, "output", "text", "Output format: text, json")

	killCmd := &cobra.Command{
		Use:   "kill <connection_id>",
		Short: "Kill a MySQL connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKill(f, cmd, args[0])
		},
	}

	rootCmd.AddCommand(monitorCmd, statusCmd, killCmd)
	registerPerfCommands(rootCmd, &f)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func buildDSN(f flags, cmd *cobra.Command) string {
	if f.dsn != "" {
		return f.dsn
	}

	// Read .my.cnf defaults
	cnfPath := f.defaultsFile
	if cnfPath == "" {
		cnfPath = db.DefaultMyCnfPath()
	}
	cnf, _ := db.ReadMyCnf(cnfPath)

	// Start from .my.cnf values, then override with explicit CLI flags
	cfg := cnf

	// Apply hardcoded defaults for fields not set by .my.cnf
	if cfg.Host == "" {
		cfg.Host = "localhost"
	}
	if cfg.Port == 0 {
		cfg.Port = 3306
	}
	if cfg.User == "" {
		cfg.User = "root"
	}

	// Explicit CLI flags override .my.cnf
	if cmd.Flags().Changed("host") {
		cfg.Host = f.host
	}
	if cmd.Flags().Changed("port") {
		cfg.Port = f.port
	}
	if cmd.Flags().Changed("user") {
		cfg.User = f.user
	}
	if cmd.Flags().Changed("password") {
		cfg.Password = f.password
	}
	if cmd.Flags().Changed("database") {
		cfg.Database = f.database
	}

	return db.BuildDSN(cfg)
}

func connect(f flags, cmd *cobra.Command) (*db.MySQLDB, error) {
	dsn := buildDSN(f, cmd)
	return db.NewMySQL(dsn)
}

func buildConfig(f flags) monitor.Config {
	cfg := monitor.DefaultConfig()
	if f.interval > 0 {
		cfg.Interval = secondsToDuration(f.interval)
	}
	if f.lockWaitThreshold > 0 {
		cfg.LockWaitThreshold = secondsToDuration(f.lockWaitThreshold)
	}
	if f.longQueryThreshold > 0 {
		cfg.LongQueryThreshold = secondsToDuration(f.longQueryThreshold)
		cfg.CriticalTrxThreshold = secondsToDuration(f.longQueryThreshold * 10)
	}
	return cfg
}

// resolvedHost returns the host that will be used for the connection,
// taking the explicit --host flag, --dsn, or .my.cnf into account.
func resolvedHost(f flags, cmd *cobra.Command) string {
	if cmd.Flags().Changed("host") && f.host != "" {
		return f.host
	}
	if f.dsn != "" {
		// Best-effort host extraction from DSN forms like
		// "user:pass@tcp(host:port)/db" or "mysql://user:pass@host:port/db".
		s := f.dsn
		if i := strings.Index(s, "@tcp("); i >= 0 {
			rest := s[i+len("@tcp("):]
			if j := strings.IndexAny(rest, ":)"); j >= 0 {
				if h := strings.TrimSpace(rest[:j]); h != "" {
					return h
				}
			}
		}
		if strings.HasPrefix(s, "mysql://") {
			rest := strings.TrimPrefix(s, "mysql://")
			if i := strings.LastIndex(rest, "@"); i >= 0 {
				rest = rest[i+1:]
			}
			if i := strings.IndexAny(rest, ":/"); i >= 0 {
				rest = rest[:i]
			}
			if rest != "" {
				return rest
			}
		}
	}
	cnfPath := f.defaultsFile
	if cnfPath == "" {
		cnfPath = db.DefaultMyCnfPath()
	}
	if cnf, err := db.ReadMyCnf(cnfPath); err == nil && cnf.Host != "" {
		return cnf.Host
	}
	if f.host != "" {
		return f.host
	}
	return "localhost"
}

// sessionLogFile builds the per-session log path
// ~/.mysqlmonitoring/<sanitized-host>_<YYYY-MM-DD_HHMMSS>.log.
func sessionLogFile(host string, now time.Time) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	name := fmt.Sprintf("%s_%s.log", sanitizeHost(host), now.Format("2006-01-02_150405"))
	return filepath.Join(home, ".mysqlmonitoring", name)
}

// sanitizeHost replaces filesystem-unfriendly characters with '_'.
func sanitizeHost(host string) string {
	if host == "" {
		return "unknown-host"
	}
	var b strings.Builder
	b.Grow(len(host))
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func secondsToDuration(s int) time.Duration {
	return time.Duration(s) * time.Second
}

func runMonitor(f flags, cmd *cobra.Command) error {
	// Default the log path to one file per session: <host>_<timestamp>.log.
	// An explicit --log-file="" still opts out of file logging.
	if !cmd.Flags().Changed("log-file") {
		f.logFile = sessionLogFile(resolvedHost(f, cmd), time.Now())
	}

	database, err := connect(f, cmd)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	cfg := buildConfig(f)
	mon := monitor.New(database, cfg)
	resultCh := mon.Run(ctx)

	// Insights always starts so the Overview tab's health vitals
	// (Threads_running, replica lag, HLL, etc.) render even without
	// --enable-perf-insights. The perf-insights collectors themselves
	// (digest/wait/CPU) are gated by ProbeCapabilities; the health
	// collector runs unconditionally because SHOW GLOBAL STATUS works
	// on any server. Probe warnings only print when the user opted in.
	perfCfg := insights.Config{
		PollInterval:        time.Duration(f.perfInterval) * time.Second,
		CPUSampleInterval:   time.Duration(f.perfCPUSampleMillis) * time.Millisecond,
		Window:              time.Duration(f.perfWindow) * time.Second,
		MaxDigests:          f.perfMaxDigests,
		SessionCapacity:     8192,
		NewDigestProtection: 30 * time.Second,
	}
	ins := insights.New(perfCfg, database)
	var probeWarn io.Writer
	if f.enablePerfInsights {
		probeWarn = os.Stderr
	}
	if err := ins.Probe(ctx, probeWarn); err != nil {
		// Probe failure is non-fatal; the health collector still runs.
		// Only surface to stderr when the user asked for perf-insights.
		if f.enablePerfInsights {
			fmt.Fprintf(os.Stderr, "perf-insights probe failed: %v\n", err)
		}
	}
	go ins.Run(ctx)

	var explainEngine *explain.Engine
	if f.enablePerfInsights {
		explainEngine = explain.New(database)
	}

	// JSON log file. The file is opened here but its lifetime is
	// managed explicitly per output mode below: closing it before the
	// log writers stop produces "file already closed" errors, so we
	// avoid `defer logFile.Close()` and instead close after the
	// writer goroutines drain.
	var logFile *os.File
	if f.logFile != "" {
		if err := os.MkdirAll(filepath.Dir(f.logFile), 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
		var err error
		logFile, err = os.OpenFile(f.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Logging to %s\n", f.logFile)
	}

	// logMu protects logFile so the TUI's forwarder goroutine and any
	// late-arriving writes can't race with Close.
	var (
		logMu     sync.Mutex
		logClosed bool
	)
	writeLog := func(result monitor.Result) {
		if logFile == nil {
			return
		}
		logMu.Lock()
		defer logMu.Unlock()
		if logClosed {
			return
		}
		if err := output.FormatJSON(logFile, result); err != nil {
			fmt.Fprintf(os.Stderr, "Log write error: %v\n", err)
		}
	}
	closeLog := func() {
		if logFile == nil {
			return
		}
		logMu.Lock()
		logClosed = true
		_ = logFile.Close()
		logMu.Unlock()
	}

	switch f.outputFormat {
	case "tui":
		if logFile != nil {
			logged := make(chan monitor.Result, 1)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer close(logged)
				for result := range resultCh {
					writeLog(result)
					// If the TUI consumer is gone, drop the message
					// rather than block forever; we still drain
					// resultCh so the monitor goroutine can exit.
					select {
					case logged <- result:
					case <-ctx.Done():
					}
				}
			}()
			err := tui.Run(logged, database, ins, explainEngine)
			cancel()
			wg.Wait()
			closeLog()
			return err
		}
		err := tui.Run(resultCh, database, ins, explainEngine)
		cancel()
		closeLog()
		return err
	case "json":
		for result := range resultCh {
			writeLog(result)
			if err := output.FormatJSON(os.Stdout, result); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		}
		closeLog()
	case "text":
		for result := range resultCh {
			writeLog(result)
			fmt.Print("\033[2J\033[H")
			output.FormatText(os.Stdout, result)
		}
		closeLog()
	default:
		return fmt.Errorf("unknown output format: %s", f.outputFormat)
	}

	return nil
}

func runStatus(f flags, cmd *cobra.Command) error {
	database, err := connect(f, cmd)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer database.Close()

	cfg := buildConfig(f)
	mon := monitor.New(database, cfg)
	result := mon.Snapshot(context.Background())

	if result.Error != nil {
		return result.Error
	}

	switch f.outputFormat {
	case "json":
		if err := output.FormatJSON(os.Stdout, result); err != nil {
			return err
		}
	default:
		output.FormatText(os.Stdout, result)
	}

	// Exit code based on severity
	switch result.MaxSeverity() {
	case detector.SeverityCritical:
		os.Exit(2)
	case detector.SeverityWarning:
		os.Exit(1)
	}

	return nil
}

func runKill(f flags, cmd *cobra.Command, connIDStr string) error {
	connID, err := strconv.ParseUint(connIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid connection ID: %s", connIDStr)
	}

	database, err := connect(f, cmd)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer database.Close()

	// Confirm
	fmt.Printf("Kill connection %d? [y/N] ", connID)
	var confirm string
	_, _ = fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("Cancelled.")
		return nil
	}

	k := killer.New(database)
	if err := k.Kill(context.Background(), connID); err != nil {
		return fmt.Errorf("failed to kill connection: %w", err)
	}

	fmt.Printf("Connection %d killed.\n", connID)
	return nil
}
