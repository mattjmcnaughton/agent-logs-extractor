# Development

## Prerequisites

- Go 1.25.0+
- [just](https://just.systems/)
- [duckdb](https://duckdb.org/docs/installation/) 1.4+ (optional) — only
  needed for `export duckdb` itself and for the `†`-marked integration/e2e
  tests; everything else runs without it (those tests skip, not fail).
- [Docker](https://www.docker.com/) (optional) — only needed for the
  `*-container` recipes (`test-integration-container`, `test-e2e-container`).

## Setup

```sh
# Install dependencies
go mod tidy
```

Nothing in this repo reads a `.env` file — there is no config file at all
(`AC-SCOPE-02`). Export the three env vars directly in your shell if you
need them; see `README.md`'s Sandboxing/Logging sections or `CLAUDE.md`'s
"No config file" bullet for what each one does.

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

Tests use the stdlib `testing` package, in four tiers. See
**`docs/testing.md`** for the full pyramid, build-tag conventions, fakes,
fixture provenance, and the AC-ID mapping — this section covers only *how
to invoke* the two opt-in tiers day to day.

### Contract tier

```sh
ALX_CONTRACT_CLAUDE_PATH=$(mktemp -d) just test-contract                             # skip path
ALX_CONTRACT_CLAUDE_PATH=$PWD/internal/testing/logfixture/claude just test-contract   # found-logs path
```

**Do not run `just test-contract` bare.** With `ALX_CONTRACT_CLAUDE_PATH`
unset the test SKIPs outright rather than defaulting to any live
`~/.claude` — always set it explicitly, one of the two ways above, or
pointed at your own real `~/.claude` if you have Claude Code history to
check it against. Never point it at another session's or another person's
real history without their consent (`docs/testing.md`'s Contract tier
section has the full rationale).

### E2E tier

```sh
# with duckdb installed locally
just test-e2e

# or, no local duckdb install needed:
just test-e2e-container
```

Set `$ALXBIN` to reuse a pre-built binary across a whole suite run;
otherwise `TestMain` builds one itself. See `docs/testing.md` for the `†`
marker and the container recipe.

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
   and an adapter under `internal/adapters/` implementing it — see "Adding
   a new port and adapter" below.
3. Add a thin shim file under `internal/adapters/cli/<name>.go` with a
   `newNameCmd(deps Deps)` function, and register it in
   `internal/adapters/cli/root.go` via `root.AddCommand(newNameCmd(deps))`.
4. Wire the new use case and any new adapters into `cli.Deps` in
   `cmd/agent-logs-extractor/main.go`.

## Adding a new port and adapter

1. Declare a small interface in a new `internal/ports/<name>.go` file — see
   `docs/architecture.md`'s Ports table for the shape existing ports take.
2. Add a fake implementing it in `internal/testing/fakes/fakes.go`
   (`FakeX` + `NewX(...)` constructor), following the existing fakes'
   "assert on observable state, never call sequences" convention.
3. Implement the real adapter under `internal/adapters/<name>/`, importing
   whatever infrastructure it needs — `internal/core` must never import it
   directly.
4. Wire the adapter into `cmd/agent-logs-extractor/main.go` and inject it
   into whichever use case(s) need it.
5. Update `docs/architecture.md`'s Ports and Adapters tables.

## Adding an acceptance criterion

1. Add the worked scenario to `docs/acceptance.md` (a `**AC-ID —
   ...**` heading with its Given/When/Then) and a matching row in §2's
   criteria index table, in the same change — `TestACCoverage`
   (`tests/e2e/coverage_test.go`) fails if only one exists.
2. For a `tier: e2e` row, add a `TestAC_<CATEGORY>_<NN>_<ShortName>`
   function under `tests/e2e/` — `TestACCoverage` fails until both the
   `docs/acceptance.md` row *and* the `TestAC_*` function exist, and the
   Test column must name the function it actually finds.
3. For `tier: unit`/`tier: integration`, point the Test column at an
   existing (or new) `TestXxx` function anywhere in the repo; for `tier:
   container`, point it at a `just <recipe>` that exists in the justfile.
4. Run `just test` — `TestACCoverage` is untagged and runs as part of it.
