## ADDED Requirements

### Requirement: MetadataLocks query returns waiter and holder context

The `MetadataLocks(ctx)` method on `db.DB` SHALL return one row per non-empty entry in `performance_schema.metadata_locks` for tables, including the LOCK_STATUS (GRANTED / PENDING) plus the matching session's processlist user, host, current SQL, and wait time, joined via `performance_schema.threads.OWNER_THREAD_ID`.

#### Scenario: Granted lock with active query

- **WHEN** a session holds a GRANTED `SHARED_READ` MDL on `shop.orders`
- **THEN** the result SHALL include one entry with `LockStatus = "GRANTED"`, `LockType = "SHARED_READ"`, the session's `PID`, `User`, `Host`, `Info` (SQL text), and `TimeSeconds` (current statement age)

#### Scenario: Pending lock waiting in queue

- **WHEN** a session is queued PENDING `EXCLUSIVE` on `shop.orders` for 47 seconds
- **THEN** the result SHALL include one entry with `LockStatus = "PENDING"`, `LockType = "EXCLUSIVE"`, `WaitSeconds ≥ 47`, the session's `PID`/`User`/`Host`/`Info`

### Requirement: MDL instrumentation probe

`ProbeCapabilities` SHALL detect whether `wait/lock/metadata/sql/mdl` is enabled in `setup_instruments`. The result is exposed as `PerfCapabilities.MDLAvailable`. When the instrument is disabled, `ProbeCapabilities` SHALL append a one-shot warning naming the disabled instrument and the SQL to enable it.

#### Scenario: Instrument disabled (MySQL 8.0 LTS default)

- **WHEN** `wait/lock/metadata/sql/mdl` has `ENABLED='NO'`
- **THEN** `MDLAvailable` SHALL be false
- **AND** the probe warnings SHALL contain `UPDATE performance_schema.setup_instruments SET ENABLED='YES', TIMED='YES' WHERE NAME='wait/lock/metadata/sql/mdl'`

#### Scenario: Instrument enabled

- **WHEN** the instrument has `ENABLED='YES'`
- **THEN** `MDLAvailable` SHALL be true
- **AND** no warning SHALL be appended

### Requirement: Surface query errors instead of silently swallowing

When `MetadataLocks(ctx)` fails for any reason other than the table being unavailable, the error SHALL be returned to the caller. The previous behaviour of silently returning `nil, nil` is removed because it hid a long-standing bug where the query referenced a non-existent column.

#### Scenario: Genuine query failure

- **WHEN** the query fails with a non-recoverable error (privilege, syntax, etc.)
- **THEN** `MetadataLocks` SHALL return a non-nil error
- **AND** SHALL NOT return `nil, nil`
