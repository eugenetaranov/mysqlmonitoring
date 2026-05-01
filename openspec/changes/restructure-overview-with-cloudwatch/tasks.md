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

- [ ] 4.1 New `renderTopAASQueries(m, width)`: calls `insights.TopSQL` with `Window: 60*time.Second, Limit: 5`. Reuses the existing per-row format (AAS + digest text + ⚠ for no-index).
- [ ] 4.2 New `renderTopAASUsers(m, width)`: calls `insights.LoadByGroup(GroupKeyUser, 60*time.Second)`. Reuses existing horizontal-bar rendering. Replaces the deleted Load-by-USER panel.
- [ ] 4.3 New `renderTopBusiestTables(m, width)`: aggregates `Snapshot.Processes` by table (via existing `extractTableFromSQL`) plus digest stats. Renders `<schema.table>  <qps>` for the top-5. Activity-based, not contention-based.
- [ ] 4.4 New `renderLongTransactions(m, width)`: filters `Snapshot.Transactions` where `Time >= 30s`, sorts desc by `Time`, top-5. Renders `<age>  pid <pid>  <user>  <query>`.
- [ ] 4.5 Reorganise `renderOverviewMiddleBand` and `renderOverviewBottomBand` into:
  - upper strip: 3 equal columns from §4.1-4.3 (joined via existing `joinHorizontal` + `padPanel`)
  - lower strip: 2 columns — Long transactions (left) + Replication (right, conditional on replica role)
- [ ] 4.6 Remove the old Load-by-USER panel + `(u/h/s)` cycle hint from `renderOverviewMiddleBand`.
- [ ] 4.7 Repurpose `u`/`h`/`s` keys: in `handleOverviewKey`, set `m.topUser`/`m.topHost`/`m.topSchema` from the cursor row of the active top-N panel and switch to ViewTop. Footer hint updated.
- [ ] 4.8 Snapshot tests in `internal/tui/overview_test.go` for the new layout: queries-panel rendering, users-panel, tables-panel, long-trx panel, replication-strip-collapse-when-standalone, no-CW vs with-CW verdict line.

## 5. Docs / shipping

- [ ] 5.1 README "Overview tab" section rewritten: new layout description, key bindings, CloudWatch opt-in, AWS credential chain note.
- [ ] 5.2 CHANGELOG: "Changed — Overview restructured" + "Added — CloudWatch RDS metrics" + the migration callouts (header rows removed, u/h/s repurposed).
- [ ] 5.3 Live smoke against the demo stack: `make test-up` then `make test-run`. Verify the deduplicated sparkline, the 60s panels populating within ~2 minutes of sysbench traffic, the long-trx panel showing the Python workload's contention scenarios. Repeat against an RDS instance with AWS credentials available; verify CW fields appear and the chrome dot lights up.
