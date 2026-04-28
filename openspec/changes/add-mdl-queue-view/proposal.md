## Why

A production `ALTER TABLE` recently sat for several minutes "waiting for table metadata lock" before being killed. The tool today surfaces InnoDB row-lock contention via the `lock_chain` detector but has no visibility into the MDL grant queue. `Snapshot.MetadataLocks` is collected and then thrown away; the TUI never reads it.

This change answers four questions on demand for any table:

1. **Hottest tables** — sorted by current MDL queue depth (count of `LOCK_STATUS='PENDING'`) and tiebroken by oldest waiter.
2. **Lock-type breakdown** — for a given table, how many waiters per `LOCK_TYPE` bucket (`SHARED_READ`, `SHARED_WRITE`, `EXCLUSIVE`, `SHARED_UPGRADABLE`, `INTENTION_EXCLUSIVE`, …).
3. **Queue position** — for any waiting PID, "you are 47th of 132 waiters", plus the lock types of waiters ahead.
4. **Direct blockers** — what specific GRANTED holders are stopping the front of the queue, with PID, lock type, and the SQL they're sitting on.

A side fix lands here too: the current `MetadataLocks` query selects a non-existent column (`LOCK_MODE`) — the right column on `performance_schema.metadata_locks` is `LOCK_TYPE`. Today the query likely errors on every snapshot, which is silently swallowed by the `if err != nil { return nil, nil }` shortcut at `internal/db/mysql.go:330`. The MDL feature has been quietly disabled.

## What Changes

- **Fix** the `MetadataLocks` query: drop `LOCK_MODE`, select `LOCK_STATUS` (PENDING vs GRANTED), join `performance_schema.threads` to `information_schema.PROCESSLIST` to pick up the user, host, query text, and wait time per holder/waiter.
- **Probe** `wait/lock/metadata/sql/mdl` instrumentation at startup; surface a one-shot warning if it's off (default on MySQL 5.7 / 8.0 LTS) with the exact `UPDATE setup_instruments` to enable it.
- **Add** an `insights.MDLBreakdown` aggregator: pure in-memory functions that group `Snapshot.MetadataLocks` by table, sort waiters by wait age, return queue position for a PID, and derive blockers via a static lock-type compatibility map.
- **Add** a new `ViewMDL` TUI tab (key `M`) with two modes: a hottest-tables list and a per-table detail view showing the FIFO queue with rank annotations + the holders blocking it.
- **Wire** the Overview's existing "Hottest Tables" panel: cursor + `enter` drills into `ViewMDL` detail filtered to the selected table.
- **Optional Phase 4** (deferred unless feedback warrants it): authoritative blocker chains via `sys.schema_table_lock_waits` when the sys schema is available.

## Capabilities

### New Capabilities

- `mdl-collection`: Fix and extend the `MetadataLocks` query so it actually runs; probe and report the `wait/lock/metadata` instrument enablement.
- `mdl-aggregation`: Pure-Go aggregation over `Snapshot.MetadataLocks` providing `BuildMDL`, `MDLQueue.PositionOf`, `BlockersOf`, and a lock-type compatibility table.
- `mdl-tui-view`: New `M` tab with list + detail modes; drill from Overview's Hottest Tables panel.

### Modified Capabilities

(None — the existing `ddl_conflict` detector keeps surfacing issues; this view exposes the *queue* underneath those issues.)

## Impact

- **Code**: new `internal/insights/mdl.go`, new `internal/tui/mdl.go`, additions to `internal/db/{db.go,mysql.go,perf_insights.go}`, small wiring in `internal/tui/{model.go,tabs.go,views.go,overview.go}`.
- **Dependencies**: none.
- **DB load**: no new query in the per-poll path. The fix turns the existing `MetadataLocks` query (already on the lock-monitor poll) from "silently failing" into "actually returning rows", with two extra columns from a JOIN that already exists. Net cost is unchanged.
- **Compatibility**: graceful degradation when `wait/lock/metadata` is disabled (notice line, no empty boxes); MariaDB works (`metadata_locks` exists; `sys.schema_table_lock_waits` usually doesn't, so blocker derivation falls back to the compatibility map).
- **UX**: existing tabs unchanged. New `M` tab is opt-in via key.
