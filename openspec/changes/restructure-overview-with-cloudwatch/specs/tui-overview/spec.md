## MODIFIED Requirements

### Requirement: Status verdict line

A single verdict line at the top of the Overview body SHALL summarise server health using a verdict word (`[HEALTHY]` / `[WARN]` / `[PAGE]`) paired with a colour, plus key gauges. The line SHALL include the existing MySQL-side gauges (`AAS`, `Threads_running` / `max_connections`, `bp_hit`, `HLL`, replica lag, deadlock count) AND, when CloudWatch metrics are available, the leftmost gauge cluster SHALL include host CPU%, free memory %, and read/write IOPS — so the operator sees the host-level signal first.

The line MAY wrap across multiple terminal rows; gauges SHALL NOT be truncated.

#### Scenario: Healthy server, no CloudWatch

- **WHEN** the operator runs against a self-managed MySQL with no AWS context
- **THEN** the verdict line renders `[HEALTHY] AAS … running … bp_hit … HLL … repl … dl …`
- **AND** no CPU% or Mem% column is present

#### Scenario: Healthy server, CloudWatch wired

- **WHEN** the operator runs against an RDS instance with AWS credentials in the SDK default chain
- **AND** the CloudWatch collector has produced at least one successful sample
- **THEN** the verdict line renders `[HEALTHY] CPU 34% Mem 71% IOPS 1.2k/450 AAS … running … bp_hit … HLL … repl … dl …`

#### Scenario: CloudWatch CPU page threshold

- **WHEN** CloudWatch reports `CPUUtilization > 95`
- **THEN** the verdict word SHALL be `[PAGE]`
- **AND** the `CPU%` gauge SHALL render in red

### Requirement: Body layout

The Overview body SHALL be organised into four strips, top to bottom:

1. The verdict line (above).
2. The DB Load sparkline + per-class legend, rendered exactly once (the chrome no longer renders one).
3. A row of three side-by-side panels with a 60-second window: Top AAS queries, Top AAS users, Top busiest tables.
4. A bottom strip: Long transactions on the left; Replication on the right when applicable, otherwise Long transactions widens to full width.

#### Scenario: Three-panel row at default width

- **WHEN** the terminal is at least 120 columns wide
- **THEN** Top AAS queries, Top AAS users, and Top busiest tables SHALL render side-by-side at roughly equal widths
- **AND** each panel SHALL display its window in the header (`Top AAS queries (60s)`, etc.)

#### Scenario: Replication panel collapse

- **WHEN** `Probe()` reports no replica role
- **THEN** the bottom strip SHALL show only Long transactions, widened to the full Overview width
- **AND** no Replication chrome SHALL be rendered

### Requirement: 60-second window for top-N panels

The three top-N panels (queries, users, tables) on the Overview SHALL aggregate over the most recent 60 seconds of sample data. Operators wanting longer windows SHALL use the dedicated Top SQL tab (`t`).

#### Scenario: Burst load shows in queries panel within seconds

- **WHEN** a new high-AAS digest starts running
- **THEN** within 60 seconds it SHALL appear in the Top AAS queries panel without averaging-out against quiet baseline
- **AND** it SHALL persist in the panel only as long as it remains in the top-5 by AAS over the rolling 60s window

### Requirement: Long transactions panel

A Long transactions panel SHALL render up to the 5 oldest open transactions whose age is at least 30 seconds, sorted by age descending. Each row SHALL show age, PID, user, and a truncated SQL summary.

#### Scenario: Idle-in-trx detection

- **WHEN** a session opens a transaction, runs one statement, and then idles for 3 minutes 12 seconds
- **THEN** it SHALL appear in the Long transactions panel labelled `3m12s pid <pid> <user> idle in trx`
- **AND** the operator SHALL be able to see this without leaving the Overview tab

### Requirement: u/h/s keys repurposed

The `u`, `h`, `s` keys are repurposed: instead of cycling a load-attribution panel, they SHALL drill into Top SQL with the appropriate filter pre-set, scoped from the cursor row of the active top-N panel.

#### Scenario: Drill into Top SQL filtered by user

- **WHEN** the cursor is on the Top AAS users panel and the user presses `u` or `enter`
- **THEN** `m.topUser` SHALL be set to the selected user
- **AND** the view SHALL change to Top SQL

## REMOVED Requirements

### Requirement: Load attribution panel toggles

**Reason**: The mid-band Load-by-USER/HOST/SCHEMA panel that this requirement governed is removed in favour of the dedicated Top AAS users panel in the new three-column row. The cycling semantics (`u`/`h`/`s`) survive in repurposed form (drill-down filter), preserving muscle memory while adapting to the new layout.

**Migration**: Operators who used the cycle to compare grouping dimensions now do so via the dedicated Top SQL tab (`t`), which gains the per-user / per-host / per-schema breakdowns at a 1-hour window. The Overview's user dimension is fixed (the most-asked dimension by far in operator interviews); host and schema dimensions are one keystroke (`t`) and one filter (`h` or `s`) away.
