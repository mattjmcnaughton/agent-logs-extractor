# TDD — agent-logs-extractor MVP

Technical design for the MVP scoped in `docs/product/prd-mvp.md`. Follows the same strict hexagonal (ports-and-adapters) architecture as `fetch-context` and `skillvendor`, and starts from the `golang-cli` copier template.

## Core decisions

The load-bearing choices, each with its rationale. Everything else in this document elaborates on one of these.

1. **Hexagonal architecture, strictly.** Pure core (`internal/core/`) with zero infrastructure imports; small ports (`internal/ports/`); one adapter per external dependency (`internal/adapters/`); wiring in `main.go` is the only place that sees concrete adapters. Same discipline as `fetch-context`.
2. **Fakes over mocks.** Testability comes from hand-written, in-memory fakes that implement the ports (an in-memory store, a canned-sessions source, a recording exporter) — not from a mocking framework or expectation-style tests. Fakes live in `internal/testing/fakes/` and are shared across test tiers. Tests assert on observable outcomes (what's in the store, what the summary says), never on call sequences.
3. **The canonical store is JSONL, not a database.** `sync` writes normalized, append-friendly JSONL owned by this tool. The store stays engine-agnostic, diffable, and trivially testable; DuckDB is a consumer, not the source of truth.
4. **DuckDB is invoked as a subprocess behind a port, not linked via CGO.** Precedent: `fetch-context` drives git via the CLI behind a port. `go-duckdb` drags in CGO and platform-specific builds; the `duckdb` CLI gives us the full SQL engine and a pure-Go binary. Cost: a runtime dependency for `export` only, reported with a clear install hint when missing.
5. **No in-tool query surface.** The DuckDB CLI over the export *is* the query interface; the README ships a cookbook. To make cookbook queries join-free, the export denormalizes session fields (`vendor`, `project_name`, `project_path`) onto `messages` and `tool_calls`.
6. **`sync` is a full, atomic rebuild.** No watermarks, no manifest, no incremental state. Parse everything, build the new store in a temp directory, swap it into place with a rename. A year of history parses in seconds; incrementality returns only if measurement says otherwise.
7. **Lenient, lossless parsing.** Unknown record types and malformed lines are counted and skipped, never fatal; every normalized row keeps the vendor record verbatim in a `raw` column; sessions carry the vendor version that wrote them, as a drift-debugging aid.
8. **Fixture-driven adapters, contract-tested against live logs.** Both vendor formats are undocumented. Real, scrubbed session files are committed as fixtures and define each adapter's behavior; an opt-in contract tier parses the developer's live `~/.claude` / `~/.codex` as the early-warning system for format drift.
9. **Local-only, sources read-only.** The tool never writes to vendor directories and never touches the network.

## Architecture overview

```
vendor logs ──(source adapters)──▶ unified model ──▶ canonical store ──▶ export
~/.claude          claude              sessions         JSONL, managed     duckdb CLI
~/.codex           codex               messages                           (snapshot .duckdb)
                                       tool_calls
```

Use cases in the core: `Sync` and `Export`, plus domain services for normalization. All take `context.Context` first and an explicit `*slog.Logger`. Source paths and store paths are resolved in wiring (including `AGENT_LOGS_EXTRACTOR_HOME` handling); the core never imports `os`, `net/http`, or `os/exec`.

## Unified data model

Three record types. IDs are vendor-namespaced (`claude:<uuid>`, `codex:<uuid>`) so they are globally unique across vendors.

### `sessions`

| Column | Type | Notes |
|---|---|---|
| `session_id` | TEXT PK | vendor-namespaced |
| `vendor` | TEXT | `claude` \| `codex` |
| `project_path` | TEXT | absolute cwd of the session |
| `project_name` | TEXT | basename of `project_path` (denormalized for ergonomic filters) |
| `started_at` / `ended_at` | TIMESTAMP | first / last record timestamp |
| `git_branch` | TEXT | nullable |
| `vendor_version` | TEXT | agent version that wrote the log; debugging aid for format drift |
| `source_path` | TEXT | the log file this row came from |

### `messages`

| Column | Type | Notes |
|---|---|---|
| `message_id` | TEXT PK | |
| `session_id` | TEXT | FK → sessions |
| `seq` | INT | 0-based order within the session |
| `parent_message_id` | TEXT | nullable; preserves Claude's tree structure |
| `role` | TEXT | `user` \| `assistant` \| `system` |
| `created_at` | TIMESTAMP | |
| `text` | TEXT | concatenated human-readable text content |
| `model` | TEXT | nullable; assistant messages only |
| `raw` | JSON | the vendor record, verbatim — normalization is never lossy |

Only conversational turns become `messages`. Vendor bookkeeping records (summaries, queue operations, attachments, mode changes, …) are counted and skipped. Tool results are attached to their `tool_calls` row, not emitted as separate messages — `role` stays a clean user/assistant/system signal for querying.

### `tool_calls`

| Column | Type | Notes |
|---|---|---|
| `tool_call_id` | TEXT PK | vendor-namespaced tool-use / call id |
| `session_id` | TEXT | FK → sessions |
| `message_id` | TEXT | FK → the assistant message that issued the call |
| `seq` | INT | order within the session |
| `tool_name` | TEXT | e.g. `Bash`, `Edit`, `shell` |
| `arguments` | JSON | tool input, verbatim |
| `output` | TEXT | tool result content; nullable if never returned |
| `status` | TEXT | `ok` \| `error` \| `pending` |
| `created_at` | TIMESTAMP | |

In the DuckDB export, `messages` and `tool_calls` additionally carry denormalized `vendor`, `project_name`, and `project_path` columns (core decision 5). The canonical store keeps them normalized; denormalization happens at export time.

## Vendor formats and mapping

Both formats are undocumented. Adapter behavior is defined by committed fixtures (real, scrubbed session files) — the notes below orient, the fixtures decide. Adapters parse leniently: unknown record types are counted and skipped, never fatal.

### Claude Code (`~/.claude/projects/`)

*Verified against a real Claude Code v2.x session log.*

- Layout: one directory per project (encoded cwd), containing `<session-uuid>.jsonl`.
- Conversational records (`type` ∈ `user`, `assistant`, `system`) carry `uuid`, `parentUuid`, `sessionId`, `timestamp`, `cwd`, `gitBranch`, `version`, `isSidechain`, and an API-shaped `message` with a content-block array. Additional bookkeeping types observed (`queue-operation`, `attachment`, `last-prompt`, `mode`, `summary`) are skipped.
- Mapping: `tool_use` blocks in assistant messages → `tool_calls` rows (`id`, `name`, `input`); `tool_result` blocks in subsequent user-type records are joined back by `tool_use_id` to fill `output`/`status` (user records also carry a richer top-level `toolUseResult` field), and do **not** produce `messages` rows; text blocks concatenate into `messages.text`. `tool_use` blocks carry a `caller` field distinguishing direct calls from subagent calls — relevant to the sidechain question below.

### Codex (`~/.codex/sessions/`)

*Reconstructed from community documentation; to be locked in by fixtures.*

- Layout: `YYYY/MM/DD/rollout-<timestamp>-<uuid>.jsonl`.
- A `session_meta` record opens the file (`id`, `cwd`, `cli_version`, …) → the `sessions` row. Subsequent records wrap a `payload`: `message` payloads (role + `input_text`/`output_text` blocks) → `messages`; `function_call` payloads (`name`, `arguments`, `call_id`) → `tool_calls`; `function_call_output` joins by `call_id` to fill `output`.

## Canonical store and sync

```
<store>/
  sessions/<vendor>/<session_id>.json     # one session doc: session row + messages + tool_calls
```

`sync` parses every session file found under the source roots, builds the complete new store in a temp directory next to the target, then atomically swaps it into place (rename old → discard, rename new → target). There is no manifest and no incremental state (core decision 6). The store is managed output: hand-edits are not supported and are clobbered on the next sync.

`sync` prints an ingest summary: per vendor, the number of sessions, messages, and tool calls ingested, and the number of records skipped (with a breakdown by reason at debug log level).

## Export path

`export duckdb` shells out to the `duckdb` CLI: a generated script builds the three relations from the store via `read_json`, applies the denormalization joins, and `COPY`s each into a fresh snapshot `.duckdb` file (written to a temp path, then renamed over the target). The export is a full snapshot each run, matching the full-rebuild sync.

## Ports and adapters

| Port | Purpose | MVP adapter | Fake |
|---|---|---|---|
| `ConversationSource` | enumerate session files for a vendor; parse one file → normalized session doc | `claudesource`, `codexsource` | canned-sessions source |
| `CanonicalStore` | read/write store docs; atomic swap | `jsonlstore` (afero) | in-memory store |
| `Exporter` | materialize the store into a sink | `duckdbcli` (`os/exec`) | recording exporter |

One file per cobra subcommand under `internal/adapters/cli/`, thin shims only: parse flags, call use case, format output.

## Error handling

- Missing vendor directory → not an error; that vendor reports zero sessions (US-7).
- Malformed line / unknown record type → skip, count, `slog` at debug; `sync` prints skip counts in its summary (US-6).
- `duckdb` binary absent → clear error naming the binary and an install hint; only `export` requires it.
- A failed sync or export never leaves a half-written store or `.duckdb` file — the temp-and-rename swap guarantees the previous output survives intact.

## Testing

Same four-tier pyramid as `fetch-context` (`docs/testing.md` there), built on fakes rather than mocks (core decision 2):

- **Unit** (no tag): normalization from fixture records, session-doc assembly, ingest-summary accounting — core use cases exercised against the fakes.
- **Integration** (`integration`): sync against fixture trees on a real temp filesystem; export against a real `duckdb` binary when present.
- **Contract** (`contract`, opt-in): parse the developer's *live* `~/.claude` / `~/.codex` and assert invariants (no panics, monotonic seq, join integrity) — the early-warning system for vendor format drift.
- **E2E** (`e2e`): black-box runs of the compiled binary against fixtures under `AGENT_LOGS_EXTRACTOR_HOME`, mapped 1:1 to AC-IDs once `docs/acceptance.md` exists.

Fixtures live in `internal/testing/logfixture/` — scrubbed real session files per vendor, plus pathological cases (truncated line, unknown type, missing tool result). Fakes live in `internal/testing/fakes/`.

## Open questions

1. Claude Code sidechains (subagent conversations, flagged by `isSidechain` and `tool_use.caller`): flatten into the parent session, or model as child sessions? MVP: flatten, keep `parentUuid` linkage in `parent_message_id`; revisit with real fixtures.
2. Message-level project override: Claude records `cwd` per record, and a session's cwd can change mid-conversation. MVP: session-level `project_path` from the first record; flag drift in the contract tests if it occurs in practice.
