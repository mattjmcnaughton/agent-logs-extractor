# Development

## Prerequisites

- Go 1.25.0+
- [just](https://just.systems/)

## Setup

```sh
# Install dependencies
go mod tidy

# Copy environment file
cp .env.example .env
```

## Common Tasks

```sh
# Format and fix
just fmt-fix

# Run vet
just vet

# Run tests
just test
just test-all

# Build binary
just build

# Run directly
just run version

# Full pre-push check
just gate

# Full check including integration tests
just gate-expensive
```

## Testing

Tests use the stdlib `testing` package, in four tiers
(`docs/technical/tdd-mvp.md`'s "Testing" section; `docs/acceptance.md` §1.5
for the acceptance-criteria mapping):

- **Unit** (no build tag) — `just test`. Lives alongside the code it tests
  (e.g. `internal/adapters/cli/sync_test.go`), plus
  `internal/testing/invariants/invariants_test.go` (the contract tier's
  engine, table-driven and provable without a live `~/.claude`) and
  `tests/e2e/coverage_test.go` (`TestACCoverage`, deliberately **untagged**
  so it gates: it only parses `docs/acceptance.md` and the Go source under
  `tests/e2e/`, no subprocess, no binary, no duckdb).
- **Integration** (`//go:build integration`) — `just test-integration`.
  `internal/adapters/duckdbcli`'s integration tests additionally need a real
  `duckdb` CLI on `PATH`; they skip (not fail) when it's absent, so a plain
  `just test-integration`/`just gate-expensive` run without duckdb installed
  reports those tests as skipped rather than passing or failing. Set
  `ALX_REQUIRE_DUCKDB=1` to turn that skip into a hard failure (what CI's
  native `gate-expensive` job does). `just test-integration-container` runs
  the same tier inside a container with duckdb baked in (`Dockerfile.duckdb`),
  for when you don't want to install duckdb locally at all.
- **Contract** (`//go:build contract`, opt-in) — `just test-contract`. See
  "Contract tier" below.
- **E2E** (`//go:build e2e`, opt-in) — `just test-e2e`. See "E2E tier" below.

Contract and e2e are never part of `just gate` or `just gate-expensive` —
see the comment above `gate` in the `justfile` for why, and
`docs/acceptance.md` §12 (R4).

### Contract tier

`internal/adapters/claudesource/claudesource_contract_test.go` parses
whatever `~/.claude` tree `ALX_CONTRACT_CLAUDE_PATH` points at and asserts
the structural invariants every `SessionDoc` must satisfy
(`internal/testing/invariants.Check`), then reports drift observations
(`invariants.Observe`) via `t.Log`: unknown record types, malformed lines,
dangling links, a session's `cwd` changing mid-conversation
(`cwd_drift` — instruments `docs/technical/tdd-mvp.md`'s open question 2),
and more. It is **strictly read-only**: only `Source.List`/`Source.Parse`
are ever called, never a `CanonicalStore`. It also fails outright (not just
observes) if the resolved root yields session files but zero sessions, or
sessions but zero messages — total ingestion loss, e.g. from a vendor
renaming a field this tool depends on, is a hard failure, never a silent
zero-row pass.

`resolveContractRoot` **requires `ALX_CONTRACT_CLAUDE_PATH` to be set at
all** — with it unset, the test SKIPs immediately rather than falling back
to `AGENT_LOGS_EXTRACTOR_HOME`/`$HOME` or any other live `~/.claude`. This
is deliberate and mechanical: CI never sets `ALX_CONTRACT_CLAUDE_PATH`, and
a contributor's own sandbox transcript (or any other live history) must
never be read by accident just because the env var was forgotten.

`ALX_CONTRACT_CLAUDE_PATH` (**test-only** — never read by the CLI or by
wiring, never document it in `README.md`, never expose it as a flag) is
therefore the *only* way to run this tier at all. Point it at an empty
directory to exercise the skip path, or at a fixture tree (or your own
real `~/.claude`) to exercise the found-logs path:

```sh
ALX_CONTRACT_CLAUDE_PATH=$(mktemp -d) just test-contract                             # skip path
ALX_CONTRACT_CLAUDE_PATH=$PWD/internal/testing/logfixture/claude just test-contract   # found-logs path
```

**Never point `ALX_CONTRACT_CLAUDE_PATH` at another session's or another
person's real Claude Code history without their consent** — this tier only
reads, but a transcript is still someone's private conversation log.

### E2E tier

`tests/e2e/` runs the compiled binary as a subprocess and asserts on its
observable behavior — black-box: no `internal/core` or `internal/adapters`
import is permitted there, only `internal/testing/logfixture`. Every test is
named `TestAC_<CATEGORY>_<NN>_<ShortName>`, mapped 1:1 to a criterion in
`docs/acceptance.md`. Set `$ALXBIN` to reuse a pre-built binary; otherwise
`TestMain` builds one itself.

A criterion marked **†** in `docs/acceptance.md` needs a real `duckdb`
binary; those tests skip (not fail) without one, same as the integration
tier's duckdb-dependent tests. To make every `†` criterion actually run:

```sh
# with duckdb installed locally
just test-e2e

# or, no local duckdb install needed:
just test-e2e-container
```

`just test-e2e-container` reuses `Dockerfile.duckdb` (no second image),
overriding its `CMD` at run time to run `just test-e2e` instead, network-
isolated (`--network none`) — this is also `docs/acceptance.md`'s
AC-SCOPE-03 (nothing reaches the network at run time).

## Building with a Version

```sh
go build -ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=1.0.0" \
  -o bin/agent-logs-extractor ./cmd/agent-logs-extractor
```

## Adding a New Command

1. Add a use case under `internal/core/` (a new package with its own
   `Request`/`Run`, following `internal/core/sync/` or
   `internal/core/export/`).
2. If it needs a new external dependency, add a port in `internal/ports/`
   and an adapter under `internal/adapters/` implementing it.
3. Add a thin shim file under `internal/adapters/cli/<name>.go` with a
   `newNameCmd(deps Deps)` function, and register it in
   `internal/adapters/cli/root.go` via `root.AddCommand(newNameCmd(deps))`.
4. Wire the new use case and any new adapters into `cli.Deps` in
   `cmd/agent-logs-extractor/main.go`.
