# agent-logs-extractor

Parse the conversation logs that coding agents write to `~/.claude` and `~/.codex` into one unified data model, then export them as a queryable DuckDB database.

Tools like Claude Code and Codex record every conversation to disk in vendor-specific JSONL formats. `agent-logs-extractor` reads those logs, normalizes them into a common schema of **sessions**, **messages**, and **tool calls**, and materializes the result as a DuckDB file. From there, the DuckDB CLI answers everything: "every message I sent in project A over the last day", "every `Bash` tool call that ran `git push`", cross-vendor history in one query. One data model, one adapter per vendor — the tool does extraction and normalization, and DuckDB does the querying.

Everything stays on your machine. Logs contain prompts, code, and command output — nothing is ever sent anywhere.

> **Status: pre-MVP.** This README describes the target MVP surface. See `docs/product/prd-mvp.md` for scope and `docs/technical/tdd-mvp.md` for the design.

## Install

```
go install github.com/mattjmcnaughton/agent-logs-extractor/cmd/agent-logs-extractor@latest
```

Or, from a checkout:
```
just install
```

Querying the export requires the [DuckDB CLI](https://duckdb.org/docs/installation/); `sync` itself does not.

## Commands

```
agent-logs-extractor sync                      # parse vendor logs into the canonical store
agent-logs-extractor export duckdb [--out <path>]   # materialize the store as a .duckdb file
agent-logs-extractor version
```

### `sync`

Scan the vendor log directories, parse every session, and rebuild the canonical store from scratch. No incremental state, no watermarks — a full run over a year of history takes seconds, and the rebuild is atomic (the old store is swapped out only after the new one is complete).

```
agent-logs-extractor sync
agent-logs-extractor sync --vendor claude
agent-logs-extractor sync --claude-path /backups/dotclaude
```

- A malformed line or an unrecognized record type is logged and skipped — one bad record never fails the run. A summary reports how much was ingested and how much was skipped.
- A missing vendor directory is fine: that vendor simply contributes zero sessions.
- `--vendor claude|codex` restricts the run to one source; `--claude-path` / `--codex-path` override the default log roots. There is no config file.

### `export`

Materialize the canonical store into a sink. MVP ships one sink: a self-contained DuckDB database file.

```
agent-logs-extractor export duckdb                      # → ~/.local/share/agent-logs-extractor/export/logs.duckdb
agent-logs-extractor export duckdb --out ./logs.duckdb
```

The export is a full snapshot, rebuilt each run. Session fields (`vendor`, `project_name`, `project_path`) are denormalized onto `messages` and `tool_calls`, so the common queries need no joins. Additional sinks (parquet, sqlite, …) hang off the same sink port later.

## Querying

Point the DuckDB CLI at the export — there is deliberately no in-tool query command.

```
duckdb ~/.local/share/agent-logs-extractor/export/logs.duckdb
```

Every message you sent in a project over the last day:

```sql
SELECT created_at, text
FROM messages
WHERE role = 'user'
  AND project_name = 'fetch-context'
  AND created_at > now() - INTERVAL 1 DAY
ORDER BY created_at;
```

Every `Bash` tool call matching a pattern, across vendors, in the last week:

```sql
SELECT created_at, vendor, project_name, arguments
FROM tool_calls
WHERE tool_name = 'Bash'
  AND arguments LIKE '%git push%'
  AND created_at > now() - INTERVAL 7 DAY;
```

Sessions per project, most active first:

```sql
SELECT project_name, vendor, count(*) AS sessions, max(ended_at) AS last_active
FROM sessions
GROUP BY ALL
ORDER BY sessions DESC;
```

One-liners work too: `duckdb logs.duckdb -json "SELECT ..."`.

## Data model

Three tables, shared by every vendor. Vendor-specific fields survive in a `raw` JSON column so nothing is lost in normalization.

- **`sessions`** — one row per conversation: `session_id`, `vendor`, `project_path`, `project_name`, `started_at`, `ended_at`, `git_branch`, `source_path`.
- **`messages`** — one row per turn: `message_id`, `session_id`, `seq`, `role`, `created_at`, `text`, `model`, `raw` (+ denormalized `vendor`, `project_name`, `project_path`).
- **`tool_calls`** — one row per tool invocation, joined back to the requesting message: `tool_call_id`, `session_id`, `message_id`, `tool_name`, `arguments`, `output`, `status`, `created_at` (+ the same denormalized columns).

Full schema and the vendor→model field mappings live in `docs/technical/tdd-mvp.md`.

## File layout

```
~/.local/share/agent-logs-extractor/
  store/                          # canonical normalized store (managed; do not hand-edit)
  export/logs.duckdb              # default `export duckdb` output
```

There is no config file — behavior is controlled by flags, with sensible defaults.

## Sandboxing

Set `AGENT_LOGS_EXTRACTOR_HOME` to redirect every path (store, exports, and the default `~/.claude` / `~/.codex` lookups) under a custom root. Useful for tests and parallel runs.

```
AGENT_LOGS_EXTRACTOR_HOME=/tmp/sandbox agent-logs-extractor sync
```

## Scope

`agent-logs-extractor` deliberately does **not**:

- Provide a query language — the DuckDB CLI over the export is the query interface.
- Tail or watch logs live — `sync` is batch; re-run it (or cron it) to pick up new sessions.
- Track incremental state — every `sync` is a full, atomic rebuild.
- Modify or garbage-collect the vendor log directories — sources are strictly read-only.
- Send data anywhere — no telemetry, no cloud sync; sinks are local files.
- Redact secrets — logs are ingested verbatim. Treat the store and exports with the same care as `~/.claude` itself.

## Development

See [docs/development.md](docs/development.md) for setup instructions and common tasks.

## License

MIT — see [LICENSE](LICENSE).
