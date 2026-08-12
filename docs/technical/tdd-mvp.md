# TDD — agent-logs-extractor MVP

Technical design for the MVP scoped in `docs/product/prd-mvp.md`. Follows the same strict hexagonal (ports-and-adapters) architecture as `fetch-context` and `skillvendor`, and starts from the `golang-cli` copier template.

## Architecture overview

```
vendor logs ──(source adapters)──▶ unified model ──▶ canonical store ──▶ query / sinks
~/.claude          claude              sessions         JSONL, managed      duckdb CLI
~/.codex           codex               messages                             (views + export)
                                       tool_calls
```

The **core** (`internal/core/`) is pure: use cases (`sync`, `query`, `export`, …) and domain services (normalization, session identity, watermarking) with zero infrastructure imports. **Ports** (`internal/ports/`) are the interfaces the core depends on. **Adapters** (`internal/adapters/`) implement them. **Wiring** in `cmd/agent-logs-extractor/main.go` is the only place that imports every concrete adapter.

Two load-bearing decisions, made up front:

1. **The canonical store is JSONL, not a database.** `sync` writes normalized, append-friendly JSONL files owned by this tool. DuckDB then *queries* the store in place (`read_json`) and *exports* it (snapshot `.duckdb` file). The store stays engine-agnostic, diffable, and trivially testable; DuckDB is a consumer, not the source of truth.
2. **DuckDB is invoked as a subprocess behind a port, not linked via CGO.** Precedent: `fetch-context` drives git via the CLI behind a port. `go-duckdb` drags in CGO and platform-specific builds; the `duckdb` CLI gives us the full SQL engine, JSON output, and a pure-Go binary. Cost: a runtime dependency for `query`/`export` (not for `sync`), reported with a clear install hint when missing. Revisit via ADR if the subprocess boundary hurts.

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

Only conversational turns become `messages`. Vendor bookkeeping records (summaries, meta events) are counted and skipped. Tool results are attached to their `tool_calls` row, not emitted as separate messages — `role` stays a clean user/assistant/system signal for querying.

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

## Vendor formats and mapping

Both formats are undocumented and observed empirically; the notes below are the starting hypothesis, **to be locked in by committed fixtures** (real, scrubbed session files) before adapter code is written. Adapters parse leniently: unknown record types are counted and skipped, never fatal.

### Claude Code (`~/.claude/projects/`)

- Layout: one directory per project (encoded cwd), containing `<session-uuid>.jsonl`.
- Records carry `type` (`user`, `assistant`, `summary`, …), `uuid`, `parentUuid`, `sessionId`, `timestamp`, `cwd`, `gitBranch`, `version`, and an API-shaped `message` with a content-block array.
- Mapping: `tool_use` blocks in assistant messages → `tool_calls` rows (`id`, `name`, `input`); `tool_result` blocks in subsequent user-type records are joined back by `tool_use_id` to fill `output`/`status`, and do **not** produce `messages` rows; text blocks concatenate into `messages.text`.

### Codex (`~/.codex/sessions/`)

- Layout: `YYYY/MM/DD/rollout-<timestamp>-<uuid>.jsonl`.
- A `session_meta` record opens the file (`id`, `cwd`, `cli_version`, …) → the `sessions` row. Subsequent records wrap a `payload`: `message` payloads (role + `input_text`/`output_text` blocks) → `messages`; `function_call` payloads (`name`, `arguments`, `call_id`) → `tool_calls`; `function_call_output` joins by `call_id` to fill `output`.

## Canonical store and incremental sync

```
<store>/
  manifest.json                    # watermarks: source_path → {size, mtime, session_id}
  sessions/<vendor>/<session_id>.json     # one session doc: session row + messages + tool_calls
```

- Unit of work = one vendor session file. `sync` diffs the manifest against the source tree: unchanged (same size + mtime) → skip; new or grown → re-parse that file and atomically rewrite its store doc (temp file + rename). Vendor session files are append-only in practice, and re-parsing one file is cheap, so per-file re-parse — not byte-offset resumption — is the simplicity/perf sweet spot.
- The store is managed output: hand-edits are not supported and are clobbered on the next sync of that session.

## Query path

`query` shells out to `duckdb` with a generated prelude that builds the three relations from the store via `read_json`, then runs the user's SQL read-only with `-json` (or table) output. `sessions` / `messages` / `tool-calls` subcommands are thin SQL compilers onto the same path — every flag (`--project`, `--since`, `--pattern`, `--tool`, `--role`, …) maps to a WHERE clause, with user input passed as bound parameters, not string-spliced. `export duckdb` runs the same prelude and `COPY`s each relation into a fresh snapshot `.duckdb` file (temp + rename).

## Ports and adapters

| Port | Purpose | MVP adapter(s) |
|---|---|---|
| `ConversationSource` | enumerate session files for a vendor; parse one file → normalized session doc | `claudesource`, `codexsource` |
| `CanonicalStore` | read/write store docs + manifest | `jsonlstore` (afero) |
| `QueryEngine` | run SQL over the store; export snapshots | `duckdbcli` (`os/exec`) |
| `ConfigStore` | load/validate config | `configstore` (strict yaml.v3) |
| `Editor` | `edit` command | `editor` |
| `Clock` | now() for `--since` resolution; injectable in tests | `realclock` |

Use cases (`internal/core/`): `Sync`, `Query`, `Export`, `EditConfig`, plus domain services for normalization and watermark diffing. All take `context.Context` first and an explicit `*slog.Logger`. Source paths and store paths are resolved in wiring (with `AGENT_LOGS_EXTRACTOR_HOME` handling); the core never touches `os`.

One file per cobra subcommand under `internal/adapters/cli/`, thin shims only.

## Error handling

- Missing vendor directory → not an error; that vendor reports zero sessions (US-8).
- Malformed line / unknown record type → skip, increment a per-file counter, `slog` at debug; `sync` prints an ingest summary including skip counts (US-7).
- `duckdb` binary absent → clear error naming the binary and an install hint; only `query`/`export` require it.
- Query SQL errors → duckdb's stderr surfaced verbatim.

## Testing

Same four-tier pyramid as `fetch-context` (`docs/testing.md` there):

- **Unit** (no tag): normalization from fixture records, watermark diffing, SQL compilation from flags, config validation.
- **Integration** (`integration`): sync against fixture trees on a real temp filesystem; query/export against a real `duckdb` binary when present.
- **Contract** (`contract`, opt-in): parse the developer's *live* `~/.claude` / `~/.codex` and assert invariants (no panics, monotonic seq, join integrity) — the early-warning system for vendor format drift.
- **E2E** (`e2e`): black-box runs of the compiled binary against fixtures under `AGENT_LOGS_EXTRACTOR_HOME`, mapped 1:1 to AC-IDs once `docs/acceptance.md` exists.

Fixtures live in `internal/testing/logfixture/` — scrubbed real session files per vendor, plus pathological cases (truncated line, unknown type, missing tool result).

## Open questions

1. Claude Code sidechains (subagent conversations in the same session file): flatten into the parent session, or model as child sessions? MVP: flatten, keep `parentUuid` linkage in `parent_message_id`; revisit with real fixtures.
2. Should `export` support incremental refresh instead of full snapshot? MVP: snapshot only — full export of even large histories is fast in DuckDB.
3. Message-level project override: Claude records `cwd` per record, and a session's cwd can change mid-conversation. MVP: session-level `project_path` from the first record; flag drift in the contract tests if it occurs in practice.
