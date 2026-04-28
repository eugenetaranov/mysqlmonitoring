## ADDED Requirements

### Requirement: Periodic digest delta sampling

The system SHALL periodically read `performance_schema.events_statements_summary_by_digest` and compute the delta versus the previous read for each `(SCHEMA_NAME, DIGEST)` pair, producing per‑interval samples that include `exec_count_delta`, `sum_timer_wait_delta`, `sum_lock_time_delta`, `sum_rows_examined_delta`, `sum_rows_sent_delta`, `sum_no_index_used_delta`, `sum_created_tmp_disk_tables_delta`, and `sum_sort_merge_passes_delta`.

The poll interval SHALL be configurable via `--interval` (default 10s), with a minimum of 1s.

#### Scenario: First poll establishes a baseline

- **WHEN** the collector starts and reads the digest table for the first time
- **THEN** no samples SHALL be emitted for that interval
- **AND** the read values SHALL be retained as the baseline for the next poll

#### Scenario: Subsequent poll emits deltas

- **WHEN** a digest is present in two consecutive polls and all monotonic counters are non‑decreasing
- **THEN** the collector SHALL emit one sample for that digest containing the per‑field differences and the wall‑clock interval in seconds

### Requirement: Counter‑reset handling

The system SHALL detect a counter reset for a digest — defined as any monotonic counter (`COUNT_STAR`, `SUM_TIMER_WAIT`, `SUM_ROWS_EXAMINED`, `SUM_ROWS_SENT`, `SUM_LOCK_TIME`) decreasing between polls — and SHALL skip emitting a sample for that digest for that interval, re‑seeding the baseline from the new read. A counter reset SHALL NOT terminate the collector.

#### Scenario: Server restart between polls

- **WHEN** every monotonic counter for a digest decreases between two consecutive polls
- **THEN** the collector SHALL discard the interval and re‑seed its baseline
- **AND** the collector SHALL continue running

#### Scenario: Performance schema digest table truncated

- **WHEN** an operator truncates `events_statements_summary_by_digest` between polls and a digest's counters drop to zero
- **THEN** the collector SHALL treat that digest as a counter reset
- **AND** subsequent polls SHALL produce normal deltas

### Requirement: Tracked‑digest cap

The system SHALL bound the number of digests held in memory by a configurable cap (`--max-digests`, default 2000). When the cap is reached, the collector SHALL evict the digest with the lowest aggregate load (sum of `sum_timer_wait_delta` over the in‑memory window) before adding a new digest.

#### Scenario: Cap exceeded

- **WHEN** a new digest appears and the tracked map already holds `--max-digests` entries
- **THEN** the collector SHALL evict the digest with the lowest aggregate load over the current in‑memory window
- **AND** the new digest SHALL be added with a fresh baseline

### Requirement: Digest text resolution

The system SHALL resolve and cache `DIGEST_TEXT` (the normalized SQL with `?` placeholders) for each tracked digest, alongside `SCHEMA_NAME` and `FIRST_SEEN`. The system SHALL NOT capture `SQL_TEXT` containing literal values into the digest cache; literal `SQL_TEXT` is only retrieved on user request via `explain-on-demand`.

#### Scenario: New digest appears

- **WHEN** the collector observes a digest for the first time
- **THEN** the digest's normalized text and schema SHALL be stored in the in‑memory digest registry
- **AND** the registry entry SHALL never contain literal parameter values

### Requirement: Ranked top‑SQL aggregation

The system SHALL aggregate per‑digest samples over a caller‑specified window (default: the full in‑memory window) and SHALL expose ranking by Average Active Sessions (AAS), executions per second, p50/p95/p99 latency, total rows examined, and rows‑examined‑to‑rows‑sent ratio. AAS for a digest SHALL be computed as `Σ sum_timer_wait_delta / Σ wall_seconds` over the window.

#### Scenario: Top‑by‑AAS

- **WHEN** an aggregation is requested with rank key "aas" over a 5‑minute window
- **THEN** digests SHALL be returned in descending AAS order
- **AND** each entry SHALL include the digest text, schema, calls/s, AAS, p50, p95, rows examined, and rows examined per row sent

### Requirement: Graceful degradation on missing instruments

The system SHALL detect at startup whether `events_statements_summary_by_digest` is populated (consumer `statements_digest` enabled and statement instruments active). If digest data is unavailable, the collector SHALL emit a single descriptive warning to stderr and SHALL disable digest sampling without aborting the process.

#### Scenario: Statement digest consumer disabled

- **WHEN** `setup_consumers.NAME = 'statements_digest'` is `NO` at startup
- **THEN** the collector SHALL log one warning naming the disabled consumer and the SQL the operator can run to enable it
- **AND** the digest collector SHALL not run for the lifetime of the process
