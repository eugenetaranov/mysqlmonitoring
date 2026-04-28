## ADDED Requirements

### Requirement: Overview is the default launch view

On startup, the TUI SHALL render the Overview tab. The tab key SHALL be `O` and the tab SHALL be the leftmost entry in the tab bar.

#### Scenario: Default launch

- **WHEN** `mysqlmonitoring monitor` is run with no extra flags
- **THEN** the rendered view SHALL be the Overview
- **AND** pressing `I` SHALL switch to Issues, `B` to Tables, `L` to Lock chains, `t` to Top SQL

### Requirement: Status verdict line

A single status line at the top of the Overview SHALL summarise server health using a verdict word (`[HEALTHY]` / `[WARN]` / `[PAGE]`) paired with a color, plus key gauges (uptime, AAS over vCPU count, `Threads_running` over `max_connections`, buffer-pool hit rate, HLL, replica lag, deadlock count). The word SHALL always carry severity so that color is not the sole signal.

#### Scenario: Healthy server

- **WHEN** `Threads_running ≤ 50% of max_connections` AND no lock chain has more than 5 waiters AND replica lag (if any) is at or below the configured threshold AND HLL ≤ 1M
- **THEN** the line SHALL begin with `[HEALTHY]` rendered in green

#### Scenario: Warning state

- **WHEN** any single warn-band gauge is exceeded (Threads_running > 50% of max_connections, replica lag > threshold, HLL > 1M, Aborted_clients delta > 0 in the latest window)
- **THEN** the line SHALL begin with `[WARN]` rendered in amber
- **AND** the offending gauge SHALL display an upward trend arrow

#### Scenario: Page state

- **WHEN** any single page-band gauge is exceeded (Threads_running > 80% of max_connections, replica lag > 5× threshold, HLL > 5M, blocker chain with >5 waiters or >30s)
- **THEN** the line SHALL begin with `[PAGE]` rendered in red

### Requirement: Load-attribution panel toggles

The Overview SHALL render a single "Load by …" panel that cycles between user / host / schema groupings. Cycling SHALL be triggered by `u` / `h` / `s` keys.

#### Scenario: User toggles to host

- **WHEN** the load panel currently shows USER and the user presses `h`
- **THEN** the panel SHALL re-render grouped by HOST
- **AND** subsequent `enter` SHALL drill into Top SQL filtered by the selected host

### Requirement: Drill-down preserves filters

Pressing `enter` on any Overview row SHALL navigate to the corresponding detail view with the appropriate filter pre-set.

#### Scenario: Drill from load-by-user

- **WHEN** the cursor is on a Load-by-USER row and the user presses `enter`
- **THEN** the view SHALL change to Top SQL
- **AND** `m.topUser` SHALL be set to the selected user

#### Scenario: Drill from hottest table

- **WHEN** the cursor is on a Hottest Tables row and the user presses `enter`
- **THEN** the view SHALL change to the Tables tab
- **AND** the rows SHALL be filtered to the selected table

### Requirement: Graceful degradation

Panels for which no data source exists SHALL be removed entirely from the layout. Placeholders, em-dashes, or "N/A" rows SHALL NOT be rendered.

#### Scenario: No replica role detected

- **WHEN** `Probe()` reports no replica role
- **THEN** the Replication panel SHALL NOT be rendered
- **AND** the surrounding panels SHALL fill the freed width

#### Scenario: performance_schema disabled

- **WHEN** `performance_schema` is disabled at startup
- **THEN** the AAS sparkline and Load-by panel SHALL collapse to a single dim-text notice
- **AND** the status line, replication panel (if applicable), and live issues panel SHALL still render
