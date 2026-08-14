# Check formatting (exits 1 if any files need formatting)
fmt:
    @if [ -n "$(gofmt -l .)" ]; then gofmt -l .; exit 1; fi

# Fix formatting
fmt-fix:
    gofmt -w .

# Run go vet
vet:
    go vet ./...

# Run unit tests
test:
    go test ./...

# Run integration tests
test-integration:
    go test -tags=integration ./...

# Run all tests
test-all: test test-integration

# Build the binary
build:
    mkdir -p bin
    go build -o bin/agent-logs-extractor ./cmd/agent-logs-extractor

# Run the CLI
run *args:
    go run ./cmd/agent-logs-extractor {{args}}

# Tidy dependencies
tidy:
    go mod tidy

# Scrub a real vendor session tree into a committable fixture
scrub-fixture *args:
    go run ./internal/tools/scrubfixture {{args}}

# Build the containerized duckdb test image and run the integration tier
# inside it, network-isolated at run time (Dockerfile.duckdb). Requires a
# working docker daemon; not part of `gate`/`gate-expensive` because it
# needs docker, not just Go — CI's "Gate (expensive, containerized duckdb)"
# job runs the same two commands directly. No --build-arg DUCKDB_VERSION
# (Dockerfile.duckdb's ARG already defaults to the same pinned version) and
# no -e ALX_REQUIRE_DUCKDB=1 (the image's own ENV already sets it).
test-integration-container:
    docker build -f Dockerfile.duckdb -t agent-logs-extractor-duckdb-test .
    docker run --rm --network none agent-logs-extractor-duckdb-test

# Run the opt-in contract tier: parses the developer's *live* ~/.claude
# (AGENT_LOGS_EXTRACTOR_HOME, else $HOME) and asserts structural invariants
# plus vendor-format-drift observations
# (internal/adapters/claudesource/claudesource_contract_test.go). Never
# gated (see the note above `gate` below) — it needs real session history
# to say anything, which CI has none of, and a contributor with no Claude
# Code history of their own must not see this fail. -v -count=1 so a
# skip's reason (or a real run's t.Log report) is never hidden by Go's test
# cache. ALX_CONTRACT_CLAUDE_PATH (test-only, see docs/development.md) can
# redirect this at a fixture tree instead of a real ~/.claude.
test-contract:
    go test -tags=contract -v -count=1 ./...

# Run the black-box e2e suite (tests/e2e/, //go:build e2e) against a
# locally-built binary. $ALXBIN, if set, names a pre-built binary to reuse;
# otherwise TestMain builds one itself. -v -count=1 for the same reason as
# test-contract above: a skipped duckdb-dependent (†) criterion should
# always show its skip reason, never hide behind the test cache.
test-e2e:
    go test -tags=e2e -v -count=1 ./tests/e2e/...

# Run the e2e suite inside Dockerfile.duckdb (reused, not a second image —
# see docs/technical/tdd-mvp.md), network-isolated at run time: this is
# AC-SCOPE-03's own proof, and — since the image already sets
# ALX_REQUIRE_DUCKDB=1 as an ENV — every duckdb-dependent (†) criterion
# hard-fails here instead of skipping. Same shape as
# test-integration-container above: no --build-arg, no -e, and the image's
# own CMD is overridden at run time rather than baked in, so one image
# serves both integration- and e2e-container recipes.
test-e2e-container:
    docker build -f Dockerfile.duckdb -t agent-logs-extractor-duckdb-test .
    docker run --rm --network none agent-logs-extractor-duckdb-test just test-e2e

# `gate`/`gate-expensive` deliberately do NOT run test-contract or test-e2e
# (do not "helpfully" add them here): test-contract needs a developer's
# real vendor history to mean anything, and test-e2e needs a locally-built
# binary and (for its † criteria) a real duckdb — costs `gate`'s "fast"
# and `gate-expensive`'s existing "needs only Go + a pinned duckdb CLI"
# promises were never meant to carry. Both stay opt-in, run by name.
# Fast pre-push check
gate: fmt vet test

# Full check
gate-expensive: gate test-integration
