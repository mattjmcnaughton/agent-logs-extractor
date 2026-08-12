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
just run example

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

1. Create `internal/cli/<name>.go` with a `newNameCmd()` function.
2. Register it in `internal/cli/root.go` via `root.AddCommand(newNameCmd())`.
3. Keep the command thin — parse args, call a function, emit output.
4. Add business logic to `internal/services/` as the project grows.
