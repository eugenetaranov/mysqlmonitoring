## 1. Load-attribution plumbing

- [x] 1.1 Add `User`, `Host` fields to `series.SessionSample` (`internal/series/sample.go`).
- [x] 1.2 Add `User`, `Host` to `db.CurrentStmt`; extend the `CurrentStatements` query in `internal/db/perf_insights.go` to SELECT `t.processlist_user` and `t.processlist_host` (the JOIN to `performance_schema.threads` already exists).
- [x] 1.3 Propagate `User` / `Host` from `CurrentStmt` into `SessionSample` in the CPU sampler (`internal/collector/cpu.go`).
- [x] 1.4 Add `insights.LoadByGroup(samples *series.RingSink[series.SessionSample], now time.Time, window time.Duration, key GroupKey) []GroupLoad` in `internal/insights/aggregate.go`. `GroupKey` is an enum: user, host, schema.
- [x] 1.5 Unit tests: empty input, single group, many groups, group-sum equals total AAS within rounding.

## 2. Health-vitals plumbing

- [x] 2.1 Add `HistoryListLength uint64` to `db.InnoDBStatus`; parse in `internal/db/innodb_status_parser.go` with regex `(?m)^History list length\s+(\d+)`. Tests with sample fixtures (steady state, idle DB, high-HLL DB).
- [x] 2.2 Add `db.HealthVitals` struct (Threads_running, Threads_connected, dirty pages, total pages, aborted_clients delta, optional Replica) and `HealthVitals(ctx)` interface method on `DB`. Implement on `MySQLDB.HealthVitals(ctx)` using one `SHOW GLOBAL STATUS WHERE Variable_name IN (…)` query plus optional `SHOW REPLICA STATUS` / `SHOW SLAVE STATUS`. Wire through existing `queryWithRetry` / per-query timeout helpers.
- [x] 2.3 New `MySQLDB.ProbeReplica(ctx)` detects replica role and dialect once at startup; cached by the HealthCollector for the process lifetime.
- [x] 2.4 New `internal/collector/health.go` mirroring `digest.go` shape: `HealthCollector`, `Poll(ctx)`, public `Latest() HealthVitals` and `ReplicaProbe()`. Holds the prior `Aborted_clients` for delta computation.
- [x] 2.5 Wire `runHealthLoop` into `internal/insights/insights.go`; add `HealthInterval` (default 5s) to `Insights.Config`. Health loop runs unconditionally — independent of perf_schema availability.
- [x] 2.6 Unit tests: HLL regex (steady, absent, large value), delta math across polls, counter-reset clamps to 0, probe-only-once, probe-failure retries next poll, error leaves cache intact, probe propagated to vitals call.

## 3. Overview view

- [x] 3.1 Add `ViewOverview` constant to `internal/tui/model.go`; flip `NewModel` default `view: ViewOverview`.
- [x] 3.2 Prepend `{"O","Overview",ViewOverview}` to `orderedTabs` in `internal/tui/tabs.go`.
- [x] 3.3 Add `case ViewOverview:` to the `renderMain` switch in `internal/tui/views.go`.
- [x] 3.4 New `internal/tui/overview.go` with `renderOverview(m Model) string` and per-panel sub-renderers: status line, sparkline+legend (reuse `renderSparklineHeader`), load panel (reuse `sparkBlocks`), replication panel, hottest queries, hottest tables, live issues.
- [x] 3.5 Add Model fields: `topUser`, `topHost`, `loadGrouping` (enum: user/host/schema), `overviewCursor`.
- [x] 3.6 Key handlers: `u`/`h`/`s` cycle load grouping; `↑/↓` move cursor; `enter` drills into Top SQL with the appropriate filter pre-set; `O` jumps back to Overview from anywhere; tab cycle includes Overview.
- [x] 3.7 Compact issues panel implemented inline in overview.go (rather than extracted from issues.go) — issuesTable's colWidths machinery isn't a fit for a fixed-height embed; the new `renderIssuesPanel` is purpose-built.
- [x] 3.8 Snapshot tests in `internal/tui/overview_test.go`: healthy verdict, warn-on-HLL, page-on-HLL, page-on-deadlock, live-issues empty/populated, load-panel-title-toggle, hottest tables, replication panel rendering, default view, tab bar, helper math (renderHBar, formatBigCount, computeVerdict).

## 4. Failure-mode polish

- [x] 4.1 Hide replication panel cleanly when `ProbeReplica` reports standalone; surrounding panels reflow into the freed width (implemented in Phase 3).
- [x] 4.2 Load panel and Hottest Queries panel show capability-specific notices ("performance_schema waits disabled", "performance_schema digest disabled") when the relevant `Capabilities()` flag is off, instead of a generic "perf-insights disabled".
- [ ] 4.2a (deferred) MariaDB-specific fallback to count-by-user from `SHOW PROCESSLIST`. The current behaviour ("performance_schema waits disabled" notice) is honest and ships the intended UX; a count-based fallback is a follow-up if user feedback shows it's wanted.
- [x] 4.3 Cold-start: panel chrome (headers / dividers) is rendered before any data lands; "gathering samples…" body keeps layout stable when first samples populate.
- [x] 4.4 Always-on Insights wiring in `cmd/mysqlmonitoring/main.go`: `Insights` now starts unconditionally so the health collector and HLL parsing always run; perf-insights collectors gate on capabilities; probe warnings only print when `--enable-perf-insights` is set.

## 5. Docs / shipping

- [ ] 5.1 README: short Overview screenshot section + 3-line description; update existing tab list.
- [ ] 5.2 CHANGELOG entry calling out the default-view flip.
- [ ] 5.3 Live smoke against fresh MySQL 8.0 container (see verification in proposal); confirm Overview is first frame, sparkline ticks, panels populate within 12s, and `u`/`h`/`s` cycle works.
