## 1. Series infrastructure

- [x] 1.1 Create `internal/series/` package with a generic `RingBuffer[T]` (fixed capacity, overwrite oldest, O(1) append, range iterator).
- [x] 1.2 Define `Sample` types: `DigestSample`, `WaitSample`, `SessionSample`.
- [x] 1.3 Define `Sink` interface (`Append`, `Range(window)`) and implement `RingSink` over `RingBuffer`.
- [x] 1.4 Define `Registry` for tracked digests with load‑based LRU eviction (D8 protection window for new digests).
- [x] 1.5 Unit tests: ring overflow, range filtering by window, registry eviction order, new‑digest protection.

## 2. DB query helpers

- [x] 2.1 Add `db.DigestStats(ctx) ([]DigestRow, error)` reading `events_statements_summary_by_digest`.
- [x] 2.2 Add `db.WaitStats(ctx) ([]WaitRow, error)` reading `events_waits_summary_global_by_event_name`.
- [x] 2.3 Add `db.CurrentStatements(ctx) ([]CurrentStmt, error)` joining `events_statements_current` with `threads` and `session_connect_attrs`.
- [x] 2.4 Add `db.RecentExample(ctx, digest) (Example, error)` reading the most recent matching `events_statements_history_long` row.
- [x] 2.5 Add startup capability probe: which `setup_consumers` and `setup_instruments` are enabled; surface a one‑shot warning struct.
- [ ] 2.6 Unit tests against a fake `*sql.DB` for each query helper; integration tests on MySQL 5.7 and 8.0 fixtures (extend `tests/integration/`). _(deferred to combined integration pass with §6.5)_

## 3. Digest collector

- [x] 3.1 Implement `collector.DigestCollector` with prior‑sample baseline map.
- [x] 3.2 Implement counter‑reset detection per digest; drop interval, reseed baseline.
- [x] 3.3 Wire eviction against `series.Registry` cap.
- [x] 3.4 Emit `DigestSample` deltas to the sink.
- [x] 3.5 Unit tests: first‑poll baseline, second‑poll delta, server restart simulation, eviction under cap pressure.

## 4. Wait collector

- [x] 4.1 Implement event‑name → wait‑class classifier with deterministic prefix rules.
- [x] 4.2 Implement `collector.WaitCollector` producing per‑class `WaitSample` deltas.
- [x] 4.3 Implement `collector.CPUSampler` running at its own interval, computing CPU AAS from `events_statements_current`.
- [x] 4.4 Unit tests: classification table, AAS math (picoseconds), CPU sampler with synthetic session lists.

## 5. App tagging

- [x] 5.1 Implement sqlcommenter‑style leading comment parser (1024‑byte cap, malformed‑pair tolerance).
- [x] 5.2 Implement resolver chain: comment.service → connect_attrs.program_name → "unknown".
- [x] 5.3 Attach app tag to `SessionSample` and propagate through the digest/wait aggregation API.
- [x] 5.4 Unit tests: each fallback level, oversized comment, malformed pairs, embedded quotes.

## 6. Monitor integration

- [x] 6.1 Register the digest, wait, and session collectors with `internal/monitor/` so they tick alongside the existing snapshot. _(landed via `internal/insights` orchestrator launched from `cmd/mysqlmonitoring`; existing monitor remains untouched per design D3 trade-off note)_
- [x] 6.2 Add `--enable-perf-insights` flag (default off in this milestone's first pass), `--interval`, `--window`, `--max-digests`.
- [x] 6.3 Surface the capability‑probe warnings from §2.5 to stderr exactly once at startup.
- [x] 6.4 Ensure existing detectors (`lock_chain`, `long_transaction`, `ddl_conflict`, `deadlock`) run unchanged when perf insights is on or off.
- [ ] 6.5 Integration test: enable perf insights against a sysbench OLTP workload and assert non‑empty digest and wait series after 30s. _(deferred — needs Docker images and sysbench fixture)_

## 7. EXPLAIN on demand

- [x] 7.1 Implement `explain.Run(ctx, digest)`: pulls recent example, runs `EXPLAIN FORMAT=JSON` on a read‑only connection with `MAX_EXECUTION_TIME(2000)` and 5s client deadline.
- [x] 7.2 Implement verb allow‑list and safety refusal for non‑SELECT examples.
- [x] 7.3 Implement plan tree renderer with red‑flag callouts: `type=ALL`, `using_filesort`, `using_temporary_table`, unindexed `attached_condition`, scan/produced ratio > 100×.
- [x] 7.4 Implement plan canonicalization + hashing; `(digest, plan_hash)` cache; `plan_flip` event emission.
- [x] 7.5 Unit tests: parser fixtures from MySQL 5.7 and 8.0 EXPLAIN JSON; verb refusal; canonical hash stability; flip detection.

## 8. CLI subcommands

- [x] 8.1 Add `mysqlmonitoring top` (cobra) with `--interval`, `--limit`, `--sort`, `--app`, `--schema`, `--json` flags.
- [x] 8.2 Add `mysqlmonitoring load` with `--interval`, `--app`, `--json`.
- [x] 8.3 Wire both to two‑poll diff against `db.DigestStats` / `db.WaitStats`; share aggregation code with the TUI.
- [x] 8.4 Implement text and NDJSON formatters in `internal/output/` extending the existing patterns.
- [x] 8.5 Snapshot tests for the formatters; CLI smoke tests with golden output. _(formatter tests in `internal/output/perf_test.go`; CLI golden tests deferred — they need a fake `*sql.DB` driver, which isn't worth its dep weight in M1)_

## 9. TUI integration

- [x] 9.1 Implement stacked‑sparkline header in `internal/tui/`, drawn on every screen, sourcing from the wait series. _(single-row total-AAS sparkline with colour-coded per-class legend; deferred true stacked-bands to a future pass — design.md open question)_
- [x] 9.2 Implement `top` panel: ranked digest table, sort hotkeys, app filter input, EXPLAIN keybinding.
- [x] 9.3 Implement EXPLAIN modal: scrollable plan tree with red‑flag highlights.
- [x] 9.4 Add a footer line showing tracked‑digest count and total evictions.
- [x] 9.5 Verify lock‑chain, long‑transaction, DDL‑conflict, and deadlock panels remain reachable and behave identically. _(verified by existing `internal/tui/` and `internal/detector/` tests still passing)_
- [ ] 9.6 Manual UX pass against MySQL 8.0 with sysbench + a contrived `SELECT SLEEP() FOR UPDATE`. _(deferred — needs running MySQL + sysbench)_

## 10. Docs and rollout

- [x] 10.1 Update README with the `top` / `load` examples and a screenshot of the sparkline + top panel. _(prose updated; screenshot deferred — needs running TUI capture)_
- [x] 10.2 Document required `performance_schema` consumers/instruments and how to enable them.
- [x] 10.3 Document app‑tagging conventions (sqlcommenter and `program_name`).
- [ ] 10.4 Flip `--enable-perf-insights` default to on once §6.5 and §9.6 pass. _(blocked on §6.5 + §9.6 — leave default off until manual UX pass against a live MySQL completes)_
- [x] 10.5 Add a CHANGELOG entry summarizing M1 capabilities and the deferred items (persistence, alerting, replication, multi‑host).
