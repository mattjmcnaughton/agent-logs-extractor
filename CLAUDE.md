# CLAUDE.md

Parse AI coding agent conversation logs (Claude Code, Codex) into a unified, queryable data model

Go CLI built with strict hexagonal (ports-and-adapters) architecture: Cobra
as the driving adapter, slog for logging, and the Go toolchain (gofmt, go
vet, go test).

## Quick Reference

| Command | Purpose |
| ------- | ------- |
| `just fmt` | Check formatting |
| `just fmt-fix` | Fix formatting |
| `just vet` | Run go vet |
| `just test` | Run unit tests |
| `just test-integration` | Run integration tests |
| `just test-integration-container` | Run integration tests in `Dockerfile.duckdb`, network-isolated |
| `just test-contract` | Run the opt-in contract tier against a live `~/.claude` |
| `just test-e2e` | Run the opt-in black-box e2e suite |
| `just test-e2e-container` | Run the e2e suite in `Dockerfile.duckdb`, network-isolated |
| `just test-all` | Run all tests |
| `just build` | Build binary to bin/ |
| `just run [args]` | Run via go run |
| `just tidy` | Tidy dependencies |
| `just scrub-fixture` | Scrub a real vendor session tree into a committable fixture |
| `just gate` | Fast pre-push check (fmt + vet + test) |
| `just gate-expensive` | Full check (gate + integration) |

## Project Structure

```
cmd/agent-logs-extractor/
  main.go          # Wiring: env → adapters → use cases → cobra root
internal/
  core/
    model/         # Unified data model (SessionDoc/Session/Message/ToolCall)
    sync/          # Sync use case
    export/        # Export use case
  ports/           # Interfaces the core depends on
  adapters/
    cli/           # Cobra subcommands, one file each (thin shims)
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
  e2e/             # Black-box tests against the compiled binary (//go:build e2e)
docs/
  adrs/            # Architecture Decision Records
  architecture.md  # System architecture overview
  acceptance.md    # Observable contract; every AC-* ID maps 1:1 to an e2e test
  development.md   # Dev setup and common tasks
```

## Key Conventions

- **`internal/core` imports no infrastructure.** No `os`, `net/http`,
  `os/exec`, or third-party SDKs — only the rest of the stdlib,
  `internal/ports`, and `internal/core` itself. Mechanically enforced by
  `internal/core/arch_test.go`.
- **One port per external dependency.** A new external dependency gets a
  new interface in `internal/ports/`; adapters implement it.
- **One file per cobra subcommand** under `internal/adapters/cli/`. Each is
  a thin shim: parse args, call a use case, emit output.
- **Wiring only in `main.go`.** It is the only file that imports every
  concrete adapter.
- **No config file.** Behavior is controlled by flags, plus
  `AGENT_LOGS_EXTRACTOR_HOME` / `AGENT_LOGS_EXTRACTOR_LOG_LEVEL` read in
  wiring.
- **Fakes over mocks.** Hand-written in-memory fakes live in
  `internal/testing/fakes/`; tests assert on outcomes, never on call
  sequences.
- **Fixtures are verbatim ground truth.** `internal/testing/logfixture/`
  holds real (scrubbed) vendor log files — never hand-edit or move them.
- **Integration tests** use the `//go:build integration` build tag.
  `internal/adapters/duckdbcli`'s integration tests additionally require a
  real `duckdb` CLI on `PATH` and skip (not fail) when it's absent; CI runs
  them with `ALX_REQUIRE_DUCKDB=1` (making a missing binary a hard failure
  there) in both a native job and a containerized job — see
  `docs/architecture.md`.
- **Version** is defined as `"dev"` by default and overridden at build time
  with `-ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=x.y.z"`.
- **Four test tiers:** unit (no tag) / integration (`integration`) / contract
  (`contract`, opt-in, reads a live `~/.claude`) / e2e (`e2e`, black-box
  against the compiled binary). Every `AC-*` ID in `docs/acceptance.md` maps
  1:1 to exactly one e2e test, enforced by the untagged `TestACCoverage`
  (`tests/e2e/coverage_test.go`). Contract and e2e never run as part of
  `just gate`/`just gate-expensive` — see `docs/development.md`.

## More Information

- `docs/architecture.md` — read before adding new modules or changing project structure
- `docs/acceptance.md` — observable contract; read before changing user-visible behavior
- `docs/development.md` — read for environment setup, debugging, or common tasks
