## Why

The current Overview tab works but suffers two real problems and is missing one signal class:

1. **The DB Load sparkline + transactions/locks/processes counts render twice** when the user is on Overview — once from `renderHeader` (chrome) and once from `renderOverview` (body). The sparkline is the most expensive line on screen visually; rendering it back-to-back wastes vertical space and creates the "did the screen glitch?" feeling. The header was designed before Overview existed; Overview's verdict line is a strict superset of what the header repeats.

2. **The mid-band Load-by-USER panel uses a 1-hour window**, which makes it slow to react during incidents. Operators triaging a fresh spike want "what's hot in the last 60 seconds", not "what's been busy since lunch". The 1-hour view is the right framing for the dedicated Top SQL tab, but on Overview it dampens the signal the operator is looking for.

3. **Host-level metrics aren't visible** when running against RDS / Aurora. The MySQL connection sees buffer-pool numbers but not CPU%, free memory, IOPS, or disk queue depth — exactly the metrics that explain the difference between "MySQL is slow because it's CPU-bound" and "MySQL is slow because the host is starved by another tenant or a CGroup limit". Operators end up alt-tabbing to the AWS console.

## What Changes

- **Restructure Overview into five distinct strips**: a one-row header with tab bar + compact right-aligned context (time · version · uptime · `[cw]●` indicator), a scalar verdict line, the DB Load sparkline (rendered exactly once), three vertical top-N panels with a 60s window (queries / users / tables), and a bottom strip with Long Transactions on the left and Replication on the right (right column collapses when standalone).
- **Strip duplication from `renderHeader`**: drop the "Server: …", "Transactions: X | Lock Waits: Y | Processes: Z", and DB Load sparkline rows. The verdict line covers the counts; the version moves to the right-edge of the header bar; the sparkline now renders only from the body.
- **Add a CloudWatch RDS collector**: when running against `IsRDS` or `IsAurora` and the AWS SDK default credential chain produces credentials, poll `GetMetricData` every 60s for `CPUUtilization`, `FreeableMemory`, `ReadIOPS`, `WriteIOPS`, `ReadLatency`, `WriteLatency`, plus Aurora-specific `DBLoad` family and `AuroraReplicaLag` when applicable.
- **Surface CloudWatch fields in the verdict line** when they are populated — host CPU %, free memory %, IOPS r/w. The fields disappear cleanly when CloudWatch is unavailable (no credentials, no region, non-RDS server).
- **Top-N panels switch to a 60s window** with the panel header explicitly labelled `(60s)` so the operator never wonders.
- **Add Top busiest tables (60s)** as a sibling to Top AAS queries — aggregates over `Snapshot.Processes` and digest activity rather than detector issues. The existing detector-issue-based "Hottest tables" panel becomes a different concept and moves to a different location (or is dropped from Overview, since the existing Tables tab covers the same ground).
- **Add Long transactions panel** (≥30s threshold). Filters `Snapshot.Transactions` to the slowest-N. Catches "stuck idle-in-trx" incidents that today require switching to Issues to find.

## Capabilities

### Modified Capabilities

- `tui-overview`: rework layout to single-row chrome + verdict + sparkline + three-column 60s top-N + long-trx/replication strip. Remove the existing Load attribution panel toggle (`u` / `h` / `s`) — the new top-AAS-users panel replaces its primary use case. Verdict line gains conditional CloudWatch fields.

### New Capabilities

- `cloudwatch-rds-metrics`: new collector that fetches a small set of CloudWatch metrics for the RDS / Aurora instance the MySQL connection is talking to. Authenticates via the AWS SDK default credential chain. Disables itself silently with a one-shot startup notice when credentials, region, or instance ID can't be resolved.

## Impact

- **Code**: rewrites in `internal/tui/views.go:renderHeader` and `internal/tui/overview.go`; new `internal/collector/cloudwatch.go`; small additions to `internal/db/db.go` (`HealthVitals.CloudWatch *CWMetrics` field) and `internal/insights/insights.go` (new collector loop). New flags in `cmd/mysqlmonitoring/main.go`: `--aws-region`, `--rds-instance`.
- **Dependencies**: adds `github.com/aws/aws-sdk-go-v2` and `github.com/aws/aws-sdk-go-v2/service/cloudwatch`. AWS SDK is large but well-tested; this is the canonical Go path for CloudWatch.
- **DB load**: zero MySQL-side cost change. CloudWatch costs ~$0.23/month at the default cadence (8 metrics × 60s period × 30 days) — well under the threshold of "noticeable on the AWS bill".
- **Compatibility**: the layout change is visible to every existing user — they land on a different-looking Overview. The Issues / Tables / Lock / MDL / Top SQL tabs are unaffected. CloudWatch is opt-in by virtue of credentials being present; servers with no AWS context render exactly as before minus the CW fields.
- **UX**: muscle memory for `u`/`h`/`s` (load grouping cycle) is broken — the panel they cycled goes away. Mitigation: keep the keys functional but route `u`/`h`/`s` to scope the Top AAS users panel directly, with a brief footer hint on first render.
