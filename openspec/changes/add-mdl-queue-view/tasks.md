## 1. Plumbing — fix MetadataLocks query and probe

- [x] 1.1 Fix the `MetadataLocks` SQL in `internal/db/mysql.go`: drop `LOCK_MODE`, select `LOCK_STATUS`, `OWNER_THREAD_ID`, JOIN `performance_schema.threads` for `PROCESSLIST_ID`/`USER`/`HOST`/`TIME`/`INFO`, ORDER BY schema/name/granted-first/wait-age-desc.
- [x] 1.2 Update `db.MetadataLock` struct in `internal/db/db.go`: drop `LockMode`, add `LockStatus`, `User`, `Host`, `PID`, `TimeSeconds`, `Info`. Updated Scan binding in `mysql.go`. No external consumers referenced `LockMode` (the only consumer, `findPotentialBlockers` in `internal/detector/ddl_conflict.go`, never read the field).
- [x] 1.3 Replaced the silent `return nil, nil` with `return nil, fmt.Errorf("metadata locks: %w", err)`.
- [x] 1.4 Extended `ProbeCapabilities` in `internal/db/perf_insights.go` with `MDLAvailable` and `mdlInstrumentEnabled(ctx)` helper. Appends a one-shot warning with the exact `UPDATE setup_instruments` SQL when off.
- [x] 1.5 Existing test suite (`go test ./internal/db/...`) passes. Real MySQL 8.0 integration test (`TestLockDetection_MySQL80`) passes — proves the new query parses on a live server.

## 2. Aggregation

- [x] 2.1 Created `internal/insights/mdl.go` with `MDLEntry`, `MDLQueue`, `MDLBreakdown`; `BuildMDL`, `Find`, `PositionOf`, `BlockersOf`; static `mdlCompat` map. Pending sorted oldest-first; granted sorted by holder age desc; tables sorted by queue depth then oldest pending wait age. Conservative `conflicts` reports true on unknown lock types so we never mislabel a hidden blocker as harmless.
- [x] 2.2 13 tests covering: empty snap, non-table entries skipped, group-and-split holders/waiters, sort-by-depth-then-age tiebreak, position-of-found, position-of-missing, head-of-queue, EXCLUSIVE-sees-every-holder, SHARED_READ-only-blocked-by-EXCLUSIVE-types, PID-not-pending-returns-nil, unknown-lock-type-is-conservative, find-missing-table, transient-states-dropped (VICTIM/TIMEOUT).

## 3. TUI

- [ ] 3.1 Add `ViewMDL` constant to `internal/tui/model.go`; new fields `mdlMode` (list/detail), `mdlTableFilter` (set by drill), `mdlCursor`, `mdlSearchPID`. Add `M` keybinding.
- [ ] 3.2 Prepend `{"M","MDL",ViewMDL}` after Overview to `orderedTabs` in `internal/tui/tabs.go`.
- [ ] 3.3 Add `case ViewMDL:` in `renderMain` switch in `internal/tui/views.go`.
- [ ] 3.4 Create `internal/tui/mdl.go`: `renderMDLList` (top-N hottest), `renderMDLDetail` (queue + holders), `handleMDLKey` (j/k/g/G/enter/B/K/`/`/esc).
- [ ] 3.5 Make `renderHottestTables` in `internal/tui/overview.go` cursor-aware; wire `enter` on a row to set `m.mdlTableFilter`, `m.mdlMode = MDLDetail`, and switch view.
- [ ] 3.6 Snapshot tests in `internal/tui/mdl_test.go`: list view with several tables, detail view with cursor on row 23/132, blocker filter on cursor's lock type, `/` search-PID jumps cursor, MDL-instrument-disabled notice.

## 4. Optional — sys.schema_table_lock_waits (deferred)

- [ ] 4.1 Add `MySQLDB.MDLBlockerChain(ctx, schema, name)` reading the sys view; cache "available" result on Probe.
- [ ] 4.2 `MDLQueue.BlockersOf` prefers chain data when present, falls back to compatibility map.

## 5. Docs / shipping

- [ ] 5.1 README: new "MDL queue (key M)" section. Lists the four operator questions and the keys that answer each. Updates the TUI Controls table.
- [ ] 5.2 CHANGELOG: "Added — MDL queue view (key M)" plus a separate "Fixed — metadata_locks query selected non-existent LOCK_MODE column; the feature was silently disabled" line.
- [ ] 5.3 Live smoke against MySQL 8.0 container per design-doc verification scenario.
