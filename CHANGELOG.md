# Changelog

## Unreleased

### Changed — Overview tab restructured

The Overview body now reads top-down as: verdict line, DB Load
sparkline, three side-by-side top-N panels at a 60-second window
(Top AAS queries, Top AAS users, Top busiest tables), and a
Long-transactions + Replication strip below. Three eye movements
to triangulate "spike → who → what."

The previous Load-by-USER/HOST/SCHEMA panel and its `u`/`h`/`s`
cycle are gone. The keys remain bound: `enter` and `u` drill into
Top SQL filtered by the cursor user; `h` and `s` jump straight to
Top SQL, where the dedicated tab handles per-host and per-schema
breakdowns. The Hottest Queries / Hottest Tables panels and the
embedded Live Issues panel have been replaced — the activity-based
"Top busiest tables (60s)" is the new sibling of Top AAS queries;
issues live in the dedicated I tab.

### Changed — Header chrome reduced to a single row

The previous four-row header (title + tab bar, server version,
transaction/lock-wait/process counts, DB Load sparkline) collapses
to a single row with title + tab bar on the left and a compact
context block on the right (snapshot time, server version + variant
tag, uptime, optional `[cw]●` indicator). The dropped rows are
all duplicated content from the Overview's body — the verdict line
already shows what they showed.

### Added — CloudWatch RDS metrics

When running against RDS or Aurora and the AWS SDK default
credential chain produces credentials, a new collector polls
CloudWatch every 60 s for `CPUUtilization`, `FreeableMemory`,
`ReadIOPS`, `WriteIOPS`, `ReadLatency`, `WriteLatency`,
`DiskQueueDepth`, plus Aurora-only `DBLoad`/`DBLoadCPU`/
`DBLoadNonCPU` and `AuroraReplicaLag`. CPU%, free memory, and IOPS
surface in the verdict line. CPU bumps the verdict word at >80%
(WARN) and >95% (PAGE).

Authentication: AWS SDK default credential chain only — no
`--aws-profile` flag. Region and instance ID are auto-detected from
the RDS hostname; explicit `--aws-region` and `--rds-instance` flags
override.

Cost: ~$0.23/month per monitored instance at the default 60 s
cadence.

### Added — MDL queue view (key `M`)

A new **MDL** tab inspects the `performance_schema.metadata_locks`
grant queue per table. Designed for the on-incident question
"my `ALTER TABLE` has been hanging for minutes — where is it in
the queue and what's blocking it?" The view answers four questions:

- **Hottest tables**: list mode sorts every contended table by
  pending queue depth, with per-`LOCK_TYPE` bucket counts.
- **Lock-type breakdown**: detail mode (press `enter` on a list
  row) shows the full QUEUE and HOLDERS panels for one table.
- **Queue position**: every PENDING entry is numbered `#1`..`#N`
  in FIFO order — find your PID, read your rank.
- **Direct blockers**: HOLDERS panel lists every GRANTED holder;
  `B` toggles a filter that keeps only holders whose `LOCK_TYPE`
  conflicts with the cursor waiter's request.

Requires the `wait/lock/metadata/sql/mdl` instrument to be enabled
(off by default on MySQL 5.7 / 8.0 LTS, on by default in 8.1+).
The tool prints a one-shot warning at startup with the exact
`UPDATE setup_instruments` SQL when it's off.

### Fixed — `MetadataLocks` query referenced non-existent `LOCK_MODE` column

The query backing `Snapshot.MetadataLocks` selected a column
(`LOCK_MODE`) that does not exist on `performance_schema.metadata_locks`
— the right column is `LOCK_TYPE`. Until now the failure has been
silently swallowed by an `if err != nil { return nil, nil }`
shortcut, leaving the `ddl_conflict` detector's
`findPotentialBlockers` helper without input. The query is rewritten
to select `LOCK_STATUS` (GRANTED / PENDING) plus the matching
`performance_schema.threads` row's processlist user / host / SQL /
wait age. Errors are now surfaced rather than swallowed so future
regressions can't hide in the same way.

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
