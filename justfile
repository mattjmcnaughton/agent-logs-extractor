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

# Fast pre-push check
gate: fmt vet test

# Full check
gate-expensive: gate test-integration
