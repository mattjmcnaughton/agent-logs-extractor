# CLAUDE.md

Parse AI coding agent conversation logs (Claude Code, Codex) into a unified, queryable data model

Go CLI using Cobra, Viper, slog, and Go toolchain (gofmt, go vet, go test).

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
  main.go               # Entrypoint — calls cli.NewRoot().Execute()
internal/
  cli/
    root.go             # Cobra root command, Viper setup, slog init
    example.go          # Example subcommand (replace with real commands)
  config/
    config.go           # Config struct, loaded from env vars via Viper
  version/
    version.go          # Version string (injectable via ldflags)
docs/
  adrs/                 # Architecture Decision Records
  architecture.md       # System architecture overview
  development.md        # Dev setup and common tasks
```

## Key Conventions

- **Commands** live in `internal/cli/`. Each command is a thin I/O wrapper:
  parse args, call business logic, emit output.
- **Business logic** should move to `internal/services/` as the project grows.
- **Configuration** is loaded from environment variables via Viper.
  Variables are prefixed with `AGENT_LOGS_EXTRACTOR_`
  (e.g. `AGENT_LOGS_EXTRACTOR_LOG_LEVEL=debug`).
- **Integration tests** use the `//go:build integration` build tag.
- **Version** is defined as `"dev"` by default and overridden at build time
  with `-ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=x.y.z"`.

## More Information

- `docs/architecture.md` — read before adding new modules or changing project structure
- `docs/development.md` — read for environment setup, debugging, or common tasks
