## Why

Today `mysqlmonitoring` reports lock contention well, but gives no answer to the most common operator question: "what is the database actually spending its time on right now, and which queries are causing it?" That question is the entire value proposition of SolarWinds DPA and AWS RDS Performance Insights. Closing the gap unlocks the project's stated goal of becoming a self‑hosted, CLI‑first alternative — without taking on a database, an agent on the DB host, or a web UI. M1 is the smallest scope that makes the tool feel like a performance‑insights product instead of a lock dashboard.

## What Changes

- Add a query‑digest collector that diff‑samples `performance_schema.events_statements_summary_by_digest` and ranks SQL by load, latency, and rows examined.
- Add a wait‑event collector that buckets `events_waits_*` deltas into wait classes (CPU / IO / Lock / Sync / Network / Other) and computes Average Active Sessions per class.
- Add a session sampler that snapshots `events_statements_current` joined with session attributes for app tagging.
- Slice every aggregation by application using `session_connect_attrs` (`program_name`) and leading sqlcommenter SQL comments.
- Add on‑demand `EXPLAIN FORMAT=JSON` for any digest, with red‑flag callouts and plan caching for the TUI session.
- Add `top` and `load` CLI subcommands (one‑shot, JSON or text) and integrate the same views into the existing TUI as new panels with a stacked DB‑load sparkline header.
- Hold all series data in fixed‑size in‑memory ring buffers; no on‑disk store, no new heavy dependency. When the process exits, history is discarded.
- Existing detectors (lock chain, long transaction, DDL conflict, deadlock) are unaffected.

## Capabilities

### New Capabilities

- `digest-sampling`: Periodically diff `events_statements_summary_by_digest` and expose ranked per‑digest series (calls/s, AAS, latency, rows examined, no‑index‑used count, tmp‑disk tables, sort‑merge passes).
- `wait-events`: Periodically diff `events_waits_summary_global_by_event_name` plus sample `events_statements_current`, classify into wait classes, and expose a DB‑load‑by‑class series.
- `app-tagging`: Resolve an application tag for each session from `session_connect_attrs.program_name` and from leading sqlcommenter SQL comments, and propagate the tag to digest and wait aggregations.
- `explain-on-demand`: Pull a recent example for a digest from `events_statements_history_long`, run `EXPLAIN FORMAT=JSON` on a read‑only connection with a server‑side timeout, render the plan with red‑flag callouts, and cache by `(digest, plan_hash)` for the process lifetime.
- `perf-insights-cli`: Add `top` and `load` subcommands and the corresponding TUI panels and DB‑load sparkline header, all reading from the in‑memory series.

### Modified Capabilities

(None — this milestone adds new collectors and views alongside the existing lock/long‑tx/DDL/deadlock pipeline; their requirements are unchanged.)

## Impact

- **Code**: new `internal/series/` (ring buffer + per‑series types), `internal/collector/` (digest, waits, sessions), additions to `internal/db/` queries, `internal/tui/` new views and sparkline header, `cmd/mysqlmonitoring/` new subcommands.
- **Dependencies**: none beyond the Go standard library and what is already vendored. No SQLite, no time‑series DB, no Prometheus client.
- **Runtime**: a single process; collectors run inside the existing monitor poll loop. Memory bounded by `--window` × `--interval` × tracked‑digest cap.
- **DB load**: small additional read cost on `performance_schema` (~5 queries per poll); `EXPLAIN` runs only on user request.
- **Compatibility**: requires `performance_schema` enabled with statement and wait instruments on (default in MySQL 5.7+). Collectors degrade gracefully when consumers/instruments are off — they emit a one‑time warning and skip the affected series.
- **Backwards‑compat**: no breaking changes; existing TUI and CLI flags continue to work.
