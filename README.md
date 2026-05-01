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

### Overview tab (default)

The TUI opens on the **Overview** tab, designed for the 3am triage
question: "is the database OK?" One screen, one frame.

```
MySQL Lock Monitor   [O Overview][I Issues][B Tables][M MDL][L Lock][t Top SQL]   10:42:13 · 8.0.45 RDS · up 14d · [cw]●
─────────────────────────────────────────────────────────────────────────────────────────
 [HEALTHY] CPU 34%  Mem 14.2GB free  IOPS 1.2k/450  AAS 1.8/8c  running 3/512
           bp_hit 99%  HLL 12k  repl +0s  dl 0
 DB Load ▁▂▂▃▄▅▆▇  CPU 1.1  IO 0.4  Lock 0.0  Σ 1.5
─────────────────────────────────────────────────────────────────────────────────────────
 Top AAS queries (60s)   Top AAS users (60s)         Top busiest tables (60s)
 AAS 4.2 select_orders   app_rw      ████ 8.4        shop.orders   2.1 AAS  2.1k qps
 AAS 1.8 update_items    reports     ██   1.6        shop.items    1.4 AAS    980 qps
 AAS 1.4 analytics_roll  app_ro      █    1.1        auth.sessions 0.8 AAS    640 qps
─────────────────────────────────────────────────────────────────────────────────────────
 Long transactions (≥30s)                       Replication
 3m12s  pid 8821  app_rw  ALTER orders          source=db-01  IO=Y SQL=Y
 2m04s  pid 8714  cron    sweep_old             lag 0s  GTID gap 0
 1m45s  pid 8717  cron    (idle in trx)
```

Three eye movements: verdict line for *what's spiking*, three top-N
panels for *who's running it and where it's landing*, Long-trx +
Replication strip for *what's stuck*.

Keys:

- `O` — back to Overview from anywhere.
- `j` / `k` / `g` / `G` — navigate the Top AAS users cursor.
- `enter` or `u` — drill into Top SQL filtered by the cursor user.
- `h` / `s` — jump to Top SQL (use Top SQL's own filters there for
  per-host / per-schema breakdowns).
- `I` / `B` / `L` / `M` / `t` — Issues / Tables / Lock chains /
  MDL queue / Top SQL.

The view contains:

- a **chrome bar** with snapshot time, server version + variant tag,
  uptime, and a `[cw]●` indicator that lights up when CloudWatch
  metrics are wired and producing samples.
- a **status verdict line** — `[HEALTHY]` / `[WARN]` / `[PAGE]` paired
  with colour. When `--aws-region` (or hostname-parse) resolves an
  RDS / Aurora target and AWS credentials are present, the leftmost
  cluster shows host CPU%, free memory, IOPS r/w, and (Aurora-only)
  DBLoad. MySQL-side gauges (AAS, threads_running, bp_hit, HLL,
  replica lag, deadlock count) follow.
- the **DB-load sparkline** by wait class (CPU / IO / Lock / Sync /
  Network), rendered exactly once.
- three **top-N panels at a 60-second window**:
  - *Top AAS queries* — digests ranked by AAS, with a `no_idx` flag
    when the digest scanned without an index.
  - *Top AAS users* — MySQL users ranked by AAS with horizontal
    bars; cursor lives here.
  - *Top busiest tables* — schema.table ranked by AAS over the
    window, with calls/sec alongside. Activity-based, distinct
    from the detector-contention view in the Tables tab.
- a **Long transactions** strip (≥30 s) showing the slowest open
  transactions sorted desc — catches the silent "idle in
  transaction" wedger.
- a **Replication** panel showing source, IO/SQL thread state, lag,
  GTID gap. Removed entirely on standalone servers; Long-trx
  widens to full width.

The verdict line, replication panel, and long-trx panel work without
`--enable-perf-insights`; the AAS sparkline and the three top-N
panels need `performance_schema` enabled and self-describe their
state when those data sources are missing.

### CloudWatch RDS metrics

When `mysqlmonitoring monitor` runs against an RDS or Aurora
instance and the AWS SDK default credential chain produces
credentials, a separate collector polls CloudWatch every 60 s for
`CPUUtilization`, `FreeableMemory`, `ReadIOPS`, `WriteIOPS`,
`ReadLatency`, `WriteLatency`, `DiskQueueDepth`, plus Aurora-only
`DBLoad` family and replication lag. The fields surface in the
verdict line; the chrome `[cw]●` indicator lights up green when at
least one sample has landed.

```bash
# Hostname parse (works for *.rds.amazonaws.com endpoints):
mysqlmonitoring monitor --host prod-orders.abc.us-east-1.rds.amazonaws.com ...

# Explicit overrides for unusual DNS configurations:
mysqlmonitoring monitor --aws-region us-east-1 --rds-instance prod-orders ...
```

No `--aws-profile` flag — the SDK default credential chain is the
contract (env vars, `~/.aws/credentials`, IAM instance role, EKS
service account). When no credentials resolve, the collector logs a
single startup notice and the CW fields don't render.

Cost: ~$0.23/month per monitored instance at the default 60 s
cadence (10 metrics × 1440 polls/day × CloudWatch GetMetricData
pricing).

### MDL queue (key `M`)

When an `ALTER TABLE` (or any other statement) is hanging on a
metadata lock, press `M` to see the per-table grant queue. The view
answers four operator questions in one screen:

1. **Hottest tables** — list mode (default) sorts every table with a
   pending MDL waiter by queue depth, with a `TYPES` column showing
   the per-`LOCK_TYPE` bucket counts (`38×SHARED_READ
   6×SHARED_WRITE 2×EXCLUSIVE`).
2. **Lock-type breakdown** for one table — same TYPES column,
   plus the full QUEUE/HOLDERS detail when you `enter` on a row.
3. **Queue position** — detail mode renders every PENDING entry as
   `#1`..`#N` in FIFO order (longest wait first ≈ MySQL's grant
   order). Find your PID and read the `#` column.
4. **Direct blockers** — detail mode's HOLDERS panel lists every
   GRANTED holder. Press `B` to filter to just the holders whose
   `LOCK_TYPE` is incompatible with the cursor waiter's request.

Keys inside the M tab: `↑↓`/`j`/`k` navigate, `enter` opens a
table's detail view, `B` toggles the blocker filter, `K` kills the
selected waiter (with confirm), `esc` returns.

The `wait/lock/metadata/sql/mdl` instrument is **off by default on
MySQL 5.7 and 8.0 LTS**. The tool warns about this once at startup
and the M tab renders the exact `UPDATE setup_instruments` SQL when
the instrument is disabled. On MySQL 8.1+ it's on by default.

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

Requires Docker. The `tests/demo/` stack runs MySQL 8.0 plus two
workload generators side-by-side:

- A **Python contention runner** that loops through long
  transactions, row-lock contention, lock chains, multi-blocker
  fan-outs, DDL/MDL conflicts, and deadlocks — guaranteeing the
  Issues, Lock, and MDL tabs always have something to show.
- **sysbench `oltp_read_write`** running steady mixed traffic on a
  separate `sbtest` database — gives the Overview's AAS sparkline,
  Top SQL panel, and load-attribution panels constant baseline
  traffic to attribute. Opt out with
  `docker compose -f tests/demo/docker-compose.yaml up -d --scale sysbench=0`.

The MDL instrument (`wait/lock/metadata/sql/mdl`) is enabled
automatically by `init.sql` so the **M** tab works out of the box.

Three Make targets, used independently:

```bash
make test-up        # start MySQL + workload + sysbench
make test-run       # build + run the local binary against the running stack
make test-down      # stop and clean up
```

Use `test-up` once and `test-run` repeatedly while iterating on the
code — the binary rebuilds in seconds and reconnects without
restarting MySQL.

`make demo` is a single-command shortcut for `test-up` + `test-run`.

Pass extra flags through with `ARGS=`:

```bash
make test-run ARGS="--interval 1"
```

The `make demo-up` and `make demo-down` aliases continue to work
for backward compatibility.

## TUI Controls

| Key       | Action |
|-----------|--------|
| j/k       | Navigate the focused panel |
| O         | Overview (default tab) |
| I         | Issues |
| B         | Tables |
| M         | MDL queue (per-table waiters + holders) |
| L         | Lock tree |
| t         | Top SQL (requires `--enable-perf-insights`) |
| u/h/s     | (Overview) Cycle Load panel between USER / HOST / SCHEMA |
| enter     | Drill into the focused row (filter set on the next view) |
| K         | Kill the selected connection |
| B         | (MDL detail) Toggle HOLDERS panel between all granted and "blockers only" for the cursor waiter |
| s         | (Top) Cycle sort key — AAS → Calls → Latency → Rows |
| e / Enter | (Top) EXPLAIN the highlighted digest |
| esc       | Back out (returns to Overview from most views; from MDL detail, returns to MDL list first) |
| q         | Quit |

## Development

```bash
make test               # unit tests
make lint               # golangci-lint
make test-integration   # integration tests (requires Docker)
```
