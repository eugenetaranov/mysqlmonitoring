## ADDED Requirements

### Requirement: Recent‑example retrieval

On user request for a given digest, the system SHALL select the most recent matching row from `performance_schema.events_statements_history_long` that has a non‑empty `SQL_TEXT` and SHALL use that row's `SQL_TEXT` and `CURRENT_SCHEMA` as the explain target. If no example is available, the system SHALL report a clear error and SHALL NOT attempt to fabricate a query from the digest text.

#### Scenario: Example available

- **WHEN** the user requests EXPLAIN for a digest and `events_statements_history_long` contains at least one row with that digest
- **THEN** the system SHALL select the most recent row by `TIMER_END`
- **AND** SHALL use its `SQL_TEXT` and `CURRENT_SCHEMA` as the explain target

#### Scenario: No example available

- **WHEN** no row in `events_statements_history_long` matches the digest
- **THEN** the system SHALL report `no recent example for digest <digest>; enable consumer events_statements_history_long` and SHALL NOT call `EXPLAIN`

### Requirement: Read‑only explain execution

The system SHALL execute `EXPLAIN FORMAT=JSON` on a dedicated read‑only connection that has `transaction_read_only=ON` for the session. The statement SHALL be wrapped with a server‑side execution timeout of 2000 ms (using `MAX_EXECUTION_TIME(2000)` for `SELECT` or session `MAX_EXECUTION_TIME` where supported). The explain SHALL be aborted client‑side if it exceeds 5000 ms.

The system SHALL refuse to run `EXPLAIN` if the example statement is one of: `INSERT`, `UPDATE`, `DELETE`, `REPLACE`, `CALL`, `LOAD`, `GRANT`, `REVOKE`, `SET`, `LOCK`, `UNLOCK`, or any DDL. In that case the system SHALL fall back to displaying the digest's normalized text and any available statistics from `events_statements_summary_by_digest` and SHALL inform the user that EXPLAIN was skipped for safety.

#### Scenario: Read‑only SELECT

- **WHEN** the example statement is a `SELECT`
- **THEN** the system SHALL run `EXPLAIN FORMAT=JSON` against it on the read‑only connection
- **AND** the connection SHALL have `transaction_read_only=ON` for the duration of the call

#### Scenario: Write statement

- **WHEN** the example statement begins with `UPDATE`, `INSERT`, `DELETE`, or any DDL keyword
- **THEN** the system SHALL NOT call `EXPLAIN`
- **AND** SHALL display the digest text plus a notice that EXPLAIN was skipped for safety

#### Scenario: Server takes longer than the timeout

- **WHEN** the server does not return a plan within 5000 ms
- **THEN** the client SHALL abort the call
- **AND** SHALL report `explain timed out after 5s for digest <digest>`

### Requirement: Plan rendering with red‑flag callouts

The system SHALL parse the JSON plan into a tree and SHALL render it as an indented text view annotated with at least the following red flags when present in any node: `access_type = ALL` (full scan), `using_filesort`, `using_temporary_table`, `attached_condition` referencing an unindexed column, and `rows_examined_per_scan` greater than `100 × rows_produced_per_join`. Each red flag SHALL be attributed to the specific node in which it appears.

#### Scenario: Full scan flagged

- **WHEN** a plan node has `access_type = ALL` on a table with more than 1000 rows
- **THEN** the rendered output SHALL show a `FULL SCAN` annotation on that node

### Requirement: Plan caching and flip detection

The system SHALL hash each rendered JSON plan with a stable canonical form and SHALL cache plans keyed by `(digest, plan_hash)` for the lifetime of the process. When a subsequent EXPLAIN for the same digest produces a different `plan_hash`, the system SHALL emit a `plan_flip` event for that digest including both hashes and the timestamp of observation.

#### Scenario: Same plan twice

- **WHEN** EXPLAIN is requested twice for the same digest and both plans hash to the same value
- **THEN** the second call SHALL return the cached rendered plan without re‑running EXPLAIN
- **AND** SHALL NOT emit a `plan_flip` event

#### Scenario: Plan changes

- **WHEN** EXPLAIN is requested for a digest that already has a cached plan and the new `plan_hash` differs
- **THEN** the new plan SHALL be cached
- **AND** a `plan_flip` event SHALL be emitted with both hashes and the observation timestamp
