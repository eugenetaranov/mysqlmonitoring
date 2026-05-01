## 1. Header chrome rework

- [ ] 1.1 Strip `Server: …`, `Transactions: X | Lock Waits: Y | Processes: Z`, and DB Load sparkline rows from `internal/tui/views.go:renderHeader`.
- [ ] 1.2 Add right-aligned compact context block: `HH:MM:SS · <version> <variant> · up <duration> · [cw]<dot>`. Variant token follows existing IsRDS/IsAurora/IsMariaDB rules. Uptime comes from `Snapshot.ServerInfo` (add a `Uptime time.Duration` field if not already present, populated from `SHOW GLOBAL STATUS` `Uptime`).
- [ ] 1.3 `[cw]<dot>` indicator: bright dot when `Insights.CloudWatch.Latest().Time` is non-zero, dim dot when configured but no sample, absent when no CW context. Render via existing `okStyle`/`dimStyle`.
- [ ] 1.4 Tests: `internal/tui/views_test.go` table tests for header rendering with / without CW configured / with samples.

## 2. CloudWatch collector

- [ ] 2.1 Add `aws-sdk-go-v2` + `aws-sdk-go-v2/service/cloudwatch` modules to `go.mod`.
- [ ] 2.2 New `internal/collector/cloudwatch.go`:
  - `CWMetrics` struct (CPUPct, FreeableBytes, ReadIOPS, WriteIOPS, ReadLatency, WriteLatency, DiskQueueDepth, DBLoad family, AuroraReplicaLag, ReplicaLag, Time).
  - `CloudWatchSource` interface (`GetMetricData(ctx, instanceID, region) (CWMetrics, error)`).
  - `CloudWatchCollector` mirroring `HealthCollector` shape: caches probe (region + instanceID), polls every 60s, exposes `Latest()`.
  - `ProbeCloudWatch(ctx, host, regionFlag, instanceFlag)` returns `(region, instanceID, available bool, err)`. Hostname-parse for RDS / Aurora cluster / Aurora reader patterns; explicit flags win.
  - SDK default credential chain check: a single `aws.Config.Credentials.Retrieve(ctx)` at probe time. If retrieval errors, `available=false` and a typed reason is returned for the startup notice.
- [ ] 2.3 Add `--aws-region` and `--rds-instance` flags in `cmd/mysqlmonitoring/main.go` (both optional, both default empty for hostname-parse path).
- [ ] 2.4 Wire `runCloudWatchLoop` into `internal/insights/insights.go` next to `runHealthLoop`. Loop only starts when probe.available=true. 60s ticker. Errors counted into `ErrorCounts.CloudWatch`.
- [ ] 2.5 Plumb `CWMetrics` into the existing `db.HealthVitals.CloudWatch *CWMetrics` (new field) so the consumer reads from one place. `HealthCollector` stays unchanged; the merge happens in `Insights` exposing both via a single accessor.
- [ ] 2.6 Tests: collector unit tests with a fake `CloudWatchSource` (probe success / failure paths, counter-resets, region/instance parsing, Aurora-vs-RDS pattern matching).

## 3. Verdict line: CW fields

- [ ] 3.1 Extend `internal/tui/overview.go:renderOverviewVerdictLine` to render CPU%, Mem%, IOPS r/w as the leftmost scalar columns when `Insights.Health.Latest().CloudWatch` is non-nil. Existing fields (`AAS`, `running`, `bp_hit`, `HLL`, `repl`, `dl`) follow.
- [ ] 3.2 Extend `computeVerdict` thresholds with CW-aware tiers (CPU% > 80 / 95 → WARN / PAGE, Mem% < 15 / 5 → WARN / PAGE).
- [ ] 3.3 Wrap the verdict line at terminal width (the line is long; current implementation joins with two-space separator on one row, which truncates). Preserve all gauges across two rows when needed.
- [ ] 3.4 Tests: render snapshots with / without CW; threshold-bump tests for the new tiers.

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
