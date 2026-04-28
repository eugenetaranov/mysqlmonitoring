## Context

`mysqlmonitoring` already polls a target MySQL server, builds a `db.Snapshot`, runs detectors over it, and renders a Bubbletea TUI. The pipeline is single‑process, agentless, and zero external dependencies beyond the Go MySQL driver.

This change adds a second class of data alongside the per‑poll snapshot: per‑digest and per‑wait‑class **time series**. Operators need to see "what is the database spending its time on?" — a question that cannot be answered from a single point‑in‑time snapshot. RDS Performance Insights and SolarWinds DPA both answer it via continuous sampling of `performance_schema` plus a server‑side store.

The user has explicitly asked to defer any on‑disk store. M1 must therefore deliver useful insights using only process memory, without compromising the option to add SQLite (or any other store) cleanly in M2+.

Constraints:
- `CGO_ENABLED=0` (per CLAUDE.md). No SQLite via cgo.
- No new heavy dependency.
- Existing detectors and TUI panels keep working unchanged.
- MySQL 5.7 and 8.0+ supported. MariaDB best‑effort: collectors must degrade gracefully when `performance_schema` views differ.

## Goals / Non-Goals

**Goals:**
- Make "top SQL" and "DB load by wait class" first‑class views in both CLI and TUI.
- Slice every aggregation by application tag, with no operator setup beyond what the app already does (`program_name` or sqlcommenter).
- Keep the binary single‑process, single‑file, no external store.
- Lay a clean seam (`internal/series/`, `internal/collector/`) so M2's persistence work is additive, not a rewrite.

**Non-Goals:**
- Persistence. No SQLite, no flat‑file dump, no `agent` daemon mode. M1 forgets everything on exit.
- Alerting, exit‑code probes, webhooks. Those are M3.
- Multi‑host. M1 monitors one DB per process.
- Anomaly detection / baselines. Needs ≥1 week of data; deferred to M5.
- Replication, host metrics, schema advisor. Deferred to M2/M4.

## Decisions

### D1: In‑memory ring buffers, not on‑disk store

**Decision:** All series live in process memory. Each digest carries its own ring of samples; wait‑class data is one ring per class. Buffers are sized in wall‑clock window × poll interval.

**Why:** the user explicitly asked for lean, no‑deps. SQLite (even pure‑Go `modernc.org/sqlite`) is ~2 MB compiled in and adds a non‑trivial schema/migration surface. The ring‑buffer abstraction is ~150 LoC of stdlib code and is exactly the seam M2's persistence layer can wrap.

**Alternative considered — pure‑Go SQLite:** rejected for M1 only. The `series.Sink` interface (see D5) makes adding it later a one‑file change.

**Alternative considered — flat‑file append:** rejected. Adds I/O failure modes for no current benefit; doesn't enable any new view; no time‑series query semantics.

### D2: Diff‑sampling, not absolute‑value sampling

**Decision:** Both digest and wait collectors store **deltas** between consecutive polls, not raw counter values. Counter resets are detected (any monotonic field decreases) and the affected interval is dropped.

**Why:** `performance_schema` summary tables are monotonic since server start (or since `TRUNCATE TABLE`). Storing absolute values wastes memory and forces every reader to subtract. Storing deltas matches the unit operators want (calls/s, AAS) and makes window aggregation a simple sum.

**Alternative considered — absolute values with on‑read diff:** rejected. Doubles memory for no analytic benefit and complicates reset handling.

### D3: Inline collectors in the existing monitor loop

**Decision:** New collectors register with `internal/monitor/` and run on the same poll tick as the existing snapshot collection. They do not spawn separate goroutines per collector.

**Why:** keeps cancellation, error reporting, and connection management identical to today. One ticker, one `select`, one error channel. If a collector is slow, every collector sees the same back‑pressure.

**Trade‑off:** a slow `EXPLAIN` could (in principle) block the loop. We solve this in D6 by running EXPLAIN on a separate connection driven by a user‑request channel, never on the poll tick.

### D4: CPU class is sampled, not derived from wait counters

**Decision:** CPU AAS is computed by sampling `events_statements_current` for sessions actively executing with no current wait, not by subtracting wait‑class sums from a target.

**Why:** PI does the same. `events_waits_*` only counts time spent waiting; CPU time has no row in those tables. Sampling running sessions at a small fixed interval (e.g., 1s) gives an unbiased estimator of CPU AAS. The sample rate is decoupled from the digest/wait poll interval to keep CPU AAS smooth even when digest sampling is rare.

### D5: `Sink` interface as the future‑persistence seam

**Decision:** Introduce a `series.Sink` interface (`Append(sample) error`, `Range(window) iter.Seq[sample]`) implemented by `RingSink` in M1. M2 can drop in `SQLiteSink` (or `DualSink{Ring, SQLite}`) without touching collectors.

**Why:** keeps the collector code persistence‑agnostic, isolates the choice to defer storage so it can be revisited cheaply.

### D6: EXPLAIN on a dedicated read‑only connection

**Decision:** Open a second pool connection on demand for EXPLAIN, set `transaction_read_only = ON` for the session, run with `MAX_EXECUTION_TIME(2000)` hint and a 5s client deadline. Refuse to EXPLAIN any non‑SELECT example.

**Why:** safety. We never want this tool to be a vector for accidentally writing to prod. The read‑only session and the explicit verb allow‑list together prevent any side effects from a sampled `SQL_TEXT`. The timeouts protect against pathological plans on huge tables.

### D7: App tag resolution: sqlcommenter first, `program_name` second

**Decision:** Comment‑embedded tags win over `connect_attrs` because they are per‑request and survive shared connection pools. `program_name` is per‑connection and is wrong when one process serves multiple services on one pool.

**Why:** matches how `sqlcommenter` and `OpenTelemetry SQL` propagate request context. Falls back cleanly when apps haven't adopted comments.

### D8: Tracked‑digest cap with load‑based eviction

**Decision:** When the digest map reaches `--max-digests` (default 2000), evict the digest with the lowest aggregate `sum_timer_wait_delta` over the in‑memory window before inserting a new one.

**Why:** unbounded growth is a memory DoS risk on busy servers with thousands of distinct digests (ORMs, ad‑hoc queries). Evicting by load preserves the digests that actually matter to operators.

**Trade‑off:** a digest that just appeared (small window, small total) can be evicted before it has a chance to accumulate load. Mitigation: brand‑new digests are protected for at least one full poll interval before becoming eviction candidates.

## Risks / Trade-offs

- **Loss of history on exit.** The whole point of "no DB" is accepted forgetfulness. → User explicitly accepted this; M2 plans persistence.
- **Memory blow‑up on high‑cardinality workloads.** Many distinct digests × long window × short interval = wide rings. → Tracked‑digest cap (D8); document and surface a TUI footer line "tracking N digests, evicted M".
- **Performance schema overhead.** Five extra reads per poll. → On healthy servers, sub‑millisecond. Document the small load and the option to raise `--interval`.
- **`SQL_TEXT` containing literals.** Retrieved only for EXPLAIN, never stored in the digest registry. → Codified in `digest-sampling` and `explain-on-demand` specs.
- **Plan flips on tiny tables.** Optimizer chooses different plans at small row counts, producing noise. → `plan_flip` is informational; not an alert in M1.
- **MariaDB drift.** Some `events_statements_history_long` columns differ. → Best‑effort: feature‑detect at startup and disable the affected paths with a one‑time warning.
- **Read‑only EXPLAIN still runs the optimizer.** On very large schemas this can be slow. → 2s server timeout, 5s client deadline (D6).

## Migration Plan

This is an additive change. There is no migration:

1. Land `internal/series/`, `internal/collector/`, `internal/db/` query helpers (no behavior change yet).
2. Land the three collectors behind a feature‑flag CLI flag (`--enable-perf-insights`); default off.
3. Land the `top` and `load` subcommands.
4. Land the TUI sparkline header and `top` panel.
5. Flip `--enable-perf-insights` default to on.
6. Remove the flag in M2 once stable.

Rollback is `git revert` — no data files to clean up.

## Open Questions

- Default `--window`: `1h` keeps memory small but loses the "what happened during last lunch's incident" use case. Worth shipping `30m` and letting operators raise it?
- Sparkline color in non‑256‑color terminals: do we degrade to glyph‑only stacked bars, or skip the header? (Lipgloss handles this, but the spec needs to be explicit.)
- Should the `top` panel's EXPLAIN keybinding be `e` (mnemonic) or `Enter` (drill‑down)? Bubbletea precedent in this repo leans on Enter for navigation.
- Do we want `--app` filtering on the existing lock‑chain panel as well, or scope it strictly to the new perf views in M1?
