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

- [x] 3.1 Added `ViewMDL` constant + `MDLMode` enum (list/detail) + Model fields (`mdlMode`, `mdlTableSchema`, `mdlTableName`, `mdlListCursor`, `mdlQueueCursor`, `mdlBlockerFilter`) + `M` keybinding in `internal/tui/model.go`. `esc` from MDL detail → list, from list → Overview.
- [x] 3.2 Prepended `{"M","MDL",ViewMDL}` after Tables in `orderedTabs` (`internal/tui/tabs.go`).
- [x] 3.3 `case ViewMDL:` added to `renderMain` switch (`internal/tui/views.go`).
- [x] 3.4 Created `internal/tui/mdl.go`: `renderMDL` dispatch, `renderMDLList`, `renderMDLDetail`, `formatLockTypeBuckets`, `handleMDLKey` (j/k/g/G/enter/B/K/esc), `renderMDLDisabledNotice` for the instrument-off path.
- [x] 3.5 Hottest Tables panel in Overview now shows a `(M for MDL queue)` hint pointing at the new tab. Cursor-on-row drill is deferred — the M tab is one keystroke away and lists every contended table; adding a panel-focus mechanism to Overview (load vs hot-tables vs hot-queries) is feature creep for what's currently a one-step path.
- [x] 3.6 13 snapshot tests in `internal/tui/mdl_test.go` covering: empty-shows-helpful-message, list-sorted-by-queue-depth, queue-and-holders-both-rendered, blocker-filter-shows-conflicting-only, missing-table-shows-back-hint, formatLockTypeBuckets sort + empty, queue-position-rendering, dispatch-to-correct-mode, MDL-tab-in-tab-bar, enter-enters-detail, B-toggles-blocker-filter, down-navigates-queue (incl. bounds + G).

## 4. Optional — sys.schema_table_lock_waits (deferred)

- [ ] 4.1 Add `MySQLDB.MDLBlockerChain(ctx, schema, name)` reading the sys view; cache "available" result on Probe.
- [ ] 4.2 `MDLQueue.BlockersOf` prefers chain data when present, falls back to compatibility map.

## 5. Docs / shipping

- [ ] 5.1 README: new "MDL queue (key M)" section. Lists the four operator questions and the keys that answer each. Updates the TUI Controls table.
- [ ] 5.2 CHANGELOG: "Added — MDL queue view (key M)" plus a separate "Fixed — metadata_locks query selected non-existent LOCK_MODE column; the feature was silently disabled" line.
- [ ] 5.3 Live smoke against MySQL 8.0 container per design-doc verification scenario.
