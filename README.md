# MySQL Lock Monitor

Real-time MySQL lock contention monitor with terminal UI.

## Installation

### Homebrew (macOS/Linux)

```bash
brew install eugenetaranov/tap/mysqlmonitoring
```

### Download Binary

Grab the latest release from [GitHub Releases](https://github.com/eugenetaranov/mysqlmonitoring/releases) and place it in your `$PATH`.

### Build from Source

```bash
go install github.com/eugenetaranov/mysqlmonitoring/cmd/mysqlmonitoring@latest
```

Or clone and build locally:

```bash
make build          # builds to ./bin/mysqlmonitoring
make install        # builds and copies to /usr/local/bin
```

## Quick Start

```bash
mysqlmonitoring monitor --host 127.0.0.1 --port 3306 --user root --password secret
# or with a DSN
mysqlmonitoring monitor --dsn "root:secret@127.0.0.1:3306/mydb"
# or using ~/.my.cnf credentials automatically
mysqlmonitoring monitor
```

Connection parameters are resolved in priority order: `--dsn` flag > explicit CLI flags (`--host`, `--user`, etc.) > `~/.my.cnf` `[client]` section > built-in defaults (localhost:3306, root).

Use `--defaults-file` to specify an alternate `.my.cnf` path.

## Commands

### `monitor` (default)

Continuous lock monitoring with a TUI dashboard.

```
--output tui|text|json   Output format (default: tui)
--interval 2             Poll interval in seconds
--log-file PATH          Append JSON snapshots to file
--lock-wait-threshold 10 Seconds before warning on lock waits
--long-query-threshold 30 Seconds before warning on long queries
```

### `status`

One-shot health check. Exit codes: 0 = ok, 1 = warning, 2 = critical.

```bash
mysqlmonitoring status --host 127.0.0.1 --user root --password secret
```

### `kill <id>`

Kill a MySQL connection by process ID (with confirmation prompt).

```bash
mysqlmonitoring kill 42 --host 127.0.0.1 --user root --password secret
```

### `top` (perf insights)

Show the heaviest SQL by Average Active Sessions, calls/s, latency or
rows examined. Performs two `events_statements_summary_by_digest`
polls separated by `--interval` seconds and prints the diff:

```bash
mysqlmonitoring top --interval 10 --limit 20 --sort aas
mysqlmonitoring top --json | jq                  # NDJSON, one row per digest
mysqlmonitoring top --app checkout               # slice by application
mysqlmonitoring top --schema orders --sort calls # filter to a schema
```

`--sort` accepts `aas`, `calls`, `latency`, `rows-examined`.

### `load` (perf insights)

Print the DB load broken down by wait class (CPU / IO / Lock / Sync /
Network / Other) for the last `--interval` seconds:

```bash
mysqlmonitoring load --interval 10
mysqlmonitoring load --json
```

CPU is sampled at ~1 Hz from `events_statements_current` for sessions
that are executing a statement with no current wait; non-CPU classes
come from `events_waits_summary_global_by_event_name`.

### Live perf insights inside the TUI

Pass `--enable-perf-insights` to `monitor` to launch the digest, wait
and CPU collectors alongside the lock monitor. The TUI gains:

- a colour-coded **DB-load sparkline** at the top of every screen
  showing the last hour of total AAS plus the current per-class
  breakdown,
- a **Top SQL panel** (press `t`) — sortable digest table with the
  same fields as `mysqlmonitoring top`,
- an **EXPLAIN modal** (`e` or Enter on a digest) that pulls a recent
  example, runs `EXPLAIN FORMAT=JSON` on a read-only connection,
  flags `FULL SCAN`, `FILESORT`, `TEMP TABLE`, `BIG SCAN RATIO`, and
  records plan flips,
- a footer line surfacing tracked-digest count and registry evictions.

```bash
mysqlmonitoring monitor --enable-perf-insights \
  --perf-interval 10 --perf-window 3600 --perf-max-digests 2000
```

All collectors run in process and forget their data on exit — no
SQLite, no agent, no extra dependencies in M1.

## Required performance_schema setup

The perf-insights views need MySQL's `performance_schema` enabled with
the right consumers. On a default MySQL 5.7+/8.0 install this is
already on; verify with:

```sql
SELECT NAME, ENABLED FROM performance_schema.setup_consumers
WHERE NAME IN (
  'global_instrumentation', 'statements_digest',
  'events_statements_history_long', 'events_waits_current'
);
```

Enable any that are `NO`:

```sql
UPDATE performance_schema.setup_consumers
   SET ENABLED='YES'
 WHERE NAME IN (
   'global_instrumentation', 'statements_digest',
   'events_statements_history_long', 'events_waits_current'
 );
```

`events_statements_history_long` is required only for EXPLAIN-on-demand
(it stores the per-execution `SQL_TEXT` we sample). When a consumer is
off, the matching collector logs a one-time warning to stderr at
startup and disables itself; the lock monitor keeps running normally.

## App tagging conventions

Top SQL and CPU samples can be sliced by application using either
convention — pick whichever your driver supports:

1. **sqlcommenter SQL comments** (preferred when you have request
   context): prefix each statement with a leading block comment
   containing `service='<app>'`. Example:
   ```sql
   /* service='checkout', route='POST /cart' */
   SELECT * FROM cart WHERE user_id = 5
   ```
   Comments must come at the start of the statement and stay under
   1024 bytes; values may be URL-encoded and single-quoted.
2. **`program_name` connect attribute**: set when opening the
   connection (e.g. `?conn_attrs=program_name:orders-api` in some
   drivers, or via `mysql_init` / `mysql_options4` in C). One
   connection one tag.

When neither is present, the digest is tagged `unknown`.

## Demo

Requires Docker. Starts MySQL + workload generators that create lock contention and deadlocks.

```bash
make demo           # build + start demo + launch monitor
make demo-down      # stop demo environment
```

## TUI Controls

| Key       | Action |
|-----------|--------|
| j/k       | Navigate lock tree (or top-SQL rows when in the Top view) |
| K         | Kill selected connection |
| t         | Open Top SQL panel (requires `--enable-perf-insights`) |
| s         | Cycle Top SQL sort key (AAS → Calls → Latency → Rows) |
| e / Enter | EXPLAIN the highlighted digest |
| L         | Return to the lock view |
| esc       | Back out of Top / EXPLAIN |
| q         | Quit |

## Development

```bash
make test               # unit tests
make lint               # golangci-lint
make test-integration   # integration tests (requires Docker)
```
