## ADDED Requirements

### Requirement: Periodic health-vitals collection

The system SHALL run a health collector at a configurable interval (default 5 seconds) and SHALL publish a `HealthSnapshot` consumed by the TUI Overview. The collector SHALL issue at most two `SHOW` statements per poll: one `SHOW GLOBAL STATUS WHERE Variable_name IN (…)` covering all required counters, and one optional `SHOW REPLICA STATUS` / `SHOW SLAVE STATUS` issued only when `Probe()` detected a replica role.

#### Scenario: Collected counters

- **WHEN** the collector polls
- **THEN** it SHALL issue a single `SHOW GLOBAL STATUS WHERE Variable_name IN (…)` query
- **AND** the resulting snapshot SHALL contain `Threads_running`, `Threads_connected`, `Innodb_buffer_pool_pages_dirty`, `Innodb_buffer_pool_pages_total`, and `Aborted_clients` (as a delta against the prior sample)

#### Scenario: Replica status when configured

- **WHEN** the collector polls AND `Probe()` previously detected a replica role
- **THEN** the collector SHALL issue `SHOW REPLICA STATUS` (or `SHOW SLAVE STATUS` on older versions, as detected at probe time)
- **AND** the snapshot SHALL contain seconds_behind_source, IO thread state, SQL thread state, GTID gap

#### Scenario: No replica configured

- **WHEN** `Probe()` did not detect a replica role
- **THEN** the collector SHALL skip the replica query entirely
- **AND** `HealthSnapshot.Replica` SHALL be nil

#### Scenario: Counter reset on server restart

- **WHEN** `Aborted_clients` decreases between two consecutive polls (server restart)
- **THEN** the delta SHALL be reported as 0 for that interval and a new baseline SHALL be established

### Requirement: History List Length parsing

The system SHALL populate `db.InnoDBStatus.HistoryListLength` from the existing `SHOW ENGINE INNODB STATUS` output. NO additional query SHALL be issued for this value.

#### Scenario: HLL extracted from existing output

- **WHEN** `ParseInnoDBStatus` is invoked on a status string containing `History list length 12345`
- **THEN** the resulting `InnoDBStatus.HistoryListLength` SHALL equal 12345

#### Scenario: HLL absent from output

- **WHEN** the status string does not contain a recognisable `History list length` line
- **THEN** `HistoryListLength` SHALL be 0
- **AND** the parse SHALL NOT return an error

### Requirement: Capability probe extension

`Probe()` SHALL extend its capability detection to include replica role and the syntactic dialect (`SHOW REPLICA STATUS` vs `SHOW SLAVE STATUS`). The probe SHALL run once at startup; results SHALL be cached for the process lifetime.

#### Scenario: Replica role detected on MySQL 8.0+

- **WHEN** `Probe()` runs against MySQL 8.0+ that has a non-empty `SHOW REPLICA STATUS` output
- **THEN** the probe SHALL record the role as replica
- **AND** SHALL record the dialect as `SHOW REPLICA STATUS`

#### Scenario: Replica role detected on MariaDB / older MySQL

- **WHEN** `Probe()` runs against MariaDB or MySQL < 8.0.22 with non-empty `SHOW SLAVE STATUS` output
- **THEN** the probe SHALL record the role as replica
- **AND** SHALL record the dialect as `SHOW SLAVE STATUS`

#### Scenario: Standalone server

- **WHEN** `Probe()` runs against a server with empty replica status
- **THEN** the probe SHALL record the role as standalone
- **AND** the health collector SHALL NOT issue replica-status queries
