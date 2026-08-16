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
| `contract` | Parses a developer's own live `~/.claude` (or a fixture tree pointed at by `ALX_CONTRACT_CLAUDE_PATH`) | `just test-contract` | **No — never gated** |
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
| `adapters/duckdbcli` (cookbook) | Real `duckdb`, real export of the committed fixtures | `cookbook_integration_test.go` |
| `core/sync` | Real fixture trees on a real temp filesystem | `sync_integration_test.go` |
| `tests/release` | The real Go toolchain: builds with the release workflow's own `-ldflags`, cross-compiles all four matrix targets | `tests/release/ldflags_integration_test.go` |

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

**Location.** `internal/adapters/claudesource/claudesource_contract_test.go`,
build tag `//go:build contract`. Run only via `just test-contract`; never
in `just gate`/`just gate-expensive`.

**What it catches.** It parses whatever `~/.claude` tree
`ALX_CONTRACT_CLAUDE_PATH` points at and asserts the structural invariants
every `SessionDoc` must satisfy, then reports drift observations via
`t.Log`: unknown record types, malformed lines, dangling links, a session's
`cwd` changing mid-conversation (`cwd_drift` — instruments
`docs/technical/tdd-mvp.md`'s open question 2), and more. It is **strictly
read-only**: only `Source.List`/`Source.Parse` are ever called, never a
`CanonicalStore`.

**Why the engine is split out.** `internal/testing/invariants` (§4's
"pure functions" table above) is what makes this tier provable at all in
an environment with no live vendor history: `Check(doc) []Violation`
reports hard structural failures a conforming source adapter must never
produce; `Observe(doc) []Observation` reports drift signals that are never
fatal on their own. Both are exhaustively unit-tested (no build tag,
`invariants_test.go`) against hand-built `SessionDoc`s, so the *rules* are
proven by `just test`; the contract test itself only proves that a real
`~/.claude` still parses into docs those rules accept.

**The total-ingestion-loss hard failure.** The contract test also fails
outright (not just observes) if the resolved root yields session files but
zero sessions, or sessions but zero messages — total ingestion loss, e.g.
from a vendor renaming a field this tool depends on, is a hard failure,
never a silent zero-row pass.

**The require-don't-default rule, and its privacy rationale.**
`resolveContractRoot` **requires `ALX_CONTRACT_CLAUDE_PATH` to be set at
all** — with it unset, the test SKIPs immediately rather than falling back
to `AGENT_LOGS_EXTRACTOR_HOME`/`$HOME` or any other live `~/.claude`. This
is deliberate and mechanical: CI never sets `ALX_CONTRACT_CLAUDE_PATH`, and
a contributor's own sandbox transcript (or any other live history) must
never be read by accident just because the env var was forgotten. The
variable is **test-only** — never read by the CLI or by wiring, never a
flag, never documented in `README.md` (`docs/acceptance.md` §13 R3).

**The two invocation recipes** (see `docs/development.md` for the exact
command lines): point `ALX_CONTRACT_CLAUDE_PATH` at an empty directory to
exercise the skip path, or at the committed fixture tree (or your own real
`~/.claude`) to exercise the found-logs path.

**Consent warning.** Never point `ALX_CONTRACT_CLAUDE_PATH` at another
session's or another person's real Claude Code history without their
consent — this tier only reads, but a transcript is still someone's
private conversation log.

## E2E tier

**Location.** `tests/e2e/` (top-level, not under `internal/`). Build tag
`//go:build e2e`, except `coverage_test.go` (§10). Run via `just test-e2e`.

**Black-box import rule.** No `internal/core` or `internal/adapters` import
is permitted anywhere in `tests/e2e/` — the only `internal/` import allowed
is `internal/testing/logfixture`, for locating fixtures. This is what keeps
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

## Fixtures and their provenance (D8)

Real, scrubbed vendor session logs live under `internal/testing/logfixture/`
and are **verbatim ground truth** — never hand-edit or move them.
Regenerate with `just scrub-fixture`; see
`internal/testing/logfixture/README.md` for the exact commands used to
generate and scrub each committed fixture, and for the redaction rules the
scrub engine applies.

**Two documented exceptions to "never hand-edit":**

1. The single-turn scratchpad fixture
   (`claude/projects/-tmp-claude-0-...-scratchpad-fixture-project/`)
   predates `just scrub-fixture` and was hand-scrubbed.
2. **All of `pathological/`** — malformed inputs exercising the
   skip-and-count contract — cannot be tool-generated even in principle:
   the scrub engine requires its input to already be a well-formed JSON
   object and rejects anything else, so a tool whose whole job is refusing
   malformed input cannot be used to produce malformed input.

**The golden-file coupling.** Regenerating a Claude fixture changes ids,
`toolu_` ids, and timestamps, so it forces regenerating
`internal/adapters/claudesource`'s golden files too:
`go test ./internal/adapters/claudesource -run TestGoldenParse -update`,
then review the diff as a drift report — id/timestamp churn is expected,
but a changed message/tool-call count or a new `unknown_record_type` is a
real behavior change to investigate, not to wave through.

**The path-charset rule.** A fixture's generating source path must contain
only `[A-Za-z0-9/-]` — Claude Code encodes a project directory name from
the cwd by replacing every non-alphanumeric character with `-`, while the
scrub engine's path renaming rewrites only `/`; any other character makes
the two diverge silently. See `internal/testing/logfixture/README.md` for
the full rule and the alphanumeric `mktemp` template it recommends.

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

Two workflows, in sequence.

### `.github/workflows/ci.yml` (**CI**)

Runs three jobs on every push and pull request to `main`:

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

**No CI job runs the e2e or contract tier.** Both are opt-in, run by name
(see the E2E and Contract tier sections above). This is why AC-RELEASE-01
is discharged at the *integration* tier rather than the e2e tier: a
criterion about the release pipeline that CI never executes would prove
very little.

### `.github/workflows/release.yml` (**Release**)

Not triggered by a push at all. It fires via `workflow_run` on the **CI**
workflow completing, and guards on
`github.event.workflow_run.conclusion == 'success'` plus a `push` to `main`
from this repository — so **any failing CI job blocks the release**, and a
fork cannot drive it. It runs semantic-release, then cross-compiles and
uploads four binaries against the tag semantic-release just cut. See
`docs/development.md`'s "Releasing" for the full flow.

Two consequences for testing:

- **This workflow has never executed.** A `workflow_run` trigger by design
  does not fire from a pull request, so the file's first real run is always
  the first push to `main` after it merges — the same caveat
  `Dockerfile.duckdb`'s containerized job carried when it landed. Its first
  runs are rehearsals rather than releases: `.releaserc.json` sets
  `"dryRun": true`, so everything up to publishing is exercised for real
  while nothing is created. `TestReleaseDryRunIsExplicit` keeps that switch
  present and unambiguous without pinning which way it points.
- **So it is tested by being read, not run.** `tests/release/` parses
  `release.yml` as data and checks what can be checked without executing
  it: that the `-ldflags` `-X` symbol path names a variable this tree
  actually declares (`TestReleaseWorkflowLdflagsPathMatchesTree` — the
  highest-value check here, since a wrong `-X` path links successfully and
  silently ships binaries reporting `dev`), that the build target is a real
  main package, that the `workflow_run` trigger names the workflow `ci.yml`
  actually declares (`TestReleaseWorkflowBindsToTheCIWorkflowName` — a
  trigger naming a nonexistent workflow is not an error to GitHub, it just
  never fires), that the Go pins agree, that the matrix covers exactly four
  targets under four distinct asset names that each describe the platform
  they were built for, that the `release` → `build-binaries` output handoff
  agrees on `new-release-version`/`new-release-published` end to end
  (`TestReleaseOutputHandoffIsWired` — a rename anywhere along that chain
  publishes a tag and a GitHub Release with zero binaries attached, green),
  that the `v` tag prefix `ref:`/`tag_name:` hardcode is the one
  semantic-release will have tagged with, that `.releaserc.json` is
  well-formed and lists its plugins in the order their side effects require,
  and that `pnpm-lock.yaml` still matches `package.json` so
  `pnpm install --frozen-lockfile` cannot fail on main. Those are untagged,
  so they run in `just gate`.
  `TestReleaseLdflagsInjectVersion` (AC-RELEASE-01) and
  `TestReleaseMatrixTargetsCompile` carry the `integration` tag and run in
  `just gate-expensive`.

## See also

- `docs/architecture.md` — the hexagonal layout that makes this test
  pyramid cheap to populate.
- `docs/acceptance.md` — the AC IDs every `e2e`-tier test (and a handful of
  `unit`/`integration`/`container`-tier tests) maps to.
- `internal/testing/logfixture/README.md` — full fixture provenance,
  regeneration commands, and the scrub engine's redaction rules.
