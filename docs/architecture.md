# Architecture

## Overview

agent-logs-extractor is a Go CLI built with Cobra and Viper, using stdlib slog for structured logging.

## Project Structure

```
cmd/agent-logs-extractor/
  main.go               # Entrypoint
internal/
  cli/                  # Cobra command definitions (thin I/O layer)
  config/               # Viper-backed configuration
  version/              # Version string
```

## Layering

```
main.go
  -> cli.NewRoot()          (Cobra root, Viper setup, slog init)
    -> subcommands          (parse args, call business logic, emit output)
      -> services/          (business logic — add as needed)
```

## Toolchain

| Tool | Purpose |
| ---- | ------- |
| Go | Language and standard library |
| Cobra | CLI framework and subcommand routing |
| Viper | Environment variable configuration |
| slog | Structured logging (stdlib) |
| gofmt | Formatting |
| go vet | Static analysis |
| go test | Testing |
| just | Task runner |

## Configuration

All configuration is loaded from environment variables via Viper.
Variables are prefixed with `AGENT_LOGS_EXTRACTOR_`.
Add fields to `internal/config/config.go` and call `config.Load()` from commands.

## Testing

- Unit tests: standard `go test ./...`
- Integration tests: tagged with `//go:build integration`, run via `just test-integration`
- No test framework required — use stdlib `testing` package

## Conventions

- Commands are thin I/O wrappers. Business logic lives in services.
- Errors are returned from `RunE`, not `Run`, so Cobra handles them cleanly.
- Version is injected at build time via ldflags; defaults to `"dev"`.
