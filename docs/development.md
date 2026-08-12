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

Tests use the stdlib `testing` package:

- Unit tests live alongside the code they test (e.g. `internal/cli/example_test.go`)
- Integration tests use the `//go:build integration` build tag
- Run integration tests with `just test-integration`

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
