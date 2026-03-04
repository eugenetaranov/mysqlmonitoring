# MySQL Lock Monitor

Real-time MySQL lock contention monitor with terminal UI.

## Installation

```bash
make build          # builds to ./bin/mysqlmonitoring
make install        # builds and copies to /usr/local/bin
```

## Quick Start

```bash
mysqlmonitoring monitor --host 127.0.0.1 --port 3306 --user root --password secret
# or with a DSN
mysqlmonitoring monitor --dsn "root:secret@tcp(127.0.0.1:3306)/mydb"
```

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

## Demo

Requires Docker. Starts MySQL + workload generators that create lock contention and deadlocks.

```bash
make demo           # build + start demo + launch monitor
make demo-down      # stop demo environment
```

## TUI Controls

| Key | Action |
|-----|--------|
| j/k | Navigate lock tree |
| K   | Kill selected connection |
| q   | Quit |

## Development

```bash
make test               # unit tests
make lint               # golangci-lint
make test-integration   # integration tests (requires Docker)
```
