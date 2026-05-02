## Context

The Overview tab shipped in the `add-overview-view` change as the new launch view. Three months of use surfaced two issues plus one signal gap:

1. The DB Load sparkline renders twice — once from the header chrome, once from the Overview body. The header was originally designed for the lock-monitor-only TUI and has not been adjusted as Overview took over the launch experience.

2. The Load-by-USER mid-panel uses a 1-hour window. That's the right window for the dedicated Top SQL tab; on Overview it dampens incident signal because spikes that started 60 seconds ago are averaged against an hour of quiet.

3. Operators running against RDS / Aurora can't see host-level CPU% or free memory from a MySQL connection alone. They alt-tab to the AWS console. This change closes that gap by polling CloudWatch directly.

## Goals

- One screen that triangulates "spike → who → what" in three eye movements.
- No duplication: every datum appears exactly once on screen.
- 60s window on Overview; 1h window stays in the dedicated Top SQL tab.
- CloudWatch is opt-in by credential availability — never required, never noisy when absent.
- Header chrome reduced to a single row.

## Non-Goals

- Persistent CloudWatch metric history (no ring buffer beyond the most recent poll).
- Multi-instance Aurora cluster aggregation. We poll the single instance the MySQL connection is talking to (per the operator's confirmed answer in discovery).
- Custom CloudWatch metrics or namespace overrides. Default RDS / Aurora namespaces only.
- High-resolution CloudWatch (10s period) — out of scope for cost and authn complexity reasons.
- Replacement of the existing detector-issue-based "Hottest tables" aggregation. The new "Top busiest tables (60s)" is activity-based; the existing one stays in the Tables tab.

## Decisions

### D1 — One-row header chrome

`renderHeader` becomes:

```
MySQL Lock Monitor   [O Overview][I Issues][B Tables][M MDL][L Lock][t Top SQL]
                                          10:42:13 · 8.0.45 RDS · up 14d · [cw]●
```

The right-aligned context block holds: snapshot time (HH:MM:SS), short version + variant tag (RDS / Aurora / MariaDB), uptime in human form, and a small `[cw]●` indicator when the CloudWatch collector has produced at least one successful sample. The dot is dim/grey when CW is configured but not yet polled, and absent entirely when no CW context exists.

The `Server: …`, `Transactions: X | Lock Waits: Y | Processes: Z`, and DB Load rows are removed. Each view's body is now responsible for its own context.

### D2 — Verdict line as the only scalar gauges row

```
[HEALTHY] CPU 34%  Mem 71%  IOPS 1.2k/450  AAS 1.8/8c  running 3/512
          bp_hit 99%  HLL 12k  repl +0s  dl 0
```

The line wraps at terminal width (no truncation). CloudWatch fields (`CPU%`, `Mem%`, `IOPS r/w`) appear only when `Insights.CloudWatch.Latest()` is non-nil. The existing computeVerdict thresholds gain CW-aware tiers:

- WARN: `CPU% > 80`, `Mem% < 15`, `IOPS read or write > 80% of provisioned IOPS` (when known).
- PAGE: `CPU% > 95`, `Mem% < 5`, sustained IOPS at 100% of provisioned for >2 min.

### D3 — Single sparkline, single source of truth

The sparkline renders only from `renderOverviewSparkline` in the body. `renderHeader` no longer emits one. Other tabs that previously inherited the header sparkline (Issues, Tables, Lock) lose it; they didn't read or use it for navigation. The Top SQL tab keeps its own sparkline from inside its body renderer.

### D4 — Three top-N columns at 60s

Three side-by-side panels of equal width, gap = 2:

```
┌Top AAS queries (60s)──────┬Top AAS users (60s)─────┬Top busiest tables (60s)────┐
│ AAS 4.2 select shop_orders │ app_rw      ████ 8.4   │ shop.orders   2.1k qps     │
│ AAS 1.8 update order_items │ reports     ██   1.6   │ shop.items     980 qps     │
│ AAS 1.4 analytics_rollup ⚠ │ app_ro      █    1.1   │ auth.sessions  640 qps     │
│ AAS 0.6 cron_balance_sweep │ replication ░    0.0   │ analytics.evt  120 qps     │
│ AAS 0.5 user_lookup        │ (others)    ░    0.4   │ shop.products  410 qps     │
└────────────────────────────┴────────────────────────┴────────────────────────────┘
```

- **Top AAS queries**: existing `insights.TopSQL` with `Window: 60*time.Second, Limit: 5, Sort: SortByAAS`. The ⚠ tag flags `NoIndexUsedCalls > 0`.
- **Top AAS users**: existing `insights.LoadByGroup(GroupKeyUser)` with the same 60s window. Renders horizontal bars proportional to the highest AAS in the panel.
- **Top busiest tables (60s)**: new aggregator. Walks `Snapshot.Processes` to count active queries per table (extracting via the existing `extractTableFromSQL`), and corroborates with digest stats per table when available. Activity-based, not contention-based — the existing detector-issue Hottest Tables stays in the Tables tab where it already lives.

### D5 — Bottom strip: Long transactions + Replication

```
┌Long transactions (≥30s)──────────────┬Replication (when applicable)──────────────┐
│ 3m12s  pid 8821  app_rw   shop_alter │ source=db-01  IO=Y SQL=Y                   │
│ 2m04s  pid 8714  cron     sweep_old  │ lag 0s (max5m 12s · CW 0.2s)               │
│ 1m45s  pid 8717  cron     idle in trx│ GTID gap 0                                 │
└──────────────────────────────────────┴────────────────────────────────────────────┘
```

- **Long transactions**: filters `Snapshot.Transactions` where `Time >= 30s`, sorts desc by `Time`, top-5. Catches the "idle in transaction" silent wedger that today's Issues tab finds eventually but Overview doesn't surface.
- **Replication panel** keeps its existing rendering, with one new line: when CloudWatch's `AuroraReplicaLag` (or `ReplicaLag`) is populated, it appears in parentheses next to the MySQL-reported lag for corroboration.
- **Standalone server (no replica role)**: Replication panel is removed entirely; Long transactions widens to full width, mirroring the existing Replication-removal behaviour.

### D6 — `u`/`h`/`s` keys: keep functional, repurpose

The existing keys cycled the Load-by-X panel grouping. The mid-panel goes away in this change, so the keys lose their original purpose. Two options considered:

- **Drop them** — minimum surgery, but breaks muscle memory.
- **Repurpose them** — `u` scopes the Top AAS users panel cursor to a single row's drill (existing `m.topUser` filter); `h` does the same with `m.topHost`; `s` with `m.topSchema`.

Decision: repurpose. The keys' semantics ("group/scope by X") are preserved; only the panel they affect changes. Footer hint reads `u/h/s scope · enter drill · t Top SQL` so the operator's first muscle-memory press lands somewhere intuitive.

### D7 — CloudWatch authentication: SDK default credential chain only

No `--aws-profile` flag. The AWS SDK default credential chain (env vars, `~/.aws/credentials`, IAM instance role) is the contract. If the chain produces no credentials, the collector logs a single startup notice ("CloudWatch metrics unavailable: no AWS credentials in the default chain") and disables itself.

Rationale: the operator runs `mysqlmonitoring` from a developer laptop or a bastion. Both already have working AWS credentials; both already configure them via the SDK default chain. A flag adds surface area without solving a real problem.

### D8 — CloudWatch target detection: hostname parse with explicit overrides

```
Detection order (first match wins):
  1. --rds-instance and --aws-region flags (explicit; never wrong)
  2. RDS hostname pattern: <id>.<rand>.<region>.rds.amazonaws.com
  3. Aurora cluster pattern: <cluster>.cluster-<rand>.<region>.rds.amazonaws.com
                            <cluster>.cluster-ro-<rand>.<region>.rds.amazonaws.com
  4. Else: collector disables with a one-shot notice
```

The collector probes once at startup and caches the resolved `instanceID + region` for the process lifetime. If detection fails the chrome `[cw]` indicator is absent; the verdict line shows no CW columns; everything else works.

### D9 — CloudWatch metrics: short list, 60s period

The minimum set that materially changes triage decisions:

```
General RDS:
  CPUUtilization                — host CPU%
  FreeableMemory                — bytes; rendered as "Mem N%" of instance class capacity
  ReadIOPS / WriteIOPS          — IOPS r/w
  ReadLatency / WriteLatency    — host disk latency
  DiskQueueDepth                — IO backup early signal

Aurora-only (when IsAurora):
  DBLoad / DBLoadCPU / DBLoadNonCPU   — Aurora's own AAS
  AuroraReplicaLag                    — replication delay

Replica-only (when MySQL replica role detected and not Aurora):
  ReplicaLag                          — corroboration of Seconds_Behind_Source
```

Period: 60s. A single `GetMetricData` call fetches all metrics in one round-trip per cycle. Cost: ~$0.23/month at default cadence — non-issue.

### D10 — Where the CW data lives

`db.HealthVitals` gains an optional `CloudWatch *CWMetrics` field. `internal/insights/insights.go` gains a `CloudWatchCollector` next to `HealthCollector`. The Overview's verdict line reads from `Insights.Health.Latest().CloudWatch` to surface the CW fields. This keeps a single read path on the consumer side; whether the data came from MySQL or AWS is opaque to the renderer.

The collector exposes `Capabilities()` so the chrome `[cw]●` indicator knows whether to render dim (configured but no sample yet), bright (sample present), or absent (no CW context).

## Risks

- **AWS SDK weight**. `aws-sdk-go-v2/service/cloudwatch` adds ~3 MB of vendored code. Mitigation: only import the cloudwatch service module, not the full SDK. The static binary stays under 30 MB.
- **CloudWatch rate limits**. `GetMetricData` is not aggressively rate-limited at 60s cadence for a single instance. Operators running against many instances simultaneously could in theory pile up — but the tool is one-instance-per-process, so this is a non-issue in practice.
- **Hostname-parse fragility**. Unusual RDS DNS configs (CNAME pointing somewhere weird, custom endpoint) won't match the regex. Mitigation: explicit `--rds-instance` / `--aws-region` flags always win.
- **Muscle memory break for `u`/`h`/`s`**. Repurposing instead of removing keeps the surgery small but the keys do something subtly different now. Mitigation: explicit footer hint on first render of Overview.
- **The 1-hour Load-by-USER panel disappears**. Operators who relied on the longer view to "see who's been busy this hour" lose that. Mitigation: the dedicated Top SQL tab (`t`) keeps the 1h window and gains the missing user/host/schema breakdowns there.

## Migration Plan

This is an in-place rework; no migrations. Operators see a different Overview after upgrading. CHANGELOG entry calls out:

- "Overview tab restructured: three top-N panels at 60s window, long transactions + replication strip below"
- "Removed: header bar's redundant server/counts/sparkline rows"
- "Removed: Load attribution panel's u/h/s grouping cycle (keys repurposed for Top users scope)"
- "Added: optional CloudWatch RDS metrics in the verdict line when AWS credentials are present"

## Open Questions

None at proposal time — all five clarification questions resolved during discovery.
