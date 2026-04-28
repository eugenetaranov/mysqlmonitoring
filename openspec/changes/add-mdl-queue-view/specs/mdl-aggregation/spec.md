## ADDED Requirements

### Requirement: BuildMDL groups MetadataLocks by table

`insights.BuildMDL(snap db.Snapshot) MDLBreakdown` SHALL produce one `MDLQueue` per `(OBJECT_SCHEMA, OBJECT_NAME)` pair appearing in `snap.MetadataLocks`. Pending entries SHALL be sorted oldest-waiter first; granted entries SHALL be present in the `Granted` slice. Tables SHALL be sorted by `len(Pending)` descending, with ties broken by the oldest pending entry's `WaitSeconds`.

#### Scenario: Mixed granted and pending entries

- **WHEN** a snapshot contains 3 GRANTED + 47 PENDING entries on `shop.orders`
- **THEN** the resulting `MDLQueue` SHALL have `len(Granted) = 3` and `len(Pending) = 47`
- **AND** `Pending[0].WaitSeconds >= Pending[len-1].WaitSeconds`

#### Scenario: Multi-table ordering

- **WHEN** two tables have 47 and 12 pending entries respectively
- **THEN** the table with 47 pending SHALL appear before the table with 12

### Requirement: Queue position lookup

`MDLQueue.PositionOf(pid uint64)` SHALL return `(rank, total, ok)` where `rank` is the 1-indexed position within `Pending` of the entry whose `PID` matches `pid`, `total = len(Pending)`, and `ok` reports whether the PID was found.

#### Scenario: PID present in queue

- **WHEN** `pid = 8821` is at index 0 of a 132-entry pending list
- **THEN** `PositionOf(8821)` SHALL return `(1, 132, true)`

#### Scenario: PID not in queue

- **WHEN** `pid = 99999` does not appear in any pending entry
- **THEN** `PositionOf(99999)` SHALL return `(0, len(Pending), false)`

### Requirement: Blocker derivation via lock-type compatibility

`MDLQueue.BlockersOf(pid uint64)` SHALL return every `Granted` entry whose `LockType` is incompatible with the requested `LockType` of the pending entry identified by `pid`. Compatibility SHALL be determined by a static map mirroring the documented MDL conflict matrix.

#### Scenario: EXCLUSIVE waiter sees every granted holder

- **WHEN** a PENDING `EXCLUSIVE` request is made and the table has GRANTED `SHARED_READ` and `SHARED_WRITE` holders
- **THEN** `BlockersOf` SHALL return both granted holders

#### Scenario: SHARED_READ waiter sees only EXCLUSIVE holders

- **WHEN** a PENDING `SHARED_READ` request is made and the table has GRANTED `SHARED_READ`, `SHARED_WRITE`, and `EXCLUSIVE` holders
- **THEN** `BlockersOf` SHALL return only the `EXCLUSIVE` holder
