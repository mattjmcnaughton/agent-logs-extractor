# Check formatting (exits 1 if any files need formatting).
#
# Scoped to the three directories that hold Go source rather than `.`: a
# bare `gofmt -l .` also walks node_modules/ (the release pipeline's
# semantic-release install — thousands of files, none of them ours). The
# scoping is lossless: no .go file in this repo lives outside cmd/,
# internal/, and tests/.
fmt:
    @if [ -n "$(gofmt -l ./cmd ./internal ./tests)" ]; then gofmt -l ./cmd ./internal ./tests; exit 1; fi

# Fix formatting (same three directories as `fmt`, same reason)
fmt-fix:
    gofmt -w ./cmd ./internal ./tests

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

# Run the opt-in contract tier: asserts structural invariants plus
# vendor-format-drift observations
# (internal/adapters/claudesource/claudesource_contract_test.go) against
# whatever ~/.claude ALX_CONTRACT_CLAUDE_PATH (test-only, see
# docs/development.md) points at. Never gated (see the note above `gate`
# below) — it needs real session history to say anything, which CI has none
# of, and a contributor with no Claude Code history of their own must not
# see this fail. -v -count=1 so a skip's reason (or a real run's t.Log
# report) is never hidden by Go's test cache.
#
# DO NOT run this recipe bare: with no ALX_CONTRACT_CLAUDE_PATH set, the
# test now SKIPs outright rather than defaulting to this sandbox's own
# ~/.claude (its harness transcript, not a developer's history, and not
# ours to read). Always set it explicitly:
#   ALX_CONTRACT_CLAUDE_PATH=$(mktemp -d) just test-contract                             # skip path
#   ALX_CONTRACT_CLAUDE_PATH=$PWD/internal/testing/logfixture/claude just test-contract   # found-logs path
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

# Ask semantic-release what it WOULD do, without doing any of it. Proves
# that .releaserc.json parses, that every plugin in it resolves and loads
# from the installed node_modules, and what next version the conventional
# commits on this branch compute to.
#
# It does NOT execute @semantic-release/exec: --dry-run skips the publish
# step, which is the only lifecycle step exec is wired into. The string
# half of that handoff is pinned statically instead, by
# TestReleaseOutputHandoffIsWired — publishCmd's two $GITHUB_OUTPUT names,
# release.yml's `outputs:` block, build-binaries' `if:`, and the -ldflags
# version expression all have to agree on `new-release-version` /
# `new-release-published`, and a rename on any one side is red in
# `just gate`. What remains unproven until the first real release on main
# is only the run-time half: that the publish step fires at all and that
# the redirect really lands in $GITHUB_OUTPUT.
#
# --branches is load-bearing: semantic-release refuses to compute anything
# from a branch that is not in its configured `branches` list (["main"]),
# so running this from a feature branch without overriding it just prints
# "this test run was triggered on the branch <x>, while semantic-release is
# configured to only publish from main" and exits.
#
# Deliberately NOT part of `gate`/`gate-expensive`: it needs `pnpm install`
# to have run, network access, and a $GITHUB_TOKEN — none of which the gate
# promises. Run it by name when touching the release config.
release-dry-run:
    pnpm exec semantic-release --dry-run --no-ci --branches "$(git branch --show-current)"

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
