# Changelog

## Unreleased

### Changed — Default view is now **Overview**

`mysqlmonitoring monitor` opens on a new **Overview** tab instead of
**Issues**. The Overview answers "is the database OK?" in one frame
with a status verdict, AAS sparkline, load attribution by user / host
/ schema, replication state (when applicable), live issues, and
hottest queries / tables. Press `I` for the old default; `O` jumps
back to Overview from anywhere.

### Added — Health vitals collector

A small additional poller (default every 5s) cherry-picks a handful
of `SHOW GLOBAL STATUS` counters (`Threads_running`,
`Threads_connected`, buffer-pool dirty / total / read requests / reads,
`Aborted_clients`) and, when the server has a replica role, runs
`SHOW REPLICA STATUS` (or `SHOW SLAVE STATUS` on older servers /
MariaDB). Cost is at most two `SHOW` statements per cycle; the
replica query is conditional on a one-time probe.

InnoDB History List Length is now parsed from the existing
`SHOW ENGINE INNODB STATUS` output (no new query).

### Added — Per-user/host load attribution

`series.SessionSample` carries `User` / `Host`, populated from the
existing `performance_schema.threads` JOIN in the `CurrentStatements`
query (no new round-trip). New `insights.LoadByGroup` aggregates AAS
by user / host / schema purely in-memory.

### Added — Milestone 1: Performance Insights (CLI)

The first slice of self-hosted Performance Insights / SolarWinds DPA
parity, all in-memory and zero new dependencies.

- **Query digest sampling.** New collector diff-samples
  `performance_schema.events_statements_summary_by_digest` every
  `--perf-interval` seconds, detects counter resets (server restart,
  `TRUNCATE TABLE`), evicts low-load digests under a configurable cap,
  and feeds an in-memory time series.
- **Wait-event sampling.** New collector buckets every wait event into
  CPU / IO / Lock / Sync / Network / Other. CPU is sampled at ~1 Hz
  from `events_statements_current`; the rest from
  `events_waits_summary_global_by_event_name` deltas.
- **Application tagging.** Sessions resolve an app tag from leading
  sqlcommenter SQL comments (`/* service='checkout', ... */`) or the
  `program_name` connect attribute. Top SQL and load can be sliced by
  app.
- **`mysqlmonitoring top`** — one-shot top-SQL ranking via two diff
  polls. Flags: `--interval`, `--limit`, `--sort {aas,calls,latency,rows-examined}`,
  `--app`, `--schema`, `--json`.
- **`mysqlmonitoring load`** — one-shot per-class DB-load breakdown.
  Flags: `--interval`, `--json`.
- **`monitor --enable-perf-insights`** — launches the same collectors
  alongside the lock monitor. The TUI gains:
  - colour-coded DB-load sparkline header on every screen,
  - **Top SQL panel** (press `t`) with sort cycling and app/schema
    filters,
  - **EXPLAIN modal** (`e` or Enter) that runs `EXPLAIN FORMAT=JSON`
    on a read-only connection, renders the plan tree, flags
    `FULL SCAN` / `FILESORT` / `TEMP TABLE` / `BIG SCAN RATIO`, and
    detects plan flips,
  - footer line with tracked-digest count and registry evictions.
- **Capability probe.** A one-shot stderr warning prints at startup
  when `setup_consumers` aren't enabled — naming the exact `UPDATE`
  needed to fix it. Each collector silently disables itself if its
  prerequisites are off; the lock monitor keeps running.
- **EXPLAIN safety.** Plan generation uses a dedicated connection with
  `transaction_read_only=ON` and `MAX_EXECUTION_TIME=2000`, plus a 5s
  client deadline. Non-`SELECT` examples are refused, never executed.

### Deferred to a later milestone

- On-disk persistence (M2). Today's series live entirely in memory and
  vanish on exit.
- Replication, InnoDB internals, schema advisor (M2).
- YAML alerting and webhook/Slack/PagerDuty sinks (M3).
- Host metrics / SSH co-sampling, multi-host registry (M4).
- p50/p95 latencies — needs `events_statements_histogram_*` (M5).
- Anomaly detection / baselines (M5).

### CLI changes

- New flags on `monitor`: `--enable-perf-insights`,
  `--perf-interval`, `--perf-window`, `--perf-max-digests`,
  `--perf-cpu-sample-ms`.
- New subcommands: `top`, `load`.

### Compatibility

- No breaking changes. Existing `monitor`, `status`, `kill` flags and
  output formats are unchanged.
- Requires `performance_schema` enabled on the target MySQL (default
  in 5.7+ and 8.0+). MariaDB best-effort.
