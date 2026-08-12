# agent-logs-extractor

Parse the conversation logs that coding agents write to `~/.claude` and `~/.codex` into one unified data model, then query and export them.

Tools like Claude Code and Codex record every conversation to disk in vendor-specific JSONL formats. `agent-logs-extractor` reads those logs, normalizes them into a common schema of **sessions**, **messages**, and **tool calls**, and stores the result in a local canonical store. From there you can run SQL over the whole history ("every message I sent in project A over the last day"), filter with convenience commands (tool calls by name, messages by pattern), or export to sinks like DuckDB for analysis elsewhere. One data model, one adapter per vendor.

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

## Commands

```
agent-logs-extractor sync                      # parse vendor logs into the canonical store (incremental)
agent-logs-extractor query "<sql>"             # SQL over sessions / messages / tool_calls
agent-logs-extractor sessions   [filters]      # list sessions
agent-logs-extractor messages   [filters]      # list messages
agent-logs-extractor tool-calls [filters]      # list tool calls
agent-logs-extractor export duckdb [--out <path>]   # materialize the store as a .duckdb file
agent-logs-extractor edit                      # open config in $VISUAL/$EDITOR/vi
agent-logs-extractor version
```

### `sync`

Scan the configured vendor log directories, parse anything new or changed, and write normalized records to the canonical store.

```
agent-logs-extractor sync
agent-logs-extractor sync --vendor claude
```

- Incremental: session files already ingested and unchanged are skipped. Re-running is cheap and idempotent.
- A malformed line or an unrecognized record type is logged and skipped — one bad record never fails the run. A summary reports how much was ingested and how much was skipped.
- `--vendor claude|codex` restricts the run to one source.

### `query`

Run a SQL query (DuckDB dialect) against the normalized views `sessions`, `messages`, and `tool_calls`.

```
agent-logs-extractor query "
  SELECT created_at, text
  FROM messages
  WHERE role = 'user'
    AND project_name = 'fetch-context'
    AND created_at > now() - INTERVAL 1 DAY
  ORDER BY created_at"
```

- `--format table|json|jsonl` controls output (default `table`).
- The query runs read-only over the canonical store; it cannot modify ingested data.

### `sessions`, `messages`, `tool-calls`

Convenience filters over the same data — each compiles to a `query` under the hood.

```
agent-logs-extractor messages --role user --project fetch-context --since 24h
agent-logs-extractor tool-calls --tool Bash --pattern 'git push' --since 7d
agent-logs-extractor sessions --vendor codex --since 30d
```

Shared flags:

| Flag | Meaning |
|---|---|
| `--vendor claude\|codex` | restrict to one vendor |
| `--project <name-or-path>` | match project by basename or full path |
| `--since 24h` / `--until ...` | time window (durations or RFC 3339) |
| `--pattern <regexp>` | regexp over message text / tool-call arguments and output |
| `--role user\|assistant` | (`messages` only) |
| `--tool <name>` | (`tool-calls` only) |
| `--limit N`, `--format table\|json\|jsonl` | output control |

### `export`

Materialize the canonical store into a sink. MVP ships one sink: a self-contained DuckDB database file.

```
agent-logs-extractor export duckdb                      # → ~/.local/share/agent-logs-extractor/export/logs.duckdb
agent-logs-extractor export duckdb --out ./logs.duckdb
```

The export is a full snapshot, rebuilt each run. Additional sinks (parquet, sqlite, …) hang off the same sink port later.

### `edit`

Opens the config in `$VISUAL`, then `$EDITOR`, then `vi`. After the editor exits, the config is reloaded and validated; an invalid edit prints an error and leaves the broken file on disk for you to fix.

## Data model

Three tables, shared by every vendor. Vendor-specific fields survive in a `raw` JSON column so nothing is lost in normalization.

- **`sessions`** — one row per conversation: `session_id`, `vendor`, `project_path`, `project_name`, `started_at`, `ended_at`, `git_branch`, `source_path`.
- **`messages`** — one row per turn: `message_id`, `session_id`, `seq`, `role`, `created_at`, `text`, `model`, `raw`.
- **`tool_calls`** — one row per tool invocation, joined back to the requesting message: `tool_call_id`, `session_id`, `message_id`, `tool_name`, `arguments`, `output`, `status`, `created_at`.

Full schema and the vendor→model field mappings live in `docs/technical/tdd-mvp.md`.

## File layout

```
~/.config/agent-logs-extractor/
  config.yaml                     # optional; defaults work with no config

~/.local/share/agent-logs-extractor/
  store/                          # canonical normalized store (managed; do not hand-edit)
  export/logs.duckdb              # default `export duckdb` output
```

### Config format

All keys are optional — with no config file, both vendors are read from their default locations.

```yaml
sources:
  claude:
    enabled: true
    path: ~/.claude          # override the log root
  codex:
    enabled: true
    path: ~/.codex

store:
  path: ~/.local/share/agent-logs-extractor/store
```

## Sandboxing

Set `AGENT_LOGS_EXTRACTOR_HOME` to redirect every path (config, store, exports, and `~` expansion for source paths) under a custom root. Useful for tests and parallel runs.

```
AGENT_LOGS_EXTRACTOR_HOME=/tmp/sandbox agent-logs-extractor sync
```

## Scope

`agent-logs-extractor` deliberately does **not**:

- Tail or watch logs live — `sync` is batch; re-run it (or cron it) to pick up new sessions.
- Modify or garbage-collect the vendor log directories — sources are strictly read-only.
- Send data anywhere — no telemetry, no cloud sync; sinks are local files.
- Redact secrets — logs are ingested verbatim. Treat the store and exports with the same care as `~/.claude` itself.

## Development

See [docs/development.md](docs/development.md) for setup instructions and common tasks.

## License

MIT — see [LICENSE](LICENSE).
