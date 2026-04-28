## Why

The TUI today opens on the Issues tab — a list of detector output. That answers "what's wrong now?" but not "is the database healthy?" An operator paged at 3am needs the latter first, in the first five seconds of looking at the screen.

The data infrastructure to answer "is it healthy" mostly exists already (`internal/insights`, `internal/series`, `internal/collector` from M1) but is currently surfaced only behind the `t Top SQL` tab. The remaining gaps are health vitals (`SHOW GLOBAL STATUS` cherry-picks, replica state, InnoDB History List Length) and per-user/host load attribution.

## What Changes

- Add a new **Overview** tab (`O`), positioned leftmost, and make it the default launch view.
- Render a single-line health verdict (`[HEALTHY]` / `[WARN]` / `[PAGE]`) with paired color and word so the severity is unambiguous on screenshots and for colorblind operators.
- Reuse the existing DB-load sparkline + wait-class legend from `internal/tui/perf.go` as the central chart.
- Add a "Load by …" panel that cycles between USER / HOST / SCHEMA via `u` / `h` / `s` keys; `enter` drills into Top SQL with the appropriate filter pre-set.
- Add a Replication panel (replica role, IO/SQL thread state, lag, GTID gap) that is rendered only when `Probe()` detects a replica role; otherwise removed entirely.
- Add Hottest Queries (by `SUM_TIMER_WAIT` delta) and Hottest Tables panels.
- Embed a compact Live Issues panel from the existing detector output.
- Add a health-vitals collector (new) for `Threads_running`, `Threads_connected`, `Innodb_buffer_pool_pages_dirty/total`, `Aborted_clients` (delta), plus `SHOW REPLICA STATUS` when applicable. Single `SHOW GLOBAL STATUS … IN (…)` query per poll; replica query conditional.
- Parse InnoDB History List Length from the existing `SHOW ENGINE INNODB STATUS` output — no new query.
- Enrich `series.SessionSample` with `User` and `Host` populated from the existing `performance_schema.threads` JOIN — no new query, no new round-trip.

## Capabilities

### New Capabilities

- `tui-overview`: Overview tab as default launch view, status verdict line, load-attribution panel toggles, drill-down into Top SQL, graceful panel removal when data sources are missing.
- `health-vitals`: Periodic collection of `SHOW GLOBAL STATUS` cherry-picks and conditional `SHOW REPLICA STATUS`; HLL parsing from existing InnoDB status output.
- `load-attribution`: `User`/`Host` on `SessionSample`; `LoadByGroup` aggregator that returns top-N AAS groups for user/host/schema.

### Modified Capabilities

(None — this change is additive. The existing tabs, detectors, and collectors are unchanged.)

## Impact

- **Code**: new `internal/collector/health.go`, new `internal/tui/overview.go`, additions to `internal/series/sample.go`, `internal/db/perf_insights.go`, `internal/db/innodb_status_parser.go`, `internal/db/mysql.go`, `internal/insights/aggregate.go`, `internal/insights/insights.go`, and small touch-ups to `internal/tui/{model.go,tabs.go,views.go,issues.go}`.
- **Dependencies**: none beyond the Go standard library and what M1 already vendored.
- **DB load**: at most two extra `SHOW` statements per poll-interval (typically 5s). The replica `SHOW` is conditional. HLL is free (parsed from output already polled). User/Host enrichment is free (extra columns on an existing JOIN). Total cost is well below noise.
- **Compatibility**: no breaking changes; the Overview view degrades gracefully when `performance_schema` is off (collapses to a notice line and still renders vitals + replication + issues), when no replica is configured (replication panel removed), or on MariaDB (load panel falls back to count-by-user with a relabelled column).
- **UX**: existing users land on a different default tab; `I` still gets them straight to Issues.
