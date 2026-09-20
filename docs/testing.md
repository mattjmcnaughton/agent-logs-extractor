# Testing Strategy

## Goals

1. **Unit tests carry most of the confidence.** Pure domain logic (`model`,
   `invariants`, `jsonlstore/naming.go`, `duckdbcli/script.go`) and use-case
   orchestration against fakes are fast, in-process, and gated on every
   push.
2. **Integration tests target one adapter against one real local
   dependency.** No mocks of our own ports; the dependency on the other
   side of the port is real (a real filesystem, a real `duckdb` binary),
   just hermetic.
3. **Contract tests verify our adapters' understanding of *undocumented
   vendor log formats* still matches reality.** Both Claude Code's and
   Codex's on-disk formats are unofficial and can drift release to
   release; the contract tier is the early-warning system, opt-in and
   never gated.
4. **E2E tests exercise the compiled binary against every acceptance
   criterion in `docs/acceptance.md`** that a black-box process invocation
   can observe.

The architecture in `docs/architecture.md` is what makes (1) and (3) cheap:
narrow ports plus pure domain packages give a large fast-test surface, and
splitting the contract tier's *engine* into a pure, unit-tested package
(`internal/testing/invariants`, §7 below) is what makes it provable at all
in an environment with no live vendor history.

## Build tags

| Tag | Meaning | Run by | Gated? |
|---|---|---|---|
| _none_ | Pure unit tests; fast; no I/O outside the Go test runtime | `just test` | Yes — `just gate` |
| `integration` | Touches a real local dependency (filesystem, a real `duckdb` binary when present) | `just test-integration` | Yes — `just gate-expensive` |
| `contract` | Parses explicitly selected Claude/Codex trees via `ALX_CONTRACT_CLAUDE_PATH` / `ALX_CONTRACT_CODEX_PATH` | `just test-contract` | **No — never gated** |
| `e2e` | Execs the compiled binary as a subprocess | `just test-e2e` / `just test-e2e-container` | **No — never gated** |

`contract` and `e2e` are excluded from `just gate` and `just gate-expensive`
on purpose — `justfile`'s comment above `gate` explains why, verbatim:

> `gate`/`gate-expensive` deliberately do NOT run test-contract or test-e2e
> (do not "helpfully" add them here): test-contract needs a developer's
> real vendor history to mean anything, and test-e2e needs a locally-built
> binary and (for its † criteria) a real duckdb — costs `gate`'s "fast"
> and `gate-expensive`'s existing "needs only Go + a pinned duckdb CLI"
> promises were never meant to carry. Both stay opt-in, run by name.

The one addition to the gated, untagged suite is `TestACCoverage` (§10
below) — pure doc/AST parsing, no subprocess, no binary, no duckdb, so it
costs nothing to gate on even though the e2e tier itself does not run.

## Unit tests

**Location.** Same package as the code under test, no build tag. Run via
`just test`.

**Two flavors:**

### Pure functions

`internal/core/model`, `internal/testing/invariants`,
`internal/adapters/jsonlstore/naming.go` (the id-to-path escaping rules),
and `internal/adapters/duckdbcli/script.go` (the generated SQL, pinned
against golden files) are pure — no port dependencies, no I/O. They get
exhaustive, mostly table-driven tests pinning behavior other layers depend
on.

### Use cases against fakes

`internal/core/sync` and `internal/core/export` are tested against
`internal/testing/fakes/` — see §5 below. Tests assert orchestration and
observable outcomes: what ended up in the fake store, what a fake exporter
was asked to produce, which vendor a summary credited a session to.

**Untagged outliers** — six files carry no build tag despite touching more
than "pure function or fake," each for a specific reason:

| File | Why untagged |
|---|---|
| `internal/core/arch_test.go` | Must run on every push to enforce core purity (decision 1); it only parses Go syntax, no I/O |
| `tests/e2e/coverage_test.go` (`TestACCoverage`) | D5 — parses `docs/acceptance.md` and Go source, no subprocess, no binary, no duckdb, so it can gate the AC-ID mapping without paying the e2e tier's cost |
| `internal/adapters/duckdbcli/cookbook_test.go` | D11's README anti-drift guard (`TestCookbookQueriesMatchTheREADME`) needs no `duckdb` binary — it only parses `README.md`'s fenced SQL and compares it against the Go constants the integration-tier `TestCookbookQueries` actually runs |
| `internal/adapters/duckdbcli/cookbook_query_test.go` | Not a test file at all in the functional sense — it holds the `CookbookQuery1`/`2`/`3` string constants `cookbook_test.go` and the integration-tier `TestCookbookQueries` both reference; untagged so both can import it |
| `internal/ports/canonicalstore_test.go` | `ValidSessionDoc` is a pure predicate; testing it needs nothing an adapter or a build tag would add |
| `tests/docs/docs_test.go` | This ticket (#12) — a mechanical drift check between the docs and the tree; pure file/AST parsing, no I/O beyond reading local files |

## Fakes over mocks (D2)

Hand-written, in-memory fakes for every port live in
`internal/testing/fakes/fakes.go`. Tests assert on their **observable
state** — what a commit left in the store, what a sink was asked to
produce, which paths a source was asked to read — "never on the order
calls happened in or how many times a method ran" (`fakes.go:1-6`'s
package doc).

| Fake | Implements | Notes |
|---|---|---|
| `FakeConversationSource` | `ports.ConversationSource` | Seeded per root/path; `Listed`/`Parsed` record what was asked, not in what order |
| `FakeCanonicalStore` / `FakeStoreRebuild` | `ports.CanonicalStore` / `ports.StoreRebuild` | Mirrors the real store's `ErrRebuildFinished`/`ErrInvalidSessionDoc`/`firstErr`-poisons-`Commit` contract exactly |
| `FakeExporter` | `ports.Exporter` | Records every `ExportRequest` it received, in order, so a test can assert what was asked for |

**The sentinel-aliasing rule.** `fakes.go` does not declare its own
`ErrRebuildFinished`/`ErrInvalidSessionDoc` — it aliases
`ports.ErrRebuildFinished` / `ports.ErrInvalidSessionDoc` directly (`var
ErrRebuildFinished = ports.ErrRebuildFinished`), so a test written against
either the fake or the real `jsonlstore` adapter can `errors.Is` against
one sentinel regardless of which `CanonicalStore` it is pointed at.

## Integration tier

**Location.** Same package as the adapter under test, file suffix
`*_integration_test.go`, build tag `//go:build integration`. Run via `just
test-integration` (and as part of `just gate-expensive`).

| Adapter | Real local dependency | File |
|---|---|---|
| `adapters/cli` | Full `sync` against a real temp filesystem | `sync_integration_test.go` |
| `adapters/jsonlstore` | Real `os.TempDir()` store root | `jsonlstore_integration_test.go` |
| `adapters/duckdbcli` | A real `duckdb` binary on `PATH` | `duckdbcli_integration_test.go` |
| `adapters/duckdbcli` (cookbook) | Real `duckdb`, real export of the synthetic examples | `cookbook_integration_test.go` |
| `core/sync` | Invented records on a real temp filesystem | `sync_integration_test.go` |

**The duckdb skip-vs-fail rule.** `internal/adapters/duckdbcli`'s
integration tests need a real `duckdb` CLI on `PATH`; `requireDuckDB(t)`
skips (not fails) when it's absent, so a plain `just test-integration` /
`just gate-expensive` run without duckdb installed reports those tests as
**skipped**, not failing. Set `ALX_REQUIRE_DUCKDB=1` to turn that skip into
a hard failure — what CI's native `gate-expensive` job does, and what
`Dockerfile.duckdb` sets as an image `ENV`.

`just test-integration-container` runs the same tier inside a container
with duckdb baked in (`Dockerfile.duckdb`), network-isolated at run time,
for a developer who doesn't want to install duckdb locally at all.

## Contract tier

Live vendor checks are **local-only and explicitly opt-in**. They never run in
CI or either gate. `just test-contract` requires `ALX_CONTRACT_CLAUDE_PATH`
and/or `ALX_CONTRACT_CODEX_PATH`; unset paths and empty sources skip. A set `CI`
variable also forces a skip. There is no implicit HOME lookup.

`internal/testing/contracts` reads raw records and checks them against parsed
output; `internal/testing/invariants` checks the unified model. Tests for both
packages use invented examples and run in CI. The live wrappers use discard
loggers and report only aggregate counts and fixed diagnostic labels. They do
not save raw logs, normalized documents, paths, IDs, text, or error details.

See [Live log contracts](log-contracts.md) for the assumptions, limitations,
report interpretation and commands. The internal maintainer skill
[validate-log-contracts](../tests/skills/validate-log-contracts/SKILL.md) guides
local verification before updating a vendor mapping.

## E2E tier

**Location.** `tests/e2e/` (top-level, not under `internal/`). Build tag
`//go:build e2e`, except `coverage_test.go` (§10). Run via `just test-e2e`.

**Black-box import rule.** No `internal/core` or `internal/adapters` import
is permitted anywhere in `tests/e2e/` — the only `internal/` import allowed
is `internal/testing/testlogs`, for generating synthetic examples. This is what keeps
the tier honestly black-box: it only ever talks to the compiled binary.

**One Go test function per AC ID.** The pattern is
`TestAC_<CATEGORY>_<NN>_<ShortName>`:

```go
func TestAC_SYNC_03_RerunIsIdempotent(t *testing.T) { /* ... */ }
func TestAC_EXPORT_04_MissingBinaryHint(t *testing.T) { /* ... */ }
```

| File | AC category |
|---|---|
| `cli_test.go` | `AC-CLI-*` |
| `sync_test.go` | `AC-SYNC-*` |
| `skip_test.go` | `AC-SKIP-*` |
| `vendor_test.go` | `AC-VENDOR-*` |
| `export_test.go` | `AC-EXPORT-*` |
| `query_test.go` | `AC-QUERY-05` |
| `sandbox_test.go` | `AC-SANDBOX-*` |
| `scope_test.go` | `AC-SCOPE-01`, `AC-SCOPE-02` |

**D1 — an unset `$ALXBIN` builds a binary, it does not fail.**
`main_test.go`'s `TestMain` resolves `$ALXBIN` once for the whole suite: if
unset, it builds `./cmd/agent-logs-extractor` itself into a temp directory
it cleans up afterward, and exports `ALXBIN` so every sandbox invocation
resolves the same binary. `just test-e2e` (`go test -tags=e2e -v -count=1
./tests/e2e/...`, per the justfile) needs no setup step of its own — it
relies entirely on this build-if-unset behavior.

**D10 — every scenario sets both `HOME` and `AGENT_LOGS_EXTRACTOR_HOME`.**
`sandbox`'s child environment is built from scratch (`setup_test.go`):
`HOME` always points at the scenario's isolated temp directory, and
`AGENT_LOGS_EXTRACTOR_HOME` is set to the same value by default — both, not
just one, so a scenario can never fall through to the real host `HOME` no
matter which precedence path the binary takes internally. `AC-SANDBOX-03`
is the one scenario that unsets `AGENT_LOGS_EXTRACTOR_HOME` from the child
environment, but `HOME` still points at the sandbox even then. Only `PATH`
and `TMPDIR` are otherwise inherited from the host, so no stray env var
from the developer's shell (or CI runner) can leak into a scenario.

**The `†` marker.** A criterion marked `†` in `docs/acceptance.md` needs a
real `duckdb` binary; those tests skip (not fail) without one, same as the
integration tier's duckdb-dependent tests, and fail under
`ALX_REQUIRE_DUCKDB=1`.

**The container recipe is AC-SCOPE-03's own proof.** `just
test-e2e-container` reuses `Dockerfile.duckdb` (no second image), running
`docker run --rm --network none <img> just test-e2e` — this is
`docs/acceptance.md`'s AC-SCOPE-03: nothing reaches the network at run
time. `ALX_REQUIRE_DUCKDB=1` is already an image `ENV`, so every `†`
criterion hard-fails inside the container instead of skipping.

## Synthetic examples

`internal/testing/testlogs` generates small invented Claude and Codex records
inside test-owned temporary directories. No captured vendor JSONL, metadata
sidecars, or derived session JSON goldens are retained in the repository.
Tests assert explicit expected fields, counts, joins, raw-byte preservation,
and malformed-input handling. Hand-built records in adapter unit tests cover
additional edge cases. The SQL-only DuckDB goldens remain: they contain schema
and export SQL, not vendor data.

Synthetic examples specify intended behavior; they cannot establish that a
vendor still writes that format. Live compatibility is checked locally through
the opt-in contract tier. CI deliberately proves only behavior against our
assumptions. Changes to shared examples require updating the counts in
`docs/acceptance.md` and their integration/e2e assertions together.

## The AC-ID rule

Every `AC-*` ID in `docs/acceptance.md` maps 1:1 to exactly one
discharging test. `tests/e2e/coverage_test.go`'s `TestACCoverage`
(untagged — see §4's outlier table) enforces it with five checks:

- (a) the set of `**AC-...**` headings in the document equals the set of
  rows in §2's criteria index table — no orphaned heading, no indexed row
  missing its worked scenario.
- (b) every `tier: e2e` row has exactly one `TestAC_<CAT>_<NN>_*` function
  under `tests/e2e/`.
- (c) every such `TestAC_*` function found maps back to a `tier: e2e` row —
  no orphaned test.
- (d) every `tier: unit` or `tier: integration` row's Test column names a
  `TestXxx` function that exists **somewhere in the repo** (not just
  `tests/e2e/`).
- (e) every `tier: container` row's Test column names a `just <recipe>`
  that is actually defined in the justfile.

Four tier values appear in the table: `e2e`, `unit`, `integration`,
`container` — checked against, respectively, the discovered
`tests/e2e/*_test.go` functions, every `TestXxx` function anywhere in the
tree, the same set, and the justfile's own recipe names.

**Adding or removing an AC.** Add the `**AC-ID — ...**` worked scenario and
its §2 index row together (check (a) fails if only one exists); for a
`tier: e2e` row, add the matching `TestAC_*` function in `tests/e2e/`
before `TestACCoverage` will pass. Removing an AC means removing both the
heading/row and its discharging test in the same change.

## CI

`.github/workflows/ci.yml` runs three jobs on every push and pull request
to `main`:

- **`Gate`** — `just gate` on a plain Ubuntu runner (no duckdb install).
  Kept alongside the more expensive jobs deliberately, even though it's a
  strict subset of them: it gives fast feedback on the common failure
  (a unit/vet/fmt break) without waiting on a duckdb download.
- **`Gate (expensive, native duckdb)`** — the **load-bearing** proof of the
  duckdb integration tier: installs a version-pinned `duckdb` release
  (checksum-verified) on the runner, sets `ALX_REQUIRE_DUCKDB=1`, and runs
  `just gate-expensive`.
- **`Gate (expensive, containerized duckdb)`** — **additive, not
  load-bearing**: proves the same integration tier again inside a
  container built from `Dockerfile.duckdb`, run with `docker run --network
  none`. Reuses `Dockerfile.duckdb`, not a second image, and can be
  reverted independently of the native job if it proves flaky.

## See also

- `docs/architecture.md` — the hexagonal layout that makes this test
  pyramid cheap to populate.
- `docs/acceptance.md` — the AC IDs every `e2e`-tier test (and a handful of
  `unit`/`integration`/`container`-tier tests) maps to.
- `docs/log-contracts.md` — local verification and report interpretation.
