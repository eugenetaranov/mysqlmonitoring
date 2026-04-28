## Context

Operators hit metadata-lock stalls regularly: an `ALTER TABLE` blocks behind an open transaction holding a `SHARED_READ` lock; the queue grows; service degrades. Today the monitoring tool can detect that there's contention (the `ddl_conflict` detector emits an issue when it sees a session in "Waiting for table metadata lock" state) but cannot answer the operator's actual on-incident questions: how deep is the queue, where am I in it, and who is at the front.

This change makes those questions answerable in the TUI without leaving the tool.

## Goals

- Surface the existing `Snapshot.MetadataLocks` data (currently collected and discarded).
- Render the MDL grant queue per table with FIFO ordering by wait age — the same approximation `sys.innodb_lock_waits` uses.
- Show, for any PID in the queue, both its rank (`#47 of 132`) and the holders blocking the head.
- Add no new round-trips to MySQL on the per-poll path.

## Non-Goals

- Historical queue replay (would require a ring buffer of MDL snapshots — out of scope).
- Authoritative MDL conflict resolution (full MDL semantics involve priority upgrades and intent locks that vary by server version). The compatibility map is conservative; Phase 4 adds `sys.schema_table_lock_waits` when authoritative answers are wanted.
- Cross-server / replicated MDL inspection.
- Notifications when "your query reaches position 1" — operator polls.

## Decisions

### D1 — Fix the broken `MetadataLocks` query

Current at `internal/db/mysql.go:315-326`:

```sql
SELECT /*+ MAX_EXECUTION_TIME(5000) */
    COALESCE(THREAD_ID, 0),
    COALESCE(LOCK_TYPE, ''),
    COALESCE(LOCK_DURATION, ''),
    COALESCE(LOCK_MODE, ''),               -- ← does not exist
    COALESCE(OBJECT_TYPE, ''),
    COALESCE(OBJECT_SCHEMA, ''),
    COALESCE(OBJECT_NAME, '')
FROM performance_schema.metadata_locks
WHERE OBJECT_TYPE = 'TABLE'
ORDER BY THREAD_ID
```

Replacement (joins `threads` to pick up the matching `PROCESSLIST_*` columns, drops the bogus column, sorts so consumers can iterate in stable order):

```sql
SELECT /*+ MAX_EXECUTION_TIME(5000) */
    ml.OBJECT_TYPE, ml.OBJECT_SCHEMA, ml.OBJECT_NAME,
    ml.LOCK_TYPE, ml.LOCK_DURATION, ml.LOCK_STATUS,
    ml.OWNER_THREAD_ID,
    COALESCE(t.PROCESSLIST_ID, 0)        AS pid,
    COALESCE(t.PROCESSLIST_USER, '')     AS user,
    COALESCE(t.PROCESSLIST_HOST, '')     AS host,
    COALESCE(t.PROCESSLIST_TIME, 0)      AS time_seconds,
    COALESCE(t.PROCESSLIST_INFO, '')     AS info
FROM performance_schema.metadata_locks ml
LEFT JOIN performance_schema.threads t ON t.THREAD_ID = ml.OWNER_THREAD_ID
WHERE ml.OBJECT_TYPE = 'TABLE'
ORDER BY ml.OBJECT_SCHEMA, ml.OBJECT_NAME,
         FIELD(ml.LOCK_STATUS, 'GRANTED', 'PENDING'),
         t.PROCESSLIST_TIME DESC
```

The struct grows `LockStatus`, `User`, `Host`, `PID`, `TimeSeconds`, `Info`; `LockMode` is removed. The silent `return nil, nil` becomes a soft-fail that records a one-shot capability warning instead — silent failures are exactly what hid this bug.

### D2 — Aggregation as pure functions

`internal/insights/mdl.go` exposes:

```go
type MDLEntry struct {
    PID          uint64
    User, Host   string
    LockType     string  // SHARED_READ, EXCLUSIVE, …
    LockDuration string  // STATEMENT / TRANSACTION / EXPLICIT
    LockStatus   string  // GRANTED / PENDING
    WaitSeconds  int64
    Query        string
}

type MDLQueue struct {
    Schema, Name string
    Granted      []MDLEntry
    Pending      []MDLEntry              // sorted: oldest waiter first
    ByLockType   map[string]int          // pending count per LOCK_TYPE
}

type MDLBreakdown struct {
    Tables []MDLQueue                    // sorted by len(Pending) desc
}

func BuildMDL(snap db.Snapshot) MDLBreakdown
func (b MDLBreakdown) Find(schema, name string) *MDLQueue
func (q MDLQueue) PositionOf(pid uint64) (rank, total int, ok bool)
func (q MDLQueue) BlockersOf(pid uint64) []MDLEntry
```

`PositionOf` is 1-indexed within `Pending`. `BlockersOf` returns every `Granted` entry whose `LockType` is incompatible with the waiter's requested type, using a static compatibility map. Pure functions over snapshot — zero new round-trips.

### D3 — `ViewMDL` (key `M`) — list + detail

**List mode** opens by default. Top-N tables by queue depth:

```
TABLE                       PEND  GRANT  TYPES                                OLDEST
shop.orders                  47    3     38×SHARED_READ 6×SHARED_WRITE …      4m12s
auth.sessions                12    1     12×INTENTION_EXCLUSIVE                47s
analytics.events              3    1     2×EXCLUSIVE 1×SHARED_UPGRADABLE       2m
```

`enter` on a row → detail mode for that table.

**Detail mode** — split panel with QUEUE (waiters) above HOLDERS (granted):

```
shop.orders   47 waiters · 3 holders · longest wait 4m12s · queue oldest-first

QUEUE
   #  AGE     PID     USER@HOST                LOCK              QUERY
>  1  4m12s   8821    app_rw@10.0.4.12         EXCLUSIVE         ALTER TABLE shop.orders ADD INDEX …
   2  3m48s   8845    app_rw@10.0.4.13         SHARED_WRITE      INSERT INTO shop.orders …
   …
  47    8s    9201    app_ro@10.0.4.21         SHARED_READ       SELECT id FROM shop.orders …

HOLDERS  (granted, blocking the head of the queue)
   PID     USER@HOST              LOCK              DURATION       QUERY
   8714    app_rw@10.0.4.12       SHARED_READ       TRANSACTION    SELECT * FROM shop.orders WHERE …
   8717    cron@10.0.6.4          SHARED_READ       TRANSACTION    (idle in trx for 6m)
```

Cursor in QUEUE: `B` filters HOLDERS to only the entries blocking the cursor's lock-type; `K` kills the cursor's PID via the existing `m.confirmKill` flow; `/` searches for a PID (useful for "where is my ALTER in this 132-row queue?"); `enter` opens a full-SQL modal.

### D4 — Drill from Overview's Hottest Tables

The Overview panel `renderHottestTables` becomes cursor-aware (reusing the cursor pattern from `Tables`); `enter` sets `m.mdlTableFilter` and switches `m.view = ViewMDL`, `m.mdlMode = MDLDetail`. The existing detector-issue-based aggregation in that panel stays — the MDL drill is *additional*, not a replacement.

### D5 — Failure modes

| Missing | Behaviour |
|---|---|
| `wait/lock/metadata/sql/mdl` instrument disabled | One-shot stderr warning at startup; `M` tab renders a single dim line with the enable command; rest of the TUI works unchanged. |
| MariaDB without `sys.schema_table_lock_waits` | Blocker derivation falls back to the compatibility map; works for all MDL types, just less authoritative. |
| Aurora with empty `metadata_locks` | Same — empty queue + warning. |

### D6 — Why a separate view, not extend Tables

The existing Tables tab is detector-issue-centric: it groups `detector.Issue` by table and shows severity / age. MDL queue depth is a different aggregation (over `metadata_locks` rows, not issues), with different sort order (queue depth, not severity), and different drill semantics (queue position, not issue detail). Smashing them together would coarsen both. A separate view with consistent column language is cleaner.

### D7 — Compatibility map vs `sys.schema_table_lock_waits`

`sys.schema_table_lock_waits` is the authoritative source for blocker chains, but it's a sys schema view that may not exist on hardened or MariaDB deployments. The compatibility map is conservative (it tags more entries as blockers than the sys view would) but always works. Phase 1-3 ship the compatibility-map version; Phase 4 (deferred) adds the sys view as the preferred source when present.

## Risks

- **Conservative blocker reporting.** The compatibility map may flag a `SHARED_READ` holder as blocking another `SHARED_READ` waiter when the actual holder of an `EXCLUSIVE` (somewhere else in the chain) is the real culprit. Mitigation: the QUEUE display is the primary source of truth; HOLDERS is supplementary. Phase 4 adds the authoritative path.
- **Probe noise.** If `wait/lock/metadata` is off, every snapshot query returns empty rows — but the warning fires only once at startup. Operators on default-MySQL-8.0 see the warning the first time and act on it.
- **MariaDB column variance.** MariaDB's `metadata_locks` exists but a few column names may diverge across versions. The query uses `OWNER_THREAD_ID` (standard since MySQL 5.7.3 and MariaDB 10.0); other columns are common.

## Migration Plan

This is a side fix + three new capabilities. No data migration. The fix turns a silently-broken query into a working one — operators who somehow relied on the empty-rows behaviour (none we know of) would just start seeing real data.

## Open Questions

None at proposal time. The repro scenario from the user's incident is the verification gold standard.
