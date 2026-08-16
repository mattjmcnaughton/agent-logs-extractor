# CLAUDE.md

Parse AI coding agent conversation logs (Claude Code today; Codex not yet implemented — see below) into a unified, queryable data model

Go CLI built with strict hexagonal (ports-and-adapters) architecture: Cobra
as the driving adapter, slog for logging, and the Go toolchain (gofmt, go
vet, go test).

## Quick Reference

| Command | Purpose |
| ------- | ------- |
| `just fmt` / `just fmt-fix` | Check / fix formatting |
| `just vet` | Run go vet |
| `just test` | Unit tests (no build tag) |
| `just test-integration` | Integration tests (`//go:build integration`) |
| `just test-integration-container` | Integration tests inside `Dockerfile.duckdb`, network-isolated |
| `just test-all` | Unit + integration |
| `just test-contract` | Contract tests (`//go:build contract`); **requires `ALX_CONTRACT_CLAUDE_PATH`** — bare invocation SKIPs by design, never defaults to a real `~/.claude`. Opt-in, never gated. |
| `just test-e2e` | Black-box e2e suite (`//go:build e2e`) against the compiled binary |
| `just test-e2e-container` | E2E suite inside `Dockerfile.duckdb`, network-isolated |
| `just build` | Build binary to `bin/` |
| `just run [args]` | Run via `go run` |
| `just tidy` | Tidy dependencies |
| `just scrub-fixture` | Scrub a real vendor session tree into a committable fixture |
| `just release-dry-run` | Ask semantic-release what it *would* release; needs `pnpm install`, network, a token. Opt-in, never gated. |
| `just gate` | Fast pre-push check: `fmt` + `vet` + `test` |
| `just gate-expensive` | Full check: `gate` + `test-integration` |

## Architecture in one paragraph

The **core** (`internal/core/`) contains pure use cases (`sync`, `export`)
and the unified data model (`model`), with zero infrastructure imports.
**Ports** (`internal/ports/`) are small interfaces the core depends on:
`ConversationSource`, `CanonicalStore`/`StoreRebuild`, `Exporter`.
**Adapters** (`internal/adapters/`) implement those ports — `claudesource`
(Claude Code log parsing), `jsonlstore` (the JSONL canonical store over
afero), `duckdbcli` (the DuckDB CLI subprocess), plus the driving `cli`
adapter (cobra). **Wiring** in `cmd/agent-logs-extractor/main.go` is the
only file that imports every concrete adapter; it reads the environment,
constructs adapters, injects them into use cases, and hands those to the
cobra root. The core never imports `os`, `net/http`, `os/exec`, or any
third-party SDK. **Read `docs/architecture.md` before adding a new module,
port, or adapter.**

## Project Structure (high level)

```
cmd/agent-logs-extractor/main.go   # Wiring: env -> adapters -> use cases -> cobra root
internal/
  core/                            # Pure: model, sync, export — zero infra imports
  ports/                           # Interfaces the core depends on
  adapters/                        # cli, claudesource, jsonlstore, duckdbcli
  testing/                         # fakes, invariants (contract tier's engine), logfixture
  tools/scrubfixture/              # CLI over logfixture/scrub
  version/
tests/e2e/                         # Black-box tests against the compiled binary (build tag e2e)
tests/docs/                        # Untagged: mechanical drift check between the docs and the tree
tests/release/                     # Untagged + integration: release.yml + .releaserc.json vs. the tree
docs/                              # product/, technical/, adrs/, architecture, testing, acceptance, development
```

Full layout, port table, use-case table, and the nine core decisions live
in `docs/architecture.md`.

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
  `AGENT_LOGS_EXTRACTOR_HOME` / `AGENT_LOGS_EXTRACTOR_LOG_LEVEL` /
  `XDG_DATA_HOME` read once in wiring. Precedence for the data directory:
  `AGENT_LOGS_EXTRACTOR_HOME` wins outright if set (XDG is not even
  consulted); else an absolute `XDG_DATA_HOME`; else `~/.local/share`.
  `~/.claude`/`~/.codex` always resolve from `AGENT_LOGS_EXTRACTOR_HOME` (or
  the real home directory), never from `XDG_DATA_HOME`.
- **Fakes over mocks.** Hand-written in-memory fakes live in
  `internal/testing/fakes/`; tests assert on outcomes, never on call
  sequences.
- **Fixtures are verbatim ground truth.** `internal/testing/logfixture/`
  holds real (scrubbed) vendor log files — never hand-edit or move them.
  Two documented exceptions live in `docs/testing.md`.
- **Codex is not implemented.** `--codex-path` is accepted and stored but
  never consulted; `sync --vendor codex` errors naming what is available.
- **Four test tiers:** unit (no tag) / integration (`integration`) /
  contract (`contract`, opt-in, requires `ALX_CONTRACT_CLAUDE_PATH`) / e2e
  (`e2e`, opt-in, black-box against the compiled binary). Every `AC-*` ID in
  `docs/acceptance.md` maps 1:1 to exactly one **discharging test** — 31 at
  the e2e tier, the rest at unit/integration/container — enforced by the
  untagged `TestACCoverage` (`tests/e2e/coverage_test.go`). Contract and
  e2e never run as part of `just gate`/`just gate-expensive`.
- **Version** is defined as `"dev"` by default and overridden at build time
  with `-ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=x.y.z"`.
  `.github/workflows/release.yml` passes exactly that flag when building
  release binaries. A `-X` path is only a string to the linker — spell it
  wrong and the link still succeeds while the binary reports `dev` — so
  `tests/release/` guards it from both ends: statically against `go.mod`
  and the source tree (`TestReleaseWorkflowLdflagsPathMatchesTree`,
  untagged, runs in `just gate`), and end-to-end by building with the
  workflow's own flag string and running `version`
  (`TestReleaseLdflagsInjectVersion`, integration tier, AC-RELEASE-01).
- **Releases are cut by semantic-release from conventional commits.**
  Merging a `feat:`/`fix:` to `main` bumps the version, writes
  `CHANGELOG.md`, tags, and attaches four binaries (linux/macOS ×
  x86_64/arm64). **Publishing is currently switched off**:
  `.releaserc.json` sets `"dryRun": true`, so each push to `main` rehearses
  the whole pipeline and logs what it would release, creating no tag,
  release, changelog, commit, or binaries. Flip that one line to `false` to
  go live; `TestReleaseDryRunIsExplicit` keeps the key present and a real
  JSON boolean without pinning its value. Config lives in
  `.releaserc.json` + `package.json` +
  `pnpm-lock.yaml`; the pipeline is `.github/workflows/release.yml`,
  triggered by `workflow_run` on the **CI** workflow, so a red CI blocks a
  release. Never run bare `pnpm install` (it rewrites the lockfile) — only
  `pnpm install --frozen-lockfile`. See `docs/development.md`'s
  "Releasing".

## More Information

Progressive disclosure — pull in the relevant doc when the task touches it:

- `README.md` — user-facing command surface, file layout, sandboxing, query
  cookbook.
- `docs/architecture.md` — hexagonal layout, port/adapter tables, use-case
  wiring, the nine core decisions. **Read before adding a new module, port,
  or adapter.**
- `docs/testing.md` — four-tier pyramid, fakes conventions, fixture
  provenance, the AC-ID <-> test mapping. **Read before writing tests
  beyond a plain unit test.**
- `docs/acceptance.md` — observable contract; every `AC-*` ID maps 1:1 to
  exactly one discharging test. **Read before changing user-visible
  behavior.**
- `docs/development.md` — environment setup, debugging, common tasks, and
  the step-by-step recipes for adding a new command, a new port and
  adapter, or an acceptance criterion.
- `docs/technical/tdd-mvp.md` — the nine core decisions in full, vendor
  format mappings, still-open questions. **Read before revisiting a
  settled design decision.**
- `docs/adrs/` — Architecture Decision Records. Currently empty; the TDD's
  nine core decisions are the de-facto decision record.
