# Development

## Prerequisites

- Go 1.25.0+
- [just](https://just.systems/)
- [duckdb](https://duckdb.org/docs/installation/) 1.4+ (optional) — only
  needed for `export duckdb` itself and for the `†`-marked integration/e2e
  tests; everything else runs without it (those tests skip, not fail).
- [Docker](https://www.docker.com/) (optional) — only needed for the
  `*-container` recipes (`test-integration-container`, `test-e2e-container`).
- [Node.js](https://nodejs.org/) 24.x + pnpm via `corepack enable`
  (optional) — **release tooling only**. Nothing in the Go build, the test
  pyramid, or `just gate`/`gate-expensive` touches Node; you need it only
  to run `just release-dry-run` against the semantic-release config. The
  pinned pnpm version comes from `package.json`'s `packageManager` field,
  which `corepack` reads automatically.

> Install release dependencies with `pnpm install --frozen-lockfile`, never
> bare `pnpm install`. The bare form re-resolves transitive dependencies
> and rewrites `pnpm-lock.yaml`; the committed lockfile is the exact byte
> sequence CI installs from, and `--frozen-lockfile` is what CI runs.

## Setup

```sh
# Install dependencies
go mod tidy
```

Nothing in this repo reads a `.env` file — there is no config file at all
(`AC-SCOPE-02`). Export the three env vars directly in your shell if you
need them; see `README.md`'s Sandboxing/Logging sections or `CLAUDE.md`'s
"No config file" bullet for what each one does.

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

Tests use the stdlib `testing` package, in four tiers. See
**`docs/testing.md`** for the full pyramid, build-tag conventions, fakes,
fixture provenance, and the AC-ID mapping — this section covers only *how
to invoke* the two opt-in tiers day to day.

### Contract tier

```sh
ALX_CONTRACT_CLAUDE_PATH=$(mktemp -d) just test-contract                             # skip path
ALX_CONTRACT_CLAUDE_PATH=$PWD/internal/testing/logfixture/claude just test-contract   # found-logs path
```

**Do not run `just test-contract` bare.** With `ALX_CONTRACT_CLAUDE_PATH`
unset the test SKIPs outright rather than defaulting to any live
`~/.claude` — always set it explicitly, one of the two ways above, or
pointed at your own real `~/.claude` if you have Claude Code history to
check it against. Never point it at another session's or another person's
real history without their consent (`docs/testing.md`'s Contract tier
section has the full rationale).

### E2E tier

```sh
# with duckdb installed locally
just test-e2e

# or, no local duckdb install needed:
just test-e2e-container
```

Set `$ALXBIN` to reuse a pre-built binary across a whole suite run;
otherwise `TestMain` builds one itself. See `docs/testing.md` for the `†`
marker and the container recipe.

## Building with a Version

```sh
go build -ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=1.0.0" \
  -o bin/agent-logs-extractor ./cmd/agent-logs-extractor
```

This is the same flag `.github/workflows/release.yml` passes when it builds
release binaries — see "Releasing" below, and note that
`tests/release/` exists precisely to keep the two from drifting apart.

## Releasing

Releases are cut automatically by
[semantic-release](https://semantic-release.gitbook.io/) from conventional
commit messages. **You never bump a version by hand**, never create a tag
by hand, and never edit `CHANGELOG.md` by hand.

### Commit type → version bump

The bump is computed from the commits merged to `main` since the last tag:

| Commit | Bump | Example |
|---|---|---|
| `fix: ...` | patch | `1.2.3` → `1.2.4` |
| `feat: ...` | minor | `1.2.3` → `1.3.0` |
| any type with `BREAKING CHANGE:` in the body (or `feat!:`) | major | `1.2.3` → `2.0.0` |
| `docs:`, `test:`, `ci:`, `chore:`, `refactor:`, `style:`, `perf:` | none | no release |

A branch whose commits are all non-releasing types merges to `main` and
cuts nothing — which is the intended outcome for a docs-only or
test-only change, not a failure.

### The publish switch: `dryRun`

**The pipeline currently rehearses instead of publishing.**
`.releaserc.json` sets `"dryRun": true`, so on every push to `main`
semantic-release runs in full — verifying plugin conditions, analysing
commits, computing the next version, and rendering the release notes into
the job log — and then stops. It creates no tag, no GitHub Release, no
`CHANGELOG.md`, no commit on `main`, and no issue comments. Because the
`publishCmd` never runs, `new-release-published` is never written, so
`build-binaries` is skipped and no binaries are uploaded.

That makes a merge to `main` a live-fire rehearsal: everything up to the
point of publishing is exercised for real, and the job log states exactly
what *would* have been released.

**To go live**, change that one line to `"dryRun": false` (or delete it —
semantic-release defaults to publishing). The commit that does so is itself
a push to `main`, so the release fires on that merge; its own commit type
is irrelevant, because the analyser reads every commit since the last tag,
not just the newest one.

`TestReleaseDryRunIsExplicit` (untagged, runs in `just gate`) requires the
key to be present and to be a real JSON boolean. It deliberately does *not*
require a particular value — flipping it is the supported path. It exists
because the two silent failures here are costly in opposite directions:
deleting the key publishes when nobody meant to, and writing the *string*
`"false"` reads like "off" but keeps dry-run on, since every non-empty
string is truthy in JavaScript.

### The two-workflow flow

There is no release job inside `ci.yml`. Instead:

1. A push to `main` runs **CI** (`.github/workflows/ci.yml`) — three jobs:
   `Gate`, `Gate (expensive, native duckdb)`, `Gate (expensive,
   containerized duckdb)`.
2. When CI **completes**, **Release**
   (`.github/workflows/release.yml`) fires via a `workflow_run` trigger and
   guards on `github.event.workflow_run.conclusion == 'success'`. **Any
   failing CI job blocks the release**, and the guard additionally requires
   the run to have been a `push` to `main` from this repository, so a fork
   cannot drive it.
3. Its `release` job installs the pinned Node toolchain, runs
   `pnpm exec semantic-release`, and — via `@semantic-release/exec` —
   writes `new-release-version` / `new-release-published` to
   `$GITHUB_OUTPUT`.
4. Its `build-binaries` job runs only `if: needs.release.outputs.new-release-published == 'true'`,
   checks out the tag semantic-release just created, cross-compiles four
   binaries (`linux`/`darwin` × `amd64`/`arm64`) with the version injected
   via `-ldflags`, and uploads each raw and `.tar.gz` to the GitHub
   release.

Because `Release` is `workflow_run`-triggered, it **cannot fire from a pull
request** — GitHub does not dispatch `workflow_run` for PR events. That is
why the file's correctness is proven by reading it as data
(`tests/release/`) rather than by executing it on a branch.

### The infinite-loop guard, twice over

`@semantic-release/git` pushes a `chore(release): <version> [skip ci]`
commit to `main` with the updated `CHANGELOG.md`. Left unguarded, that push
would trigger CI, which would trigger Release, which would push again.
Two independent mechanisms stop it:

1. **`[skip ci]` in the commit subject** — GitHub Actions skips workflow
   runs for commits whose message contains it.
2. **`GITHUB_TOKEN` pushes do not trigger workflows** — a documented
   Actions behavior, independent of the commit message.

Either alone would be sufficient. Both are deliberate: relying on only the
message marker would break the moment someone reformatted the commit
template, and relying only on the token behavior would break if the
workflow were ever switched to a PAT.

### Checking the config without cutting a release

```sh
pnpm install --frozen-lockfile
just release-dry-run
```

This proves that `.releaserc.json` parses, that every plugin resolves and
loads, and what next version the commits on the current branch compute to.
`--branches "$(git branch --show-current)"` is load-bearing: without it,
semantic-release refuses to compute anything off a branch that is not
`main`.

It does **not** run `@semantic-release/exec`: `--dry-run` skips the publish
step, the only lifecycle step that plugin is wired into. It also needs
network access and a `$GITHUB_TOKEN`, which is why it is deliberately
**not** part of `just gate`/`just gate-expensive`.

What *is* gated: `tests/release/`'s untagged checks run in `just gate`
(the `-X` symbol path against `go.mod` and the source, the build target,
the `workflow_run` binding to CI's workflow `name:`, the Go version pin,
the matrix and its asset names, the `release` → `build-binaries` output
handoff, the `v` tag prefix, `.releaserc.json`'s plugin list and shape, and
`pnpm-lock.yaml`'s agreement with `package.json`), and
`TestReleaseLdflagsInjectVersion` / `TestReleaseMatrixTargetsCompile` run
in `just gate-expensive`.

`TestReleaseOutputHandoffIsWired` is worth calling out: it pins the
`new-release-version` / `new-release-published` names across all four
places that must agree on them — `publishCmd`'s `$GITHUB_OUTPUT` writes,
the `release` job's `outputs:` block, `build-binaries`' `if:`, and the
`-ldflags` version expression. Every break in that chain is silent (a
renamed output makes the `if:` compare false and publishes a tag and a
Release with zero binaries attached, green), and none of it is reachable
before a merge. What is still unproven until the first real release is only
the run-time half: that the publish step fires and that the redirect lands
in `$GITHUB_OUTPUT`.

## Adding a New Command

1. Add a use case under `internal/core/` (a new package with its own
   `Request`/`Run`, following `internal/core/sync/` or
   `internal/core/export/`).
2. If it needs a new external dependency, add a port in `internal/ports/`
   and an adapter under `internal/adapters/` implementing it — see "Adding
   a new port and adapter" below.
3. Add a thin shim file under `internal/adapters/cli/<name>.go` with a
   `newNameCmd(deps Deps)` function, and register it in
   `internal/adapters/cli/root.go` via `root.AddCommand(newNameCmd(deps))`.
4. Wire the new use case and any new adapters into `cli.Deps` in
   `cmd/agent-logs-extractor/main.go`.

## Adding a new port and adapter

1. Declare a small interface in a new `internal/ports/<name>.go` file — see
   `docs/architecture.md`'s Ports table for the shape existing ports take.
2. Add a fake implementing it in `internal/testing/fakes/fakes.go`
   (`FakeX` + `NewX(...)` constructor), following the existing fakes'
   "assert on observable state, never call sequences" convention.
3. Implement the real adapter under `internal/adapters/<name>/`, importing
   whatever infrastructure it needs — `internal/core` must never import it
   directly.
4. Wire the adapter into `cmd/agent-logs-extractor/main.go` and inject it
   into whichever use case(s) need it.
5. Update `docs/architecture.md`'s Ports and Adapters tables.

## Adding an acceptance criterion

1. Add the worked scenario to `docs/acceptance.md` (a `**AC-ID —
   ...**` heading with its Given/When/Then) and a matching row in §2's
   criteria index table, in the same change — `TestACCoverage`
   (`tests/e2e/coverage_test.go`) fails if only one exists.
2. For a `tier: e2e` row, add a `TestAC_<CATEGORY>_<NN>_<ShortName>`
   function under `tests/e2e/` — `TestACCoverage` fails until both the
   `docs/acceptance.md` row *and* the `TestAC_*` function exist, and the
   Test column must name the function it actually finds.
3. For `tier: unit`/`tier: integration`, point the Test column at an
   existing (or new) `TestXxx` function anywhere in the repo; for `tier:
   container`, point it at a `just <recipe>` that exists in the justfile.
4. Run `just test` — `TestACCoverage` is untagged and runs as part of it.
