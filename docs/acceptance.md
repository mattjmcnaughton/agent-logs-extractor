# agent-logs-extractor — Acceptance Criteria

This document defines the observable behavior `agent-logs-extractor` must
exhibit to be considered correct. Each criterion is a Given / When / Then
scenario naming a concrete invocation and checkable assertions, referenced
by ID (`AC-<CATEGORY>-<NN>`). Every criterion maps 1:1 to exactly one
discharging test — §2 is the index; §3 onward are the worked scenarios.

A scenario **passes** only if every assertion in its **Then** holds.

## 1. How to read this document

### 1.1 The harness

Most criteria below (tier `e2e`) run the **compiled binary** as a
subprocess, black-box, from `tests/e2e/`: no `internal/core` or
`internal/adapters` import is permitted anywhere in that package, only
`internal/testing/logfixture` for locating fixtures (`tests/e2e/main_test.go`'s
package doc). `$ALXBIN`, if set, names the binary under test; otherwise
`TestMain` builds one itself. A `tier: unit` or `tier: integration`
criterion instead points at an existing test elsewhere in the tree — see
§1.5 and decision R5 below for why US-3/US-4's cookbook criteria are not
re-tested at the e2e tier. A `tier: container` criterion names a `just`
recipe that runs the suite inside `Dockerfile.duckdb`, network-isolated.

### 1.2 Exit-code convention (R1)

Unlike `fetch-context`'s three-way `0`/`1`/`2` split, this tool uses exactly
two exit codes, unchanged by this ticket:

| Code | Meaning |
|---|---|
| `0` | Success — including a bare invocation, which prints help and exits `0` (cobra's default for a command group with no `RunE`; see R2) |
| `1` | Any failure — a bad flag, an unknown subcommand, a missing sink, a failed sync or export |

### 1.3 Fixture topology and the numbers it yields

The e2e tier reads the same committed Claude fixture tree
`internal/testing/logfixture/claude/` that `internal/core/sync`'s and
`internal/adapters/claudesource`'s own test tiers already pin exact counts
against — **the fixture numbers below are now pinned in three places**:
`internal/core/sync/sync_integration_test.go`, this document, and the e2e
tests in `tests/e2e/`. A future fixture regeneration (see
`internal/testing/logfixture/README.md`) must update all three or the e2e
tier and the integration tier will disagree.

- The full fixture tree (3 project directories: sidechain, tool-error,
  single-turn/scratchpad) yields **3 sessions, 15 messages, 4 tool calls,
  23 records skipped** when synced.
- The pathological fixture tree (`internal/testing/logfixture/pathological/claude`)
  yields **1 session, 4 messages, 1 tool call, 14 records skipped** (12
  bookkeeping + 1 malformed line + 1 unknown record type).
- Seeding only the tool-error and single-turn/scratchpad projects (omitting
  the sidechain project) yields **2 sessions, 8 messages, 2 tool calls, 14
  records skipped**; adding the sidechain project back yields the full 3/15/4/23
  line (AC-SYNC-04).

### 1.4 Per-scenario isolation

Every e2e scenario runs against a fresh `sandbox` (`tests/e2e/setup_test.go`):
an isolated home directory that backs **both** `HOME` and, by default,
`AGENT_LOGS_EXTRACTOR_HOME` (decision D10, §12) — never the real host
`HOME`. `AC-SANDBOX-03`
is the one scenario that deliberately unsets `AGENT_LOGS_EXTRACTOR_HOME`
from the child environment; `HOME` still points at the sandbox even then,
so the scenario can never see the host's real `~/.claude`. The child
process environment is otherwise built from scratch — only `PATH` and
`TMPDIR` are inherited from the host, so no stray env var from the
developer's shell (or CI runner) can leak into a scenario.

### 1.5 Tiers

Four tiers, `docs/technical/tdd-mvp.md`'s "Testing" section:

- **unit** (no build tag) — exercised by `just test`.
- **integration** (`//go:build integration`) — `just test-integration`.
- **e2e** (`//go:build e2e`) — `just test-e2e`; opt-in, never part of `just gate`/`just gate-expensive` (R4).
- **container** — `just test-e2e-container`, the e2e suite run inside `Dockerfile.duckdb` with `--network none`; opt-in, proves AC-SCOPE-03 (no outbound network) and every duckdb-dependent (†) criterion with `ALX_REQUIRE_DUCKDB=1` already set as an image `ENV`.

`TestACCoverage` (`tests/e2e/coverage_test.go`, deliberately **untagged** so
it runs inside `just gate`) enforces that every criterion below maps to
exactly one discharging test, and that every `TestAC_*` test in
`tests/e2e/` maps back to a criterion here.

### 1.6 The `†` marker

A criterion marked **†** requires a real `duckdb` binary
(`internal/adapters/duckdbcli.requireDuckDB`-equivalent — see
`tests/e2e/assertions_test.go`'s `requireDuckDB`): it **skips** when `duckdb` is
absent from `PATH` and `ALX_REQUIRE_DUCKDB` is unset, and **fails** when
`duckdb` is absent and `ALX_REQUIRE_DUCKDB=1` (CI's native and containerized
gate-expensive jobs, and the `test-e2e-container` image, all set it).

---

## 2. Criteria index

| AC ID | Story | Statement | Test | Tier |
|---|---|---|---|---|
| AC-CLI-01 | — | `version` exits 0, non-empty stdout | `TestAC_CLI_01_VersionPrints` | e2e |
| AC-CLI-02 | — | bare invocation prints help to stdout, exits 0 | `TestAC_CLI_02_NoArgsPrintsHelp` | e2e |
| AC-CLI-03 | — | unknown subcommand exits 1, stderr names it | `TestAC_CLI_03_UnknownSubcommand` | e2e |
| AC-CLI-04 | US-5 | `export` with no sink exits 1, stderr names duckdb | `TestAC_CLI_04_ExportRequiresASink` | e2e |
| AC-CLI-05 | US-5 | `export bogus` exits 1, stderr names the sink | `TestAC_CLI_05_UnknownExportSink` | e2e |
| AC-CLI-06 | — | `--log-level bogus` exits 1 before any work | `TestAC_CLI_06_InvalidLogLevel` | e2e |
| AC-CLI-07 | — | `AGENT_LOGS_EXTRACTOR_LOG_LEVEL`: flag wins over env, invalid env value warns and falls back to `info` | `TestAC_CLI_07_LogLevelEnvVar` | e2e |
| AC-SYNC-01 | US-1 | `sync --claude-path` over the fixture tree prints the canonical summary, writes 3 store files | `TestAC_SYNC_01_FixtureTreeSummary` | e2e |
| AC-SYNC-02 | US-1 | bare `sync` resolves `~/.claude` from the sandbox home, identical summary | `TestAC_SYNC_02_BareSyncUsesSandboxHome` | e2e |
| AC-SYNC-03 | US-2 | re-run is byte-identical, no leftovers | `TestAC_SYNC_03_RerunIsIdempotent` | e2e |
| AC-SYNC-04 | US-2 | a grown source yields an updated store | `TestAC_SYNC_04_RerunPicksUpNewSessions` | e2e |
| AC-SYNC-05 | US-1 | `--vendor claude` equals bare `sync` | `TestAC_SYNC_05_VendorClaudeMatchesBareSync` | e2e |
| AC-SYNC-06 | US-1 | `--vendor codex` exits 1 naming what is available | `TestAC_SYNC_06_VendorCodexNotYetAvailable` | e2e |
| AC-SYNC-07 | US-1 | `--vendor bogus` exits 1 naming both accepted values | `TestAC_SYNC_07_UnknownVendor` | e2e |
| AC-SKIP-01 | US-6 | the pathological tree never fails a sync, exact counts | `TestAC_SKIP_01_PathologicalTreeNeverFails` | e2e |
| AC-SKIP-02 | US-6 | `--log-level debug` emits the skip-by-reason breakdown; default does not | `TestAC_SKIP_02_DebugPrintsSkipBreakdown` | e2e |
| AC-SKIP-03 | US-6 | an unreadable session file is counted, not fatal | `TestAC_SKIP_03_UnreadableFileIsCounted` | e2e |
| AC-VENDOR-01 | US-7 | no `~/.claude` at all yields an all-zero summary, exit 0 | `TestAC_VENDOR_01_MissingVendorDirIsFine` | e2e |
| AC-VENDOR-02 | US-7 | `--claude-path` pointing nowhere yields an all-zero summary, exit 0 | `TestAC_VENDOR_02_MissingOverridePathIsFine` | e2e |
| AC-EXPORT-01 † | US-5 | `export duckdb --out` exits 0, prints `wrote <p>`, no leftovers | `TestAC_EXPORT_01_WritesTheFile` | e2e |
| AC-EXPORT-02 † | US-5 | bare `export duckdb` writes the default path | `TestAC_EXPORT_02_DefaultOutPath` | e2e |
| AC-EXPORT-03 † | US-5 | the exported file's table row counts equal what `sync` printed | `TestAC_EXPORT_03_TablesMatchTheSyncSummary` | e2e |
| AC-EXPORT-04 | US-5 | no duckdb on PATH exits 1 with an install hint | `TestAC_EXPORT_04_MissingBinaryHint` | e2e |
| AC-EXPORT-05 | US-5 | `export` before any `sync` exits 1 naming `sync` as the fix | `TestAC_EXPORT_05_NoStoreYet` | e2e |
| AC-EXPORT-06 † | US-7 | an empty store exports cleanly: 3 tables, 0 rows | `TestAC_EXPORT_06_EmptyStoreExports` | e2e |
| AC-QUERY-01 | US-3 | README cookbook query 1 binds and returns the right rows | `TestCookbookQueries` (Q1 subtests, `internal/adapters/duckdbcli`) | integration |
| AC-QUERY-02 | US-4 | README cookbook query 2 ditto | `TestCookbookQueries` (Q2 subtests, `internal/adapters/duckdbcli`) | integration |
| AC-QUERY-03 | US-3, US-4 | README cookbook query 3 returns one row per project | `TestCookbookQueries` (Q3 subtest, `internal/adapters/duckdbcli`) | integration |
| AC-QUERY-04 | US-3, US-4 | the SQL under test is byte-identical to README's | `TestCookbookQueriesMatchTheREADME` (`internal/adapters/duckdbcli`) | unit |
| AC-QUERY-05 † | US-5 | the binary's own export carries the denormalized columns | `TestAC_QUERY_05_DenormalizedColumns` | e2e |
| AC-SANDBOX-01 † | US-1 | `AGENT_LOGS_EXTRACTOR_HOME` redirects both store and export, nothing written outside it | `TestAC_SANDBOX_01_HomeRedirectsStoreAndExport` | e2e |
| AC-SANDBOX-02 | US-1 | `AGENT_LOGS_EXTRACTOR_HOME` wins outright over `XDG_DATA_HOME` | `TestAC_SANDBOX_02_HomeBeatsXDG` | e2e |
| AC-SANDBOX-03 | US-1 | with the home var unset, `XDG_DATA_HOME` governs the data dir while `~/.claude` still comes from `$HOME` | `TestAC_SANDBOX_03_XDGGovernsDataDirOnly` | e2e |
| AC-SCOPE-01 | — | vendor sources are read-only: hash before/after a sync is unchanged | `TestAC_SCOPE_01_SourcesAreReadOnly` | e2e |
| AC-SCOPE-02 | — | no config file is ever read or written | `TestAC_SCOPE_02_NoConfigFile` | e2e |
| AC-SCOPE-03 | — | nothing reaches the network at run time | `just test-e2e-container` | container |

---

## 3. CLI smoke

**AC-CLI-01 — `version` prints**
- When: `agent-logs-extractor version`.
- Then: exit `0`; stdout is non-empty.

**AC-CLI-02 — bare invocation prints help**
- When: `agent-logs-extractor` (no subcommand, no flags).
- Then: exit `0` (R2 — cobra's default for a command group with no
  `RunE`, deliberately not changed by this ticket); help text printed to
  **stdout**.

**AC-CLI-03 — unknown subcommand**
- When: `agent-logs-extractor frobnicate`.
- Then: exit `1`; stderr contains `unknown command "frobnicate"`.

**AC-CLI-04 — `export` requires a sink**
- When: `agent-logs-extractor export` (no sink argument).
- Then: exit `1`; stderr contains `export requires a sink: duckdb`.

**AC-CLI-05 — unknown export sink**
- When: `agent-logs-extractor export bogus`.
- Then: exit `1`; stderr contains `unknown export sink "bogus"`.

**AC-CLI-06 — invalid `--log-level`**
- When: `agent-logs-extractor sync --log-level bogus` (any subcommand — the
  flag is validated in `PersistentPreRunE`, before the subcommand's own
  `RunE` runs).
- Then: exit `1`; stderr contains `invalid --log-level "bogus"`; no sync
  side effect occurs (the store directory is never created).

**AC-CLI-07 — `AGENT_LOGS_EXTRACTOR_LOG_LEVEL` env var**

`README.md`'s "Logging" section documents two behaviors this criterion
covers together: `--log-level` beats the env var when both are set, and
(deliberately unlike `--log-level`'s hard failure, AC-CLI-06) an invalid
env var value is a warning that falls back to `info`, not a failure. There
is no direct observable signal for "which level is in effect" other than
`--log-level debug`'s own effect (the skip-by-reason breakdown AC-SKIP-02
already pins), so that effect stands in as the proxy across all three
sub-cases below.

- Given: `agent-logs-extractor sync --claude-path <the committed Claude
  fixture root>` (same fixture and skip breakdown as AC-SKIP-02).
- When (1): `AGENT_LOGS_EXTRACTOR_LOG_LEVEL=warn` in the environment,
  `--log-level debug` on the command line.
- Then (1): exit `0`; stderr carries the debug-only skip breakdown
  (`msg="sync: skipped records"`) — the flag wins even though the env var
  alone would suppress it.
- When (2): `AGENT_LOGS_EXTRACTOR_LOG_LEVEL=debug` in the environment, no
  `--log-level` flag.
- Then (2): exit `0`; stderr carries the same debug-only skip breakdown —
  the env var alone governs the level.
- When (3): `AGENT_LOGS_EXTRACTOR_LOG_LEVEL=bogus` in the environment, no
  `--log-level` flag.
- Then (3): exit `0` (not `1` — unlike AC-CLI-06, an invalid env var value
  is a warning, not a hard error); stderr contains `invalid
  AGENT_LOGS_EXTRACTOR_LOG_LEVEL "bogus"`; stderr carries no debug-only skip
  breakdown (falls back to `info`).

Two related gaps are deliberately **out of scope**, not oversights:
`--codex-path` has no AC of its own because Codex source support itself
isn't implemented yet (nothing to point it at); US-2's "in seconds" has no
timing criterion because wall-clock performance is not part of this
ticket's observable contract and would need a dedicated, environment-
sensitive benchmark to assert honestly.

## 4. `sync`

**AC-SYNC-01 — fixture tree summary**
- Given: the sandbox's `~/.claude` is unseeded.
- When: `agent-logs-extractor sync --claude-path <the committed Claude fixture root>`.
- Then: exit `0`; stdout is **exactly**
  `claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped\n`;
  the store holds exactly 3 files under `sessions/claude/`.

**AC-SYNC-02 — bare `sync` uses the sandbox home**
- Given: the sandbox's `~/.claude/projects` is seeded with the full fixture tree (§1.3).
- When: `agent-logs-extractor sync` (no `--claude-path`).
- Then: exit `0`; stdout is identical to AC-SYNC-01's.

**AC-SYNC-03 — re-run is idempotent**
- Given: a sandbox synced once already (AC-SYNC-01's invocation).
- When: the identical `sync` invocation runs again.
- Then: exit `0`; stdout is byte-identical to the first run's; the on-disk
  `sessions/` tree hashes identically (`hashTree`) before and after; no
  `.staging-`/`.trash-`/`.orphan-` leftover directory exists (`noLeftovers`).

**AC-SYNC-04 — a grown source is picked up**
- Given: the sandbox's `~/.claude/projects` is seeded with only the
  tool-error and single-turn/scratchpad fixture projects.
- When: `sync` runs, then the sidechain fixture project is seeded in
  addition, then `sync` runs again.
- Then: the first run's stdout is **exactly**
  `claude: 2 sessions, 8 messages, 2 tool calls, 14 records skipped\n`;
  the second run's stdout is the full 3/15/4/23 line from AC-SYNC-01.

**AC-SYNC-05 — `--vendor claude` matches bare `sync`**
- Given: the sandbox seeded as AC-SYNC-02.
- When: `sync --vendor claude` runs, and separately a bare `sync` runs in an
  identically-seeded fresh sandbox.
- Then: both runs' stdout are identical.

**AC-SYNC-06 — `--vendor codex` is not yet available**
- When: `agent-logs-extractor sync --vendor codex`.
- Then: exit `1`; stderr contains
  `vendor "codex" is not supported by this build yet; available: [claude]`.

**AC-SYNC-07 — unknown `--vendor` value**
- When: `agent-logs-extractor sync --vendor bogus`.
- Then: exit `1`; stderr contains `unknown vendor "bogus"`, `"claude"`, and `"codex"`.

## 5. Skip accounting

**AC-SKIP-01 — the pathological tree never fails a sync**
- When: `sync --claude-path <the committed pathological Claude fixture root>`.
- Then: exit `0`; stdout is **exactly**
  `claude: 1 session, 4 messages, 1 tool call, 14 records skipped\n`
  (singular nouns pinned: "1 session", "1 tool call", not "1 sessions"/"1 tool calls").

**AC-SKIP-02 — the skip breakdown is a debug-only diagnostic**
- Given: the sandbox seeded with the full fixture tree.
- When: `sync --log-level debug` runs, and separately a default-level `sync` runs.
- Then: the debug run's stderr contains `msg="sync: skipped records"`,
  `reason=bookkeeping_record`, and `count=23`; the default-level run's
  stderr contains no `reason=` substring at all.

**AC-SKIP-03 — an unreadable file is counted, not fatal**
- Given: a **copy** of the full fixture tree in a mutable temp directory
  (never the committed fixture itself), with one extra file added: a
  **broken symlink** `broken.jsonl -> /nonexistent/nope.jsonl` inside one
  project directory (a broken symlink, not `chmod 000` — the container
  runs as root, where mode bits are ignored, and that variant would
  silently pass there).
- When: `sync --claude-path <the copied tree>`.
- Then: exit `0`; stdout is **exactly**
  `claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped, 1 file unreadable\n`.

## 6. Missing vendor directories

**AC-VENDOR-01 — no `~/.claude` at all**
- Given: a sandbox whose `~/.claude` does not exist.
- When: `agent-logs-extractor sync`.
- Then: exit `0`; stdout is **exactly**
  `claude: 0 sessions, 0 messages, 0 tool calls, 0 records skipped\n`.

**AC-VENDOR-02 — `--claude-path` pointing at nothing**
- When: `agent-logs-extractor sync --claude-path <a path that does not exist>`.
- Then: exit `0`; stdout is identical to AC-VENDOR-01's.

## 7. `export duckdb`

**AC-EXPORT-01 † — writes the file**
- Given: a sandbox already synced against the full fixture tree.
- When: `export duckdb --out <p>`.
- Then: exit `0`; stdout is exactly `wrote <p>\n`; `<p>` exists; no
  `.export-*` temp directory and no `<p>.wal` file survive beside it.

**AC-EXPORT-02 † — default `--out` path**
- Given: a sandbox already synced.
- When: bare `export duckdb` (no `--out`).
- Then: exit `0`; the file exists at the sandbox's default export path
  (`<data-home>/agent-logs-extractor/export/logs.duckdb`).

**AC-EXPORT-03 † — table counts match the sync summary**
- Given: a sandbox synced against the full fixture tree (3/15/4/23), then exported.
- When: the exported file is queried for `SELECT count(*) FROM sessions`,
  `... FROM messages`, `... FROM tool_calls`.
- Then: `sessions`=3, `messages`=15, `tool_calls`=4.

**AC-EXPORT-04 — missing duckdb binary**
- Given: `PATH` scrubbed of every directory containing a `duckdb`
  executable (unconditionally — this assertion must hold whether or not the
  host actually has duckdb installed).
- When: `export duckdb --out <p>` runs with that scrubbed `PATH`.
- Then: exit `1`; stderr contains `duckdb binary not found`,
  `https://duckdb.org/docs/installation/`, and a note that `sync` needs no
  binary.

**AC-EXPORT-05 — no store yet**
- Given: a fresh sandbox that has never run `sync`.
- When: `export duckdb --out <p>`.
- Then: exit `1`; stderr contains `no canonical store`, the store path, and
  `` run `sync` first ``. This check is duckdb-independent
  (`storeHasDocs` runs before `exec.LookPath`), so it must exit `1` with
  this message even when duckdb is entirely absent from `PATH`.

**AC-EXPORT-06 † — an empty store exports cleanly**
- Given: a sandbox where `sync` ran against an empty (missing) `~/.claude`
  (AC-VENDOR-01), so the store exists but holds zero session files.
- When: `export duckdb --out <p>`.
- Then: exit `0`; the exported file has 3 tables (`sessions`, `messages`,
  `tool_calls`), each with 0 rows.

## 8. Cookbook queries (US-3, US-4)

**AC-QUERY-01 — cookbook query 1 binds and returns the right rows**
- Discharged by `TestCookbookQueries`'s Q1 subtests in
  `internal/adapters/duckdbcli` (integration tier, real `duckdb`). See
  decision R5: not re-tested at the e2e tier.

**AC-QUERY-02 — cookbook query 2 binds and returns the right rows**
- Discharged by `TestCookbookQueries`'s Q2 subtests, same package.

**AC-QUERY-03 — cookbook query 3 returns one row per project**
- Discharged by `TestCookbookQueries`'s Q3 subtest, same package.

**AC-QUERY-04 — the tested SQL matches README's**
- Discharged by `TestCookbookQueriesMatchTheREADME` (unit tier, no duckdb
  needed): parses the ` ```sql ` fences out of `README.md` and fails the
  moment either copy drifts from the Go constants the acceptance test
  actually runs.

**AC-QUERY-05 † — the binary's own export carries the denormalized columns**
- Given: a sandbox synced against the full fixture tree, then exported.
- When: the exported file is queried for
  `SELECT vendor, project_name, project_path FROM messages LIMIT 1`.
- Then: the row's `vendor`, `project_name`, and `project_path` columns are
  all non-empty — proving the binary's own export (not just the adapter's
  unit-tested `Script`) actually denormalizes these onto `messages`.

## 9. Sandboxing

**AC-SANDBOX-01 † — `AGENT_LOGS_EXTRACTOR_HOME` redirects store and export**
- Given: a sandbox with `AGENT_LOGS_EXTRACTOR_HOME` set to its home dir (the
  default — see §1.4). The `export duckdb` half needs a real `duckdb`
  binary (§1.6); the `sync` half does not.
- When: `sync` then `export duckdb` (no `--out`) both run.
- Then: the store and the exported file both land under
  `<home>/.local/share/agent-logs-extractor/`; nothing is written outside
  the sandbox's home directory.

**AC-SANDBOX-02 — the home var wins outright over `XDG_DATA_HOME`**
- Given: a sandbox with both `AGENT_LOGS_EXTRACTOR_HOME` (default) and
  `XDG_DATA_HOME` (pointed at a second, distinct temp directory) set.
- When: `sync` runs.
- Then: the store lands under `AGENT_LOGS_EXTRACTOR_HOME`'s data dir; the
  `XDG_DATA_HOME` directory receives nothing.

**AC-SANDBOX-03 — with the home var unset, XDG governs the data dir only**
- Given: a sandbox with `AGENT_LOGS_EXTRACTOR_HOME` **unset** (never sent to
  the child at all — see §1.4), `HOME` still pointed at the sandbox
  (guarding against ever reading a real `~/.claude`), and `XDG_DATA_HOME`
  set to a distinct temp directory.
- When: `sync` runs.
- Then: the store lands under `<XDG_DATA_HOME>/agent-logs-extractor/store`,
  **not** under `<home>/.local/share/...`; the sync summary still reflects
  whatever `~/.claude` (i.e. `<sandbox home>/.claude`) held, proving vendor
  roots come from `$HOME` regardless of `XDG_DATA_HOME`.

## 10. Scope and negative assertions

**AC-SCOPE-01 — vendor sources are read-only**
- Given: a fresh **copy** of the full fixture tree in a mutable temp
  directory (never the committed fixture — see the ticket's fixture-safety
  rule).
- When: `hashTree` runs before and after `sync --claude-path <the copy>`.
- Then: the two hashes are identical.

**AC-SCOPE-02 — no config file**
- Given: a sandbox with a plausible config path
  (`<home>/.config/agent-logs-extractor/config.yaml`) pre-populated with
  garbage content.
- When: `sync` runs.
- Then: exit `0`; the garbage file is byte-for-byte unchanged (never read,
  since a bad config would otherwise fail parsing; never overwritten).

**AC-SCOPE-03 — nothing reaches the network**
- Discharged by `just test-e2e-container`: the whole e2e suite run inside
  `Dockerfile.duckdb` with `docker run --network none`. Not independently
  verifiable in this repository's authoring sandbox (no working container
  daemon there — same caveat `Dockerfile.duckdb`'s own header already
  documents for the integration tier's containerized job); CI's
  `gate-expensive-container`-style job is where this criterion gets its
  first real proof, mirroring how `test-integration-container` was
  originally landed.

---

## 11. User-story coverage

| Story | Criteria |
|---|---|
| US-1 | AC-SYNC-01, 02, 05, 06, 07; AC-SANDBOX-01, 02, 03 |
| US-2 | AC-SYNC-03, 04 |
| US-3 | AC-QUERY-01, 03, 04 |
| US-4 | AC-QUERY-02, 03, 04 |
| US-5 | AC-EXPORT-01–06; AC-QUERY-05; AC-CLI-04, 05 |
| US-6 | AC-SKIP-01, 02, 03 |
| US-7 | AC-VENDOR-01, 02; AC-EXPORT-06 |

---

## 12. Resolved decisions

**R1 — Exit codes stay 0/1, not fetch-context's 0/1/2.** This ticket is
tests-and-docs; adopting a three-way exit-code convention (success / runtime
failure / usage error) would be a user-visible behavior change and is out
of scope. `agent-logs-extractor` uses `0` for success and `1` for every
failure, full stop (§1.2). Revisit as its own ticket if a caller ever needs
to distinguish "usage error" from "runtime failure" programmatically.

**R2 — A bare invocation prints help and exits 0.** This is cobra's default
behavior for a command group with no `RunE` of its own, already true before
this ticket, and is recorded here as a resolved (not accidental) decision:
changing it to exit non-zero (fetch-context's `AC-USAGE-01` convention)
would be part of the same R1 exit-code redesign, not a drive-by fix here.

**R3 — `ALX_CONTRACT_CLAUDE_PATH` is test-only.** It exists solely so the
contract tier (`internal/adapters/claudesource/claudesource_contract_test.go`)
can be validated — both its skip path and its found-logs path — without a
developer's real `~/.claude`. It is never read by the CLI or by wiring, is
documented in `docs/development.md`, and is deliberately absent from
`README.md` and from every `--flag`.

**R4 — `gate`/`gate-expensive` stay exactly as they were.** The contract and
e2e tiers are opt-in (`just test-contract`, `just test-e2e`,
`just test-e2e-container`) and never run as part of `just gate` or
`just gate-expensive` — CI (`.github/workflows/ci.yml`) is unchanged by this
ticket. The one addition to the untagged suite `just gate`/`just test`
already runs is `TestACCoverage` (§1.5), which is pure doc/AST parsing with
no subprocess, no binary, and no duckdb, so it costs nothing to gate on.

**R5 — US-3/US-4 are not re-tested at the e2e tier.** The cookbook queries
are already proven end-to-end against a real `duckdb` binary by
`TestCookbookQueries` (integration tier) and pinned byte-identical to
README's fences by `TestCookbookQueriesMatchTheREADME` (unit tier) —
re-running the same three queries through the compiled binary would add
process-spawn cost without covering anything those tests don't already
cover. The two binary-level gaps that genuinely are new — does the real
`export duckdb` invocation actually produce the row counts `sync` promised
(AC-EXPORT-03), and does it actually carry the denormalized columns the
cookbook queries depend on (AC-QUERY-05) — get their own thin e2e criteria
instead.

**R6 — Container e2e reuses `Dockerfile.duckdb`.** `just test-e2e-container`
builds and runs the same image `just test-integration-container` already
does, only substituting the `CMD` at run time (`docker run --rm --network
none <img> just test-e2e`) — no second Dockerfile, no named volumes (the
image warms its Go module cache at build time; no bind mounts are needed at
run time). `ALX_REQUIRE_DUCKDB=1` is already an image `ENV`, so every `†`
criterion hard-fails inside the container rather than silently skipping.

The three decisions below (T3) were cited by their short D-series names in
code comments (`tests/e2e/main_test.go`, `coverage_test.go`,
`setup_test.go`) from the start, but were never actually written down
anywhere — the citations pointed at "the ticket plan this document was
written from," a document that was never part of this repo. They are
promoted here, under their existing names, so every `D1`/`D5`/`D10`
reference in the tree now resolves.

**D1 — an unset `$ALXBIN` builds a binary, it does not fail.**
`tests/e2e/main_test.go`'s `TestMain` resolves `$ALXBIN` once for the whole
suite: if unset, it builds `./cmd/agent-logs-extractor` itself into a temp
directory it cleans up afterward, and exports `ALXBIN` so every sandbox
invocation resolves the same binary. The `just test-e2e` /
`test-e2e-container` recipes pre-build and set `$ALXBIN` so a full suite
run shares one binary instead of paying a rebuild per test file; a
developer running `go test -tags=e2e ./tests/e2e/...` directly still gets a
working suite with no setup step of their own.

**D5 — `TestACCoverage` carries no build tag.** It never spawns the binary,
never touches duckdb, never needs a fixture — it only parses this document
and the Go source under `tests/e2e/` and the rest of the repo — so it runs
as part of the untagged `go test ./...` / `just gate` (§1.5) and proves the
AC-ID ↔ test mapping stays honest on every push, not just when someone
remembers to run the opt-in e2e tier. It is deliberately the one file in
`tests/e2e/` without `//go:build e2e`.

**D10 — every e2e run sets both `HOME` and `AGENT_LOGS_EXTRACTOR_HOME`.**
`sandbox`'s child environment is built from scratch (§1.4): `HOME` always
points at the sandbox's isolated temp directory, and
`AGENT_LOGS_EXTRACTOR_HOME` is set to the same value by default — both, not
just one, so a scenario can never fall through to the real host `HOME` no
matter which precedence path the binary takes internally.
`AC-SANDBOX-03`'s `unsetAgentLogsExtractorHome()` omits
`AGENT_LOGS_EXTRACTOR_HOME` from the child environment for that one
scenario, but `HOME` still points at the sandbox even then.
