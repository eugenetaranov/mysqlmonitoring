## ADDED Requirements

### Requirement: SessionSample carries User and Host

`series.SessionSample` SHALL include `User` and `Host` fields populated from the session's row in `performance_schema.threads` (the `processlist_user` and `processlist_host` columns).

#### Scenario: Sample emitted

- **WHEN** the CPU sampler emits a `SessionSample` for an executing thread
- **THEN** `Sample.User` SHALL equal the thread's `processlist_user`
- **AND** `Sample.Host` SHALL equal the thread's `processlist_host`

#### Scenario: Anonymous session

- **WHEN** the thread row has NULL `processlist_user` and `processlist_host` (typical for system / replication threads)
- **THEN** `Sample.User` SHALL be the empty string
- **AND** `Sample.Host` SHALL be the empty string

### Requirement: Group-by aggregator

The system SHALL provide `insights.LoadByGroup(samples, now, window, key) []GroupLoad` that returns top-N AAS groups for `key` ∈ {user, host, schema}. The function SHALL operate purely in memory over an existing `RingSink[SessionSample]`; it SHALL NOT issue any database queries.

#### Scenario: Group-by user

- **WHEN** the caller invokes `LoadByGroup(samples, now, 5*time.Minute, GroupKeyUser)`
- **THEN** the result SHALL be sorted by AAS descending
- **AND** the sum of returned group AAS SHALL equal the global AAS for the window within rounding error

#### Scenario: Empty samples

- **WHEN** `LoadByGroup` is invoked over a window with no samples
- **THEN** it SHALL return an empty slice
- **AND** SHALL NOT return an error

#### Scenario: Single dominant group

- **WHEN** all samples in the window have the same `User`
- **THEN** `LoadByGroup(..., GroupKeyUser)` SHALL return exactly one entry whose AAS equals the global AAS for the window
