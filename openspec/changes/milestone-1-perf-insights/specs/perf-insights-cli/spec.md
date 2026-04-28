## ADDED Requirements

### Requirement: `top` subcommand

The system SHALL provide a `mysqlmonitoring top` subcommand that performs two digest polls separated by `--interval` (default 10s), aggregates the resulting deltas, and prints a ranked top‑SQL table. The subcommand SHALL accept the following flags:

- `--interval <duration>`: poll spacing, default `10s`, minimum `1s`.
- `--limit <n>`: number of rows, default `20`.
- `--sort <key>`: one of `aas`, `calls`, `p95`, `rows-examined`; default `aas`.
- `--app <tag>`: filter by application tag resolved per `app-tagging`.
- `--schema <name>`: filter by schema.
- `--json`: emit one JSON object per line instead of the text table.

The subcommand SHALL exit non‑zero with a clear message if `performance_schema` digest data is unavailable.

#### Scenario: Default text output

- **WHEN** `mysqlmonitoring top` is run against a database with active workload
- **THEN** the system SHALL print a header row and up to 20 ranked rows showing AAS, calls/s, p50, p95, rows examined, schema, and a truncated digest text
- **AND** the process SHALL exit `0`

#### Scenario: JSON output

- **WHEN** `mysqlmonitoring top --json --limit 5` is run
- **THEN** the system SHALL print up to five lines of NDJSON, one object per digest, with stable field names

#### Scenario: Filter by app

- **WHEN** `mysqlmonitoring top --app checkout` is run and at least one digest has sessions tagged `checkout`
- **THEN** only digests with sessions tagged `checkout` SHALL appear in the output

### Requirement: `load` subcommand

The system SHALL provide a `mysqlmonitoring load` subcommand that performs two wait‑event polls separated by `--interval` and prints per‑class Average Active Sessions for the interval. Flags:

- `--interval <duration>`: default `10s`.
- `--app <tag>`: filter to sessions tagged with the given app.
- `--json`: emit one JSON object describing the per‑class breakdown.

#### Scenario: Default text output

- **WHEN** `mysqlmonitoring load` is run
- **THEN** the system SHALL print one line per class (`CPU`, `IO`, `Lock`, `Sync`, `Network`, `Other`) with that class's AAS value
- **AND** SHALL print a final `total` line

### Requirement: TUI integration

The TUI SHALL gain two additions:

1. A stacked DB‑load‑by‑wait‑class sparkline rendered as a header on every screen, drawn from the in‑memory wait series for the visible window.
2. A `top` panel reachable via a key binding that displays the same ranked digest table as the `top` subcommand and supports keystrokes for sort change, app filter, and triggering EXPLAIN on the selected digest.

The lock‑chain, long‑transaction, DDL‑conflict, and deadlock views SHALL remain reachable and unchanged in behavior.

#### Scenario: Sparkline header

- **WHEN** the TUI is running with a populated wait series
- **THEN** every screen SHALL display a stacked sparkline at the top whose color bands correspond to the six wait classes
- **AND** the sparkline SHALL update on each successful wait poll

#### Scenario: Trigger EXPLAIN from `top` panel

- **WHEN** the user selects a row in the `top` panel and presses the EXPLAIN key binding
- **THEN** the TUI SHALL invoke `explain-on-demand` for that digest and render the result in a modal view

### Requirement: In‑memory series window

All `top`/`load` aggregations and TUI panels SHALL read from in‑memory ring buffers whose total wall‑clock retention is `--window` (default `1h`). The system SHALL NOT persist series data to disk in M1.

#### Scenario: Window exceeded

- **WHEN** the TUI has been running for longer than `--window`
- **THEN** samples older than the window SHALL be evicted
- **AND** the sparkline SHALL show only the most recent `--window` worth of data
