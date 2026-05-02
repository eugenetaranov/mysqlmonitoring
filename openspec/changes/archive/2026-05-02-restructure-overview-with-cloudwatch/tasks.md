## 1. Header chrome rework

- [x] 1.1 Stripped `Server: …`, `Transactions: X | Lock Waits: Y | Processes: Z`, and DB Load sparkline rows from `internal/tui/views.go:renderHeader`. Now strictly chrome — no data.
- [x] 1.2 Added right-aligned compact context: `HH:MM:SS · <version> <variant> · up <duration> · [cw]<dot>`. Uptime added to `db.HealthVitals.UptimeSeconds`, plus `Uptime` in the existing `SHOW GLOBAL STATUS` cherry-pick (zero new round-trips). `compactVersion` strips build suffixes; `humanUptime` scales granularity with magnitude (47s → 47m → 2h 17m → 14d 3h).
- [x] 1.3 `cloudWatchIndicator` placeholder added; returns "" today. Phase 2 will wire it once the collector lands so the chrome dot reflects real CW state.
- [x] 1.4 7 new tests: no-duplicated-counts, right-aligned-segments, omits-context-when-empty, stacks-when-width-unknown, compactVersion table, humanUptime table, uptime-suppressed-without-insights.

## 2. CloudWatch collector

- [x] 2.1 Added `aws-sdk-go-v2/config` and `aws-sdk-go-v2/service/cloudwatch` to `go.mod`.
- [x] 2.2 New `internal/collector/cloudwatch.go`:
  - `CWMetrics` struct with all required fields.
  - `CloudWatchSource` interface (`GetMetricData(ctx, instanceID, isAurora) (CWMetrics, error)`).
  - `CloudWatchCollector` mirroring `HealthCollector`: caches probe, exposes `Latest()`/`Probe()`, short-circuits Poll when probe is non-Available so the call is safe even when CW is disabled.
  - `ProbeCloudWatch` does the credential resolution; `resolveTarget` (pure) handles hostname / flag parsing so unit tests don't trigger SDK calls.
  - Hostname regex matches both RDS instance endpoints (`<id>.<rand>.<region>.rds.amazonaws.com`) and Aurora cluster endpoints (cluster / cluster-ro variants share the same trailing region pattern).
  - `AWSCloudWatchSource` is the production source; one `GetMetricData` per Poll covers all metrics including conditional Aurora-only ones.
- [x] 2.3 `--aws-region` and `--rds-instance` flags in `cmd/mysqlmonitoring/main.go`. Both optional. New `extractHostFromDSN` helper feeds the hostname-parse path.
- [x] 2.4 Wired `runCloudWatchLoop` into `internal/insights/insights.go`. Loop starts only when `CloudWatch != nil && Probe.Available`. Per-poll context timeout 5s so a stuck CW endpoint never blocks shutdown.
- [x] 2.5 `Insights.AttachCloudWatch` setter so the collector is wired post-`New` (the AWS source needs the resolved region from the probe). `ErrorCounts.CloudWatch` exposed.
- [x] 2.6 9 unit tests covering: not-RDS-disables, hostname parse for RDS / Aurora-cluster / Aurora-reader, explicit-flags-beat-hostname, partial-flags-fill-from-host, non-RDS-hostname-fails, Poll short-circuit when unavailable, first-poll, Aurora flag propagation, error-leaves-cache-intact, secondsToDuration helper.

Bonus: chrome `[cw]●` indicator wired in `internal/tui/views.go:cloudWatchIndicator` — bright green dot when a sample exists, dim circle when configured but no sample yet, absent when no CW context.

## 3. Verdict line: CW fields

- [x] 3.1 `renderOverviewVerdictLine` now reads from `latestCloudWatch(m)` and prepends a CPU%/Mem-free/IOPS/DBLoad cluster as the leftmost gauges after `[HEALTHY/WARN/PAGE]`. Snapshot time removed from the verdict line (it lives in the chrome now per Phase 1). Mem renders as bytes free (no instance-class data to compute %); operators read absolute GB faster than % anyway.
- [x] 3.2 `computeVerdict` gains a CW CPU% tier (>80 → WARN, >95 → PAGE). Mem and IOPS thresholds need provisioned-quota data we don't have, so those fields self-colour at the gauge level only and don't bump the verdict word.
- [x] 3.3 New `wrapVerdictParts` joins gauges with a two-space separator and breaks to a new (two-space-indented) line when the next part would overflow `widthOr120(m.width)`. ANSI-aware via `lipgloss.Width`.
- [x] 3.4 Tests: formatBytes table, formatRate table, wrapVerdictParts overflow-breaks + no-overflow-stays-on-one-line. Existing computeVerdict tests still pass.

## 4. Three-column top-N + bottom strip

- [x] 4.1 `renderTopAASQueries` calls `insights.TopSQL` with `Window: overviewWindow (60s), Limit: 5, Sort: SortByAAS`. Renders `AAS X.XX  <digest>  no_idx` (when applicable).
- [x] 4.2 `renderTopAASUsers` calls `insights.LoadByGroup(GroupKeyUser, 60s)`. Horizontal bars proportional to the highest AAS in the panel; cursor highlight on the focused row.
- [x] 4.3 `renderTopBusiestTables` + `topBusiestTables` aggregator: walks digest activity over the 60s window, extracts table names via existing `extractTableFromSQL`, sums per-table AAS + calls/sec. Activity-based, distinct from the detector-issue-based aggregation in the Tables tab.
- [x] 4.4 `renderLongTransactions` filters `Snapshot.Transactions` where `Time >= 30s`, sorts by Time desc, top-5. Empty query → `(idle in trx)` placeholder. Renders `<age> pid <pid> <user> <query>`.
- [x] 4.5 `renderOverviewMiddleBand` rewritten to render the three top-N panels at equal widths. `renderOverviewBottomBand` rewritten as Long-trx + Replication strip; replication panel collapses cleanly on standalone servers and Long-trx widens to full width.
- [x] 4.6 Removed `renderLoadPanel`, `renderHottestQueries`, `renderHottestTables`, `renderIssuesPanel`, `loadGroupingTitle` — all unreferenced after the layout swap.
- [x] 4.7 `u/h/s` keys repurposed: `enter` and `u` drill into Top SQL with `m.topUser` set from the cursor row of the AAS Users panel. `h` and `s` jump straight to Top SQL (no filter); operators use Top SQL's own filters for per-host / per-schema breakdowns. Footer hint updated.
- [x] 4.8 Snapshot tests rewritten: new-layout-panel-headers (positive + the obsolete-panel guards), graceful-messages-without-insights, long-trx-panel-empty, long-trx-filter-by-30s, long-trx-sort-by-age-desc, top-busiest-tables aggregation via extractTableFromSQL.

## 5. Docs / shipping

- [x] 5.1 README "Overview tab" section rewritten with the new layout mockup, key bindings (`enter`/`u` drill, `h`/`s` jump-to-Top-SQL), failure modes. New "CloudWatch RDS metrics" subsection covers auth (SDK default credential chain only), target detection (hostname-parse + explicit overrides), and the cost number ($0.23/month at default cadence).
- [x] 5.2 CHANGELOG entries above the prior block: "Changed — Overview tab restructured", "Changed — Header chrome reduced to a single row", "Added — CloudWatch RDS metrics". Each entry calls out the migration touchpoints (key behaviour change, header row removal, etc.).
- [x] 5.3 Live smoke against the demo stack: `make test-up`, `mysqlmonitoring status` against the running stack confirms the binary connects, sees the Python contention workload's lock chains (depth 3), and reports the deadlock detector output. End-to-end render-frame smoke is covered by the unit tests' positive + guard assertions on the new panel headers; visual smoke against a real RDS instance with CloudWatch credentials would require an actual AWS account and is left for the operator to verify on first deployment.
