# Architecture

## Overview

agent-logs-extractor is a Go CLI built with strict hexagonal
(ports-and-adapters) architecture, following the same shape as
`fetch-context`:

- The **core** (`internal/core/`) contains pure use cases and domain
  services — see Layering below for what "pure" means and how it's
  enforced.
- **Ports** (`internal/ports/`) are small Go interfaces the core depends on.
- **Adapters** (`internal/adapters/`) are concrete implementations of ports,
  one adapter per external dependency.
- **Wiring** (`cmd/agent-logs-extractor/main.go`) is the only file that
  imports every concrete adapter: it reads the environment, constructs
  adapters, injects them into use cases, and hands those to the cobra root.

The hexagon's outside edge is the Claude Code log tree, the local
filesystem, and the `duckdb` CLI — three edges, three ports
(`ports.ConversationSource`, `ports.CanonicalStore`, `ports.Exporter`).
Codex is a fourth edge the ports already anticipate (`model.VendorCodex`
exists; `ports.ConversationSource` is vendor-agnostic) but no `codexsource`
adapter has landed yet — see "Codex is not implemented" in `CLAUDE.md`.

## Project Structure

```
cmd/agent-logs-extractor/
  main.go          # Wiring: env -> adapters -> use cases -> cobra root
internal/
  core/            # Pure use cases + domain model, zero infra imports
    model/         # Unified data model (SessionDoc/Session/Message/ToolCall)
    sync/          # Sync use case
    export/        # Export use case
    arch_test.go   # TestCorePurity: mechanically enforces the purity rule below
  ports/           # Interfaces the core depends on
    conversationsource.go  # ConversationSource
    canonicalstore.go      # CanonicalStore, StoreRebuild, ValidSessionDoc
    exporter.go             # Exporter
  adapters/
    cli/           # Driving adapter: cobra subcommands, one file each (thin shims)
    claudesource/  # ConversationSource for ~/.claude/projects (Claude Code)
    jsonlstore/    # CanonicalStore over afero: JSONL store + atomic swap
    duckdbcli/     # Exporter over the duckdb CLI subprocess (os/exec)
  testing/
    fakes/         # In-memory fakes for every port
    invariants/    # Structural checks + drift observations over a SessionDoc (contract tier's engine)
    logfixture/    # Verbatim vendor log fixtures (never hand-edit)
      scrub/       # Scrub engine: strips/replaces sensitive text in fixture JSONL
  tools/
    scrubfixture/  # CLI over logfixture/scrub, wired via `just scrub-fixture`
  version/
    version.go     # Version string (injectable via ldflags)
tests/
  e2e/             # Black-box tests against the compiled binary (build tag e2e),
                   # one file per AC-ID category, plus the untagged coverage_test.go
  docs/            # Untagged: mechanical drift check between the docs and the tree
  release/         # Untagged: release.yml read as data (ldflags path, CI binding, matrix,
                   # .releaserc.json, pnpm lock) + an integration-tagged build-and-run proof
docs/
  product/         # Product requirements (prd-mvp.md)
  technical/       # Technical design (tdd-mvp.md) — the nine core decisions live here
  adrs/            # Architecture Decision Records (currently empty — see "See also")
  architecture.md  # this document
  testing.md       # four-tier test pyramid, fakes conventions, fixture provenance
  acceptance.md    # observable contract; every AC-* ID maps to exactly one discharging test
  development.md   # dev setup and common tasks
```

`claudesource`, `jsonlstore`, and `duckdbcli` have landed and are wired into
`main.go`; `codexsource` is the one adapter this shape anticipates but does
not yet have.

## Layering

```
main.go (wiring; the only file that imports every concrete adapter)
  -> constructs adapters, injects them into use cases
  -> cli.NewRoot(deps)          (driving adapter: cobra commands, thin shims)
       |
       v
     internal/core/*            (use cases + domain services)
       |  (depends only on v)
       v
     internal/ports/*           (interfaces)
       ^
       |  (implemented by ^)
     internal/adapters/<rest>/  (driven adapters: claudesource, jsonlstore, duckdbcli)
       |
       v
     third-party deps + stdlib infra (cobra, afero, os/exec, ...)
       -- only adapters and wiring import these
```

`internal/core` never imports infrastructure (`os`, `net/http`, `os/exec`,
`os/signal`, `net`, `syscall`, `runtime/debug`, `plugin`, `embed`, or any
third-party SDK) — only the rest of the stdlib, `internal/ports`, and
itself. `internal/core/arch_test.go`'s `TestCorePurity` walks every non-test
`.go` file under `internal/core/` and asserts this; `TestForbidden` is a
table test of the classifier itself, guarding against `TestCorePurity`
passing vacuously if the classifier were ever broken.

**Documented gap:** `TestCorePurity` only checks *direct* imports of files
under `internal/core/`; taint through `internal/ports` is not caught — a
port implementation is free to import `os/exec`, and a core file that only
imports `internal/ports` would not see that transitively
(`arch_test.go`'s own doc comment, lines 24-30). `internal/ports` is three
files of pure interface declarations whose imports are trivially
reviewable by hand, so this is an accepted gap, not a hole to close.

## Ports

| Port | Purpose | Adapter | Fake |
|---|---|---|---|
| `ConversationSource` | enumerate session files for a vendor; parse one file into a normalized `SessionDoc` | `claudesource` (Codex: not yet implemented) | `fakes.FakeConversationSource` |
| `CanonicalStore` | the store's root path; begin a rebuild | `jsonlstore` (afero) | `fakes.FakeCanonicalStore` |
| `StoreRebuild` | accumulate one pending store generation: `Put`, `Commit`, `Discard` | `jsonlstore`'s rebuild type | `fakes.FakeStoreRebuild` |
| `Exporter` | materialize the canonical store into one sink | `duckdbcli` (`os/exec`) | `fakes.FakeExporter` |

A few things the table can't show:

- **`CanonicalStore` is two interfaces, not one.** `CanonicalStore` itself
  is small (`Root`, `BeginRebuild`); the interesting surface —
  `Put`/`Commit`/`Discard` — is `StoreRebuild`, the value `BeginRebuild`
  returns. This split exists because every `sync` is a full,
  build-then-swap rebuild (TDD decision 6): a caller cannot write into the
  live store directly, only into a pending generation it must explicitly
  commit or discard.
- **`ValidSessionDoc` lives in `internal/ports`, not in an adapter.**
  `ports.ValidSessionDoc(sess model.Session) bool` is the single predicate
  every conforming `StoreRebuild.Put` must reject against
  (`ErrInvalidSessionDoc`), and the one `internal/core/sync` must pre-filter
  with before calling `Put` at all. `canonicalstore.go:24-40`'s doc comment
  records the drift this prevented: a narrower, hand-rolled "empty
  `SessionID`" check previously used at one call site missed the strictly
  larger rejection set (non-namespaced id, bare-prefix id, traversal
  vendor) the real store actually enforces — exporting the predicate from
  `internal/ports` is what keeps `jsonlstore.relPath`,
  `fakes.FakeStoreRebuild.Put`, and `internal/core/sync`'s pre-filter from
  reimplementing (and drifting from) three different versions of the same
  check.
- **There is no env port.** Unlike `fetch-context`'s `envx` adapter-layer
  helper, wiring here reads `os.Getenv` directly in `main.go` — a
  deliberate contrast, not an oversight: this tool reads exactly three env
  vars, all in one file, so a typed indirection layer would add a package
  for no callers beyond that one file.

## Use Cases

| Use case | Package | Subcommand | Ports consumed |
|---|---|---|---|
| `Sync` | `internal/core/sync` | `sync` | `ConversationSource`, `CanonicalStore` |
| `Export` | `internal/core/export` | `export duckdb` | `CanonicalStore`, `Exporter` |

**`Sync.Run`'s severity is a three-way split, not two**, not merely
lenient-vs-fatal. See `sync.go`'s doc comment and
`docs/technical/tdd-mvp.md`'s "Severity is a three-way split, not two"
bullet (under core decision 8) for the three cases and why each is
lenient or fatal.

**`Export.Run` is deliberately thin**: it resolves the sink by name and
guards only the two inputs no `Exporter` can proceed without, delegating
everything else to the sink adapter. See `export.go:61-67`'s doc comment
for the full rationale.

**`Sync.Vendors()`** returns the sorted set of vendors this build actually
has a registered source for — nil-receiver-safe, so a `Deps{}` built
without a `Sync` in a CLI-tree test never needs a nil check. The CLI's bare
`sync` fans out over `Vendors()` rather than a hardcoded vendor list, so a
build with only `claudesource` wired syncs Claude only and exits 0; `sync
--vendor codex` fails loudly, naming what is available
(`internal/adapters/cli/sync.go`'s `selectedVendors`). This is the
deferred-Codex rule: once a `codexsource` adapter is registered in
`main.go`, this rule needs no change — `Vendors()` picks it up
automatically.

## Unified data model

`internal/core/model` (`model.go`) declares `SessionDoc`/`Session`/
`Message`/`ToolCall` and imports nothing from this module — a pure leaf
package, so both `internal/core` and `internal/ports` can speak it with no
import cycle. Its JSON tags are load-bearing: they are exactly the
canonical store's on-disk format and the column names the DuckDB export's
generated SQL reads back out via `read_json`. Full schema, the
vendor-to-model field mappings, and the `TIMESTAMPTZ` rationale live in
`docs/technical/tdd-mvp.md`.

## Adapters

| Adapter | Port | External dependency | Internal split |
|---|---|---|---|
| `cli` | — (driving) | `github.com/spf13/cobra` | One file per subcommand: `root.go`, `sync.go`, `export.go`, `export_duckdb.go`, `version.go` |
| `claudesource` | `ConversationSource` | none (pure parsing) | `classify.go` (skip taxonomy), `normalize.go`, `record.go`, `sidechain.go` |
| `jsonlstore` | `CanonicalStore`, `StoreRebuild` | `github.com/spf13/afero` | `naming.go` (id-to-path escaping), `rebuild.go` (staging + two-rename swap) |
| `duckdbcli` | `Exporter` | `os/exec` (the `duckdb` CLI binary) | `script.go` (pure SQL generation), `duckdbcli.go` (subprocess + temp-dir-then-rename) |

**Afero is only behind `CanonicalStore`.** `jsonlstore` is the one adapter
that uses `afero.Fs` (injectable, so its own tests can run against an
in-memory filesystem) — including for its staging directory, created via
`afero.TempDir(s.fs, s.root, stagingPrefix)` (`jsonlstore.go:125`), not
`os.MkdirTemp`. Its only use of the `os` package is two `os.FileMode`
constants, `dirMode`/`fileMode` (`jsonlstore.go:71-72`). `duckdbcli` and
`claudesource` use `os`/`os/exec` directly — this table should not read as
"all filesystem access in this repo goes through afero."

## Wiring

`cmd/agent-logs-extractor/main.go` is the only file that imports every
concrete adapter. In order:

1. Resolve the log level: start from `AGENT_LOGS_EXTRACTOR_LOG_LEVEL` (an
   invalid value warns and falls back to `info`), so one `*slog.LevelVar`
   backs the one logger every use case logs through; `--log-level`, if
   passed, later overwrites the same `LevelVar` in `PersistentPreRunE`.
2. Resolve default paths (`defaultPaths()`) — pure path arithmetic, no I/O.
3. Construct the concrete adapters: `claudesource.New(log)`,
   `jsonlstore.New(fsys, paths.storeRoot, log)`, `duckdbcli.New(log)`.
4. Construct the use cases (`sync.New`, `export.New`), injecting the ports
   they need.
5. Construct the cobra root (`cli.NewRoot(deps)`) and execute it under a
   context from `signal.NotifyContext(..., os.Interrupt, syscall.SIGTERM)`
   — not just `signal.Notify` — so a Ctrl-C during a sync unwinds through
   `Sync.Run`'s deferred `r.Discard()` instead of killing the process
   mid-`Put` and leaving a `.staging-*` directory for the next
   `BeginRebuild`'s sweep to clean up. `os/signal` is infrastructure, so
   this lives here, never in `internal/core`.

**Path precedence for the data directory** (store, export) is settled by
`main.go`'s `defaultPaths()` — see `README.md`'s Sandboxing section, or
`CLAUDE.md`'s "No config file" bullet, for the three-step precedence rule
rather than a fourth copy of it here.

## Library choices

| Library | Used by | Notes |
|---|---|---|
| Cobra (`v1.10.0`) | `adapters/cli` | Driving adapter; each subcommand is a thin shim: parse flags, call use case, format output |
| afero (`v1.15.0`) | `adapters/jsonlstore` | Filesystem abstraction behind `CanonicalStore`, injectable for unit tests; the core never imports it |

Standard-library choices worth naming:

| Stdlib package | Used by | Notes |
|---|---|---|
| `log/slog` | wiring + every use case/adapter | Structured logging; use cases take a `*slog.Logger` as an explicit dependency, not a global |
| `os/exec` | `adapters/duckdbcli` | Shells out to the `duckdb` CLI; wrapped in the adapter, the core never sees it |
| `os/signal` | `cmd/agent-logs-extractor/main.go` | `signal.NotifyContext`, wiring only |

Go itself is pinned at `1.25.0` (`go.mod`); CI additionally pins `duckdb
1.4.0` (`.github/workflows/ci.yml`'s `DUCKDB_VERSION`,
`Dockerfile.duckdb`'s `ARG DUCKDB_VERSION`) and `just 1.21.0`
(`Dockerfile.duckdb`'s `ARG JUST_VERSION`) — `duckdbcli.MinVersion`
(`"1.4"`) documents the same floor in error messages, but is not a probed
or tested contract across a version range (see `docs/technical/tdd-mvp.md`
core decision 4's elaboration under "Settled by `internal/adapters/duckdbcli`").

The release pipeline pins its own toolchain, entirely separately from the
Go build: **Node 24.x** and **pnpm 11.1.2**
(`.github/workflows/release.yml`'s `node-version`, `package.json`'s
`packageManager` field, which `corepack` reads), plus exact — not
caret-ranged — versions for **semantic-release 24.2.3** and each of its six
plugins (`package.json`'s `devDependencies`, resolved transitively by the
committed `pnpm-lock.yaml`, which CI installs with `--frozen-lockfile`).
None of this is a dependency of the Go module: `go.mod` stays at Cobra +
afero, and `tests/release/` deliberately parses `release.yml` and
`pnpm-lock.yaml` with the standard library rather than buying a YAML
library for a test's convenience. `release.yml`'s Go pin is additionally
asserted equal to `ci.yml`'s by `TestReleaseWorkflowGoVersionMatchesCI`, so
released binaries are always built by the toolchain the gate ran.

## The nine core decisions, and where they live

`docs/technical/tdd-mvp.md`'s "Core decisions" section is the load-bearing
list; this table makes each one individually falsifiable against the tree
rather than restated here.

| # | Decision | Embodied in | Proven by |
|---|---|---|---|
| 1 | Hexagonal architecture, strictly | `internal/core/`, `internal/ports/`, `internal/adapters/`, `main.go` | `internal/core/arch_test.go` (`TestCorePurity`) |
| 2 | Fakes over mocks | `internal/testing/fakes/` | `fakes_test.go`; every `internal/core/*` unit test |
| 3 | Canonical store is JSONL, not a database | `internal/adapters/jsonlstore/` | `jsonlstore_test.go`, `jsonlstore_integration_test.go` |
| 4 | DuckDB invoked as a subprocess behind a port, not CGO | `internal/adapters/duckdbcli/` (`os/exec`) | `duckdbcli_test.go`, `duckdbcli_integration_test.go` |
| 5 | No in-tool query surface; denormalized export | `README.md`'s cookbook; `script.go`'s `scriptTail` joins | `TestCookbookQueriesMatchTheREADME`, `TestCookbookQueries`, AC-QUERY-05 |
| 6 | `sync` is a full, atomic rebuild | `internal/core/sync/sync.go`; `jsonlstore/rebuild.go`'s staging + two-rename swap | AC-SYNC-03 (idempotent, no leftovers) |
| 7 | Lenient, lossless parsing | `model.SkipReason`; `claudesource/classify.go`; `sync.go`'s three-way severity split | AC-SKIP-01/02/03 |
| 8 | Fixture-driven adapters, contract-tested against live logs | `internal/testing/logfixture/`; `claudesource_contract_test.go`; `internal/testing/invariants/` | golden tests in `claudesource`; `invariants_test.go` |
| 9 | Local-only, sources read-only | no network client anywhere in the core or adapters; vendor dirs never written | AC-SCOPE-01/02/03 |

## Conventions

This repo's coding conventions — core purity, one port per dependency, one
file per subcommand, wiring-only-in-`main.go`, and the rest — live in
`CLAUDE.md`'s "Key Conventions" section, not duplicated here.

## See also

- `docs/testing.md` — the four-tier test pyramid, fakes conventions,
  fixture provenance, and the AC-ID <-> test mapping this architecture
  makes cheap to hold.
- `docs/acceptance.md` — the observable contract every e2e test verifies.
- `docs/technical/tdd-mvp.md` — the nine core decisions in full, the
  vendor format mappings, and the still-open questions.
- `docs/adrs/` is currently **empty**: no standalone ADRs have been
  written. The TDD's nine core decisions above are the de-facto decision
  record for this project.
