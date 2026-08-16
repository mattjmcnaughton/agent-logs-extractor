# agent-logs-extractor

Parse the conversation logs that coding agents write to `~/.claude` and `~/.codex` into one unified data model, then export them as a queryable DuckDB database.

Tools like Claude Code and Codex record every conversation to disk in vendor-specific JSONL formats. `agent-logs-extractor` reads those logs, normalizes them into a common schema of **sessions**, **messages**, and **tool calls**, and materializes the result as a DuckDB file. From there, the DuckDB CLI answers everything: "every message I sent in project A over the last day", "every `Bash` tool call that ran `git push`", cross-vendor history in one query. One data model, one adapter per vendor — the tool does extraction and normalization, and DuckDB does the querying.

Everything stays on your machine. Logs contain prompts, code, and command output — nothing is ever sent anywhere.

See `docs/product/prd-mvp.md` for scope and `docs/technical/tdd-mvp.md` for the design.

## Install

The release pipeline is built but is deliberately still rehearsing
(`"dryRun": true` in `.releaserc.json`), so no release has been published
yet — build from source for now. Once it is switched on, each [GitHub
release](https://github.com/mattjmcnaughton/agent-logs-extractor/releases)
will carry prebuilt binaries, each one both raw and as a `.tar.gz`:

| Asset | Platform |
|---|---|
| `agent-logs-extractor-linux-x86_64` | Linux, x86_64 |
| `agent-logs-extractor-linux-arm64` | Linux, arm64 |
| `agent-logs-extractor-macos-x86_64` | macOS, Intel |
| `agent-logs-extractor-macos-arm64` | macOS, Apple silicon |

Download the one for your platform, `chmod +x` it, and put it on your
`PATH`. `agent-logs-extractor version` reports the released version.

Or with the Go toolchain:
```
go install github.com/mattjmcnaughton/agent-logs-extractor/cmd/agent-logs-extractor@latest
```

Or, from a checkout:
```
just build   # builds to bin/agent-logs-extractor
```

(Both source builds report version `dev`; only the release binaries carry a
real version.)

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

```
claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped
```

- A malformed line or an unrecognized record type is logged and skipped — one bad record never fails the run, and a session file that can't even be read (a permissions error, say) is likewise counted, not fatal. The one-line-per-vendor summary above reports how much was ingested and how much was skipped; the full skip breakdown by reason is available at `--log-level debug`.
- A missing vendor directory is fine: that vendor simply contributes zero sessions.
- `--vendor claude|codex` restricts the run to one source; `--claude-path` / `--codex-path` override the default log roots. There is no config file.
- **`--vendor` rebuilds the whole store from that vendor alone.** `sync` is always a full rebuild (no incremental state), so `sync --vendor claude` replaces the *entire* canonical store with claude-only content, removing any other vendor's sessions that were in it. A bare `sync` (no `--vendor`) is the way to keep every vendor's sessions in the store together.
- Codex support (`codexsource`) hasn't landed yet: a bare `sync` currently syncs Claude only, and `sync --vendor codex` errors, naming what's actually available. `--codex-path` is accepted and stored but not yet consulted.

### `export`

Materialize the canonical store into a sink. MVP ships one sink: a self-contained DuckDB database file.

```
agent-logs-extractor export duckdb                      # → ~/.local/share/agent-logs-extractor/export/logs.duckdb
agent-logs-extractor export duckdb --out ./logs.duckdb
```

The export is a full snapshot, rebuilt each run: it shells out to the `duckdb` CLI, so it requires that binary on `PATH` — `sync` never does. If it's missing, `export duckdb` fails with an install hint pointing at [duckdb.org](https://duckdb.org/docs/installation/); if the export itself fails for any other reason, re-run with `--log-level debug` to see the generated SQL. Session fields (`vendor`, `project_name`, `project_path`) are denormalized onto `messages` and `tool_calls`, so the common queries need no joins. Additional sinks (parquet, sqlite, …) hang off the same sink port later.

## Querying

Point the DuckDB CLI at the export — there is deliberately no in-tool query command. Timestamp columns (`started_at`, `ended_at`, `created_at`) are `TIMESTAMPTZ`, so time comparisons like `now() - INTERVAL 1 DAY` below are unambiguous UTC instants regardless of your local timezone; `raw` and `arguments` are DuckDB's native `JSON` type, so they're queryable with `json_extract`/`->>`, not just `LIKE`.

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

That default root is actually `$XDG_DATA_HOME/agent-logs-extractor/` when `XDG_DATA_HOME` is set to an absolute path, falling back to `~/.local/share/agent-logs-extractor/` otherwise — so on a machine with no `XDG_DATA_HOME` set, nothing changes. `~/.claude` and `~/.codex` are unaffected either way; see Sandboxing below for the full precedence. There is no config file — behavior is controlled by flags, with sensible defaults.

## Sandboxing

Set `AGENT_LOGS_EXTRACTOR_HOME` to redirect every path — store, exports, *and* the default `~/.claude` / `~/.codex` lookups — under a custom root. Useful for tests and parallel runs.

```
AGENT_LOGS_EXTRACTOR_HOME=/tmp/sandbox agent-logs-extractor sync
```

Full precedence for the store/export data directory:

1. `AGENT_LOGS_EXTRACTOR_HOME`, when set, wins outright — `<home>/.local/share/agent-logs-extractor/...` — and `XDG_DATA_HOME` is not even consulted. This is what keeps a sandboxed or test run hermetic even on a machine that also happens to export `XDG_DATA_HOME`.
2. Otherwise, `XDG_DATA_HOME` wins if it's set to an absolute path: `$XDG_DATA_HOME/agent-logs-extractor/...`.
3. Otherwise, `~/.local/share/agent-logs-extractor/...`.

`~/.claude` and `~/.codex` always resolve from `AGENT_LOGS_EXTRACTOR_HOME` (or the real home directory) — never from `XDG_DATA_HOME`, which governs *this tool's own* data directory, not where a third-party tool keeps its logs.

## Logging

Log level is controlled by the `--log-level` persistent flag (debug, info, warn, error) or the `AGENT_LOGS_EXTRACTOR_LOG_LEVEL` environment variable, in that order of precedence, defaulting to `info` if neither is set. An invalid `--log-level` value is a hard error; an invalid env var value prints a warning and falls back to `info`.

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
