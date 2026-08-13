# Architecture

## Overview

agent-logs-extractor is a Go CLI built with strict hexagonal
(ports-and-adapters) architecture: Cobra as the driving adapter, stdlib slog
for structured logging. It follows the same shape as `fetch-context`.

## Project Structure

```
cmd/agent-logs-extractor/
  main.go          # Wiring: env → adapters → use cases → cobra root
internal/
  core/            # Pure use cases + domain model, zero infra imports
    model/         # Unified data model (SessionDoc/Session/Message/ToolCall)
    sync/          # Sync use case
    export/        # Export use case
  ports/           # Interfaces the core depends on
  adapters/
    cli/           # Cobra subcommands (thin shims), one file each
    claudesource/  # ConversationSource for ~/.claude/projects (Claude Code)
    jsonlstore/    # CanonicalStore over afero: JSONL store + atomic swap
  testing/
    fakes/         # In-memory fakes for every port
    logfixture/    # Verbatim vendor log fixtures
      scrub/       # Scrub engine: strips/replaces sensitive text in fixture JSONL
  tools/
    scrubfixture/  # CLI over logfixture/scrub, wired via `just scrub-fixture`
  version/         # Version string
```

Concrete adapters (source parsers, the canonical store, the DuckDB exporter)
land as their tickets close; the port and use-case shapes above are frozen
by this ticket for them to build against. `claudesource` (#6) and
`jsonlstore` (#7) have landed and are wired into `main.go` as of #8, which
also implements `internal/core/sync`; `codexsource` and `duckdbcli` remain
to come.

## Layering

```
main.go (wiring)
  -> constructs adapters, injects them into use cases
  -> cli.NewRoot(deps)      (Cobra root; each subcommand is a thin shim)
    -> internal/core/*      (use cases: parse args in, call use case, emit output)
      -> internal/ports/*   (interfaces the use cases depend on)
```

`internal/core` never imports infrastructure (`os`, `net/http`, `os/exec`,
third-party SDKs) — only the rest of the stdlib, `internal/ports`, and
itself. This is mechanically enforced by `internal/core/arch_test.go`.

## Toolchain

| Tool | Purpose |
| ---- | ------- |
| Go | Language and standard library |
| Cobra | CLI framework and subcommand routing |
| afero | Filesystem abstraction behind `CanonicalStore` (`jsonlstore`), injectable for unit tests |
| slog | Structured logging (stdlib) |
| gofmt | Formatting |
| go vet | Static analysis |
| go test | Testing |
| just | Task runner |

## Configuration

There is no config file. Behavior is controlled by flags, plus
`AGENT_LOGS_EXTRACTOR_HOME` / `AGENT_LOGS_EXTRACTOR_LOG_LEVEL`, both read
once in `main.go` (wiring).

## Testing

- Unit tests: standard `go test ./...`
- Integration tests: tagged with `//go:build integration`, run via `just test-integration`
- No test framework required — use stdlib `testing` package
- Fakes over mocks: hand-written in-memory fakes in `internal/testing/fakes/`

## Conventions

- Commands are thin I/O wrappers over use cases in `internal/core/`.
- Errors are returned from `RunE`, not `Run`, so Cobra handles them cleanly.
- One port per external dependency; wiring only in `main.go`.
- Version is injected at build time via ldflags; defaults to `"dev"`.
