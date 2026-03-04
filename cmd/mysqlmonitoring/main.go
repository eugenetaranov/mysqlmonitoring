package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
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
	interval           int
	lockWaitThreshold  int
	longQueryThreshold int
	outputFormat       string
	logFile            string
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
	pf.StringVar(&f.dsn, "dsn", "", "MySQL DSN (e.g. user:pass@tcp(host:port)/db)")
	pf.StringVar(&f.host, "host", "localhost", "MySQL host")
	pf.IntVar(&f.port, "port", 3306, "MySQL port")
	pf.StringVar(&f.user, "user", "root", "MySQL user")
	pf.StringVar(&f.password, "password", "", "MySQL password")
	pf.StringVar(&f.database, "database", "", "MySQL database")

	monitorCmd := &cobra.Command{
		Use:   "monitor",
		Short: "Continuously monitor for lock issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("output") {
				f.outputFormat = "tui"
			}
			return runMonitor(f)
		},
	}
	monitorCmd.Flags().IntVar(&f.interval, "interval", 2, "Poll interval in seconds")
	monitorCmd.Flags().IntVar(&f.lockWaitThreshold, "lock-wait-threshold", 10, "Lock wait warning threshold in seconds")
	monitorCmd.Flags().IntVar(&f.longQueryThreshold, "long-query-threshold", 30, "Long query warning threshold in seconds")
	monitorCmd.Flags().StringVar(&f.outputFormat, "output", "tui", "Output format: tui, text, json")
	monitorCmd.Flags().StringVar(&f.logFile, "log-file", defaultLogFile(), "Append JSON log of each snapshot to this file")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "One-shot status check (exit code: 0=ok, 1=warning, 2=critical)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("output") {
				f.outputFormat = "text"
			}
			return runStatus(f)
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
			return runKill(f, args[0])
		},
	}

	rootCmd.AddCommand(monitorCmd, statusCmd, killCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func buildDSN(f flags) string {
	if f.dsn != "" {
		return f.dsn
	}
	return db.BuildDSN(db.DSNConfig{
		Host:     f.host,
		Port:     f.port,
		User:     f.user,
		Password: f.password,
		Database: f.database,
	})
}

func connect(f flags) (*db.MySQLDB, error) {
	dsn := buildDSN(f)
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

func defaultLogFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mysqlmonitoring", "monitor.log")
}

func secondsToDuration(s int) time.Duration {
	return time.Duration(s) * time.Second
}

func runMonitor(f flags) error {
	database, err := connect(f)
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

	// JSON log file
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
		defer logFile.Close()
		fmt.Fprintf(os.Stderr, "Logging to %s\n", f.logFile)
	}

	writeLog := func(result monitor.Result) {
		if logFile != nil {
			if err := output.FormatJSON(logFile, result); err != nil {
				fmt.Fprintf(os.Stderr, "Log write error: %v\n", err)
			}
		}
	}

	switch f.outputFormat {
	case "tui":
		if logFile != nil {
			logged := make(chan monitor.Result, 1)
			go func() {
				defer close(logged)
				for result := range resultCh {
					writeLog(result)
					logged <- result
				}
			}()
			return tui.Run(logged, database)
		}
		return tui.Run(resultCh, database)
	case "json":
		for result := range resultCh {
			writeLog(result)
			if err := output.FormatJSON(os.Stdout, result); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		}
	case "text":
		for result := range resultCh {
			writeLog(result)
			fmt.Print("\033[2J\033[H")
			output.FormatText(os.Stdout, result)
		}
	default:
		return fmt.Errorf("unknown output format: %s", f.outputFormat)
	}

	return nil
}

func runStatus(f flags) error {
	database, err := connect(f)
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

func runKill(f flags, connIDStr string) error {
	connID, err := strconv.ParseUint(connIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid connection ID: %s", connIDStr)
	}

	database, err := connect(f)
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
