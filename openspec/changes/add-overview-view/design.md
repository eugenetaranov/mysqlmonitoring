## Context

The TUI's current default tab (`Issues`) lists detector output. That is the right view *after* an operator knows something is wrong. The first question on launch — especially at 3am — is whether anything is wrong at all, and which dimension to look at next.

A team review (DBA + SRE + UX perspectives) on the parent design conversation converged on a single dense view that answers that question without scrolling, with drill-down into the existing detail tabs. This change is the implementation of that consensus.

User-confirmed constraints:

1. Overview is the new default launch view; no flag, no opt-in.
2. Prefer the lighter-on-DB collection path; only after that, prefer what's easier to maintain.
3. Minimum target width is 120 columns. No responsive stacking.

## Goals

- Render a "is it healthy?" answer in the first frame after launch.
- Reuse the existing AAS / wait-class infrastructure rather than introducing a parallel rendering pipeline.
- Add no new heavy dependency, no new configuration storage, and no new round-trips per poll beyond the absolute minimum.
- Degrade visibly but never silently: missing data sources remove panels, never render placeholders.

## Non-Goals

- Host CPU / memory metrics (`/proc`-style). This tool talks to MySQL.
- Heatmaps or per-second granularity (would require persistent timeseries storage).
- Configurable Overview layout. Ship one opinionated layout; iterate later if needed.
- Mobile / narrow-terminal responsive design. 120 columns is the floor.
- Replication topology graphs. One server, one replica state line.

## Decisions

### D1 — Default view flips to Overview, no flag

`internal/tui/model.go` `NewModel` currently sets `view: ViewIssues`. It will set `view: ViewOverview`. Per user direction, no flag — this is the new default. `internal/tui/tabs.go` `orderedTabs` gets `{"O","Overview",ViewOverview}` prepended; `Issues` keeps `I`.

Rationale: a flag is a half-commitment that prolongs the migration cost. The change is small, reversible, and the old behaviour is one keystroke away.

### D2 — All new collection is "lighter than noise"

The wishlist signals map to:

- `SHOW GLOBAL STATUS LIKE` for five named counters (in-memory lookups, sub-millisecond on any modern MySQL): `Threads_running`, `Threads_connected`, `Innodb_buffer_pool_pages_dirty`, `Innodb_buffer_pool_pages_total`, `Aborted_clients`. Single query.
- `SHOW REPLICA STATUS` (or `SHOW SLAVE STATUS` on older versions) — conditional on `Probe()` detecting a replica role.
- HLL: regex over the existing `SHOW ENGINE INNODB STATUS` output (already polled by the lock-monitor `Snapshot()` loop). Zero new queries.
- User/Host on `SessionSample`: two extra columns added to the existing `CurrentStatements` SELECT, which already JOINs `performance_schema.threads`. Zero new queries, zero new round-trips.
- `LoadByGroup`: pure in-memory aggregation over `RingSink[SessionSample]`. Zero DB cost.

Net per poll: at most two extra `SHOW` statements; the replica one is conditional. Well below noise.

### D3 — Health collector lives next to existing collector loops

`internal/insights/insights.go` already has `runDigestLoop`, `runWaitLoop`, `runCPULoop`. The health collector adds `runHealthLoop` alongside them — same pattern, same lifecycle, no new harness. `Insights.Config` gains `HealthInterval` (default 5s).

Rationale: a parallel collector framework would be more code to maintain than a fourth loop in the existing one. User stated maintenance preference (#2).

### D4 — Hide, don't grey

A signal with no source is removed from the layout, not displayed as `—` or `N/A`. `Probe()` extends to detect:

- Replica role (`@@server_id` + non-empty replica status).
- `performance_schema.threads` accessibility.
- `Innodb_buffer_pool_pages_*` presence (some forks omit them).

Each Overview panel renders only if its data is available; surrounding panels reflow to fill the gap. The layout is designed for the dense (everything-present) case; gaps make remaining panels wider, not stretched.

Cold-start (the first window of samples not yet collected) is a special case: the panel chrome renders with `gathering samples (0/N)…` so layout doesn't reflow when data lands.

### D5 — Status verdict is a word AND a color

```
[HEALTHY] up 14d  AAS 1.8/8c  running 3/512  bp_hit 99%  HLL 12k  repl +0s  dl 0
[WARN]    up 14d  AAS 6.8/8c↑ running 47/512 bp_hit 96%  HLL 2.1M↑ repl +12s↑ dl 0   ← lag rising
[PAGE]    up 14d  AAS 31/8c↑↑ running 890/512* HLL 12M↑↑                             ← over capacity
```

Color alone fails on screenshots, on phones with f.lux, and for colorblind operators. The word always carries severity; color reinforces it.

Thresholds (all reuse `--lock-wait-threshold` as the time-based knob to avoid adding new flags now):

- `[WARN]` if any: `Threads_running > 0.5 × max_connections`, replica lag `> threshold`, HLL `> 1M`, `Aborted_clients` delta in the window `> 0`.
- `[PAGE]` if any: `Threads_running > 0.8 × max_connections`, replica lag `> 5 × threshold`, HLL `> 5M`, lock chain with `>5` waiters or `>30s`.
- `[HEALTHY]` otherwise.

These thresholds are intentionally conservative and tunable in a follow-up if operators report false positives.

### D6 — Drill-down reuses existing filter machinery

Existing `m.topApp` / `m.topSchema` filters into the Top-SQL view via `enter` (`internal/tui/model.go:565`). This change mirrors with `m.topUser` and `m.topHost`. Cycling the load panel (`u` / `h` / `s`) switches both the displayed grouping AND which filter `enter` will apply. One state field (`loadGrouping`) drives both — no new view machinery.

Hot-tables drill reuses the existing `m.issuesTableFilter` path. Hot-queries drill jumps to the existing `ViewExplain`.

### D7 — 120 columns is the floor, not a target

The proposed layout uses 120 cols. Operators on narrower terminals will see truncated panel content but the layout structure remains intact (no stacking). This matches user-stated preference (#3).

A future change could add a "narrow mode" that stacks the middle band; this change does not.

### D8 — InnoDB Status parsing is regex, not state machine

The existing `internal/db/innodb_status_parser.go` parses the InnoDB status string with section-based regex extraction. HLL is a simple `(?m)^History list length\s+(\d+)$` — no parser-architecture changes.

## Risks

- **Threshold false positives.** `[PAGE]` triggering on a benign HLL spike during a long backup would erode trust. Mitigation: the `[WARN]` band catches edge cases first, and we can tune the thresholds before the change ships in a release. Worst case, the operator presses `I` to drill in and sees no real issue — acceptable cost for a first cut.
- **Sparkline bandwidth at high session counts.** The CPU sampler iterates active sessions on each tick; 1000+ active sessions could amplify the existing per-tick cost. Existing `M1` config caps `SessionCapacity` at 8192 — already protected.
- **`SHOW GLOBAL STATUS` on Aurora.** Aurora exposes a different set of `Innodb_*` variables. The collector will skip absent fields silently (D4), but the `bp_hit` value may simply never render on Aurora. Acceptable for v1; a follow-up can add Aurora-specific cherry-picks.
- **MariaDB replica status syntax.** MariaDB uses `SHOW SLAVE STATUS`; recent MySQL uses `SHOW REPLICA STATUS`. The collector probes once and remembers which to use.

## Migration Plan

This is a launch-default flip plus three new capabilities. No data migration. Existing users land on a different default tab; `I` still gets them straight to Issues. CHANGELOG entry calls this out.

## Open Questions

None at proposal time. The user signed off on the three constraints in the parent conversation.
