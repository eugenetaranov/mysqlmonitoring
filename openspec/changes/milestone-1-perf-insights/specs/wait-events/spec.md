## ADDED Requirements

### Requirement: Periodic wait‑event delta sampling

The system SHALL periodically read `performance_schema.events_waits_summary_global_by_event_name` and compute the delta versus the previous read for each event name, producing per‑interval samples containing `count_delta` and `sum_timer_wait_delta`. The poll interval SHALL match the digest collector's `--interval`.

#### Scenario: Subsequent poll emits per‑event deltas

- **WHEN** the wait collector reads the table on a non‑first poll and counters are non‑decreasing
- **THEN** the collector SHALL emit one sample per event name containing the count and time deltas and the wall‑clock interval

### Requirement: Wait‑class bucketing

The system SHALL classify each event name into exactly one of the following classes: `CPU`, `IO`, `Lock`, `Sync`, `Network`, or `Other`. Classification SHALL be deterministic and based on the event name prefix:

- `wait/io/file/*`, `wait/io/table/*` → `IO`
- `wait/lock/*` → `Lock`
- `wait/synch/mutex/*`, `wait/synch/rwlock/*`, `wait/synch/cond/*`, `wait/synch/sxlock/*` → `Sync`
- `wait/io/socket/*` → `Network`
- any other `wait/*` event → `Other`

The `CPU` class SHALL NOT be derived from `events_waits_*` (it represents non‑waiting time) and is produced by the session sampler defined below.

#### Scenario: Event names map to classes

- **WHEN** the bucketer receives event names `wait/io/file/innodb/innodb_data_file`, `wait/lock/table/sql/handler`, `wait/synch/mutex/sql/LOCK_open`, `wait/io/socket/sql/server_unix_socket`, and `wait/io/redo_log_flush`
- **THEN** they SHALL map to `IO`, `Lock`, `Sync`, `Network`, and `Other` respectively

### Requirement: CPU‑load sampling

The system SHALL periodically sample `performance_schema.events_statements_current` joined with `threads` for sessions whose `EVENT_NAME` indicates active statement execution and which have no current wait event. Each such observed session SHALL contribute one CPU‑class observation for the sampling interval.

#### Scenario: Active session with no wait

- **WHEN** a sample observes a session that is executing a statement and has no current wait event
- **THEN** the session SHALL contribute to the CPU class for that sample
- **AND** SHALL NOT contribute to any other class for that sample

### Requirement: Average Active Sessions per class

The system SHALL compute Average Active Sessions (AAS) per wait class per interval as `Σ sum_timer_wait_delta_for_class / Σ wall_picoseconds_in_interval`, using the event‑class bucketing above and the picoseconds units returned by `performance_schema`. CPU AAS SHALL be computed as `cpu_observation_count / sample_count_in_interval`.

#### Scenario: Stacked AAS for a window

- **WHEN** a 60‑second window is queried
- **THEN** the system SHALL return one stacked series with one value per class per sample tick
- **AND** the per‑tick sum across classes SHALL approximate the total Average Active Sessions for that tick

### Requirement: Graceful degradation on missing wait instruments

The system SHALL detect at startup whether the relevant wait instruments and consumers are enabled. If wait sampling is unavailable, the collector SHALL emit a single descriptive warning naming the disabled instruments and SHALL disable the wait collector without aborting the process.

#### Scenario: Wait instruments disabled

- **WHEN** every `wait/*` instrument is disabled at startup
- **THEN** the wait collector SHALL log one warning and SHALL not run
- **AND** other collectors SHALL continue to run
