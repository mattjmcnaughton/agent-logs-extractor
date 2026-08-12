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
| `just test-all` | Run all tests |
| `just build` | Build binary to bin/ |
| `just run [args]` | Run via go run |
| `just tidy` | Tidy dependencies |
| `just gate` | Fast pre-push check (fmt + vet + test) |
| `just gate-expensive` | Full check (gate + integration) |

## Project Structure

```
cmd/agent-logs-extractor/
  main.go               # Wiring: env → adapters → use cases → cobra root
internal/
  core/
    model/               # Unified data model (SessionDoc/Session/Message/ToolCall)
    sync/                 # Sync use case
    export/                # Export use case
  ports/                 # Interfaces the core depends on
  adapters/
    cli/                  # Cobra subcommands, one file each (thin shims)
  testing/
    fakes/                # In-memory fakes for every port
    logfixture/            # Verbatim vendor log fixtures (never hand-edit)
  version/
    version.go            # Version string (injectable via ldflags)
docs/
  adrs/                  # Architecture Decision Records
  architecture.md        # System architecture overview
  development.md         # Dev setup and common tasks
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
- **Version** is defined as `"dev"` by default and overridden at build time
  with `-ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=x.y.z"`.

## More Information

- `docs/architecture.md` — read before adding new modules or changing project structure
- `docs/development.md` — read for environment setup, debugging, or common tasks
