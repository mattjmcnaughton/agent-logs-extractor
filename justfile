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

# Fast pre-push check
gate: fmt vet test

# Full check
gate-expensive: gate test-integration
