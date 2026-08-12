# PRD — agent-logs-extractor MVP

## Problem

Coding agents keep a complete record of every conversation — prompts, responses, tool calls, command output — but each vendor writes it in its own format to its own directory (`~/.claude/projects/**/*.jsonl` for Claude Code, `~/.codex/sessions/**/*.jsonl` for Codex). That history is effectively write-only today:

- There is no way to ask a question across vendors ("what did I work on yesterday?" spans both).
- The raw formats are nested JSONL keyed by vendor-internal concepts; even single-vendor questions require throwaway `jq` archaeology.
- The formats are undocumented and change between vendor versions, so every ad-hoc script rots.

The data to answer "show me every message I sent in project A over the last day" or "find every `Bash` tool call that ran `git push --force`" already exists on disk. It just isn't queryable.

## Solution

A local-first Go CLI that does extraction and normalization, and delegates querying to DuckDB:

1. **Parses** vendor logs through per-vendor adapters into **one unified data model** (sessions, messages, tool calls).
2. **Stores** the normalized result in a canonical local store — a full, atomic rebuild on every `sync`.
3. **Exports** it as a self-contained DuckDB database; the DuckDB CLI is the query interface, supported by a query cookbook in the README.

The vendor adapters are the isolation boundary: when a vendor changes its format, one adapter changes; the data model, store, and sinks do not.

## Users

- **Primary: the tool's author** and developers like them — heavy daily users of Claude Code and/or Codex who want to search, audit, and analyze their own agent history.
- **Secondary: agent tinkerers** — people building retrospectives, usage dashboards, or prompt-mining pipelines on top of their logs, who want a clean DuckDB file instead of raw JSONL.

## Goals

- One unified schema across vendors; no query ever needs vendor-specific knowledge.
- The motivating queries work against the export with cookbook SQL:
  - all messages I sent in project `$A` over the last day;
  - all tool calls with name `$T` (optionally matching pattern `$P`) in a time window;
  - all sessions per project / per vendor.
- Lossless enough to trust: anything the normalizer doesn't map is preserved in a `raw` column; malformed input is skipped and counted, never silently dropped.
- Local-only: nothing leaves the machine.
- `sync` is idempotent and fast enough that incremental state is unnecessary; running it in a cron/hook loop is cheap.

## Non-goals (MVP)

- **An in-tool query surface** — no `query` command, no filter subcommands; the DuckDB CLI over the export is the query interface.
- **Incremental sync** — every `sync` is a full rebuild; watermark state returns only if a measured sync is actually slow.
- **A config file** — flags and `AGENT_LOGS_EXTRACTOR_HOME` cover the MVP; no config format, no `edit` command.
- **Live tailing / watch mode** — batch `sync` only.
- **Vendors beyond Claude Code and Codex** (Gemini CLI, Cursor, aider, …) — the adapter port is designed for them, but none ship in the MVP.
- **Sinks beyond DuckDB** — parquet/sqlite/postgres hang off the same port later.
- **Secret redaction** — logs are ingested verbatim; the store inherits the sensitivity of the source logs.
- **A TUI or web UI.**
- **Modifying vendor logs** — sources are read-only; this is not a log rotation or cleanup tool.
- **Analytics/derived metrics** (token costs, usage stats) — expressible via SQL, but not productized.

## User stories

| ID | Story |
|----|-------|
| US-1 | As a user, I run `sync` and my Claude Code and Codex histories are parsed into the store, with a summary of sessions/messages ingested and records skipped. |
| US-2 | As a user, I re-run `sync` after a day of work and get an identical-or-updated store in seconds; re-running is always safe. |
| US-3 | As a user, I can list all messages I sent in project `$A` over the last day with a cookbook query against the export. |
| US-4 | As a user, I can find all `Bash` tool calls whose arguments match a pattern, across vendors, in a time window, with a cookbook query. |
| US-5 | As a user, I can export everything to a single `.duckdb` file and open it in the DuckDB CLI or a notebook with no schema explanation beyond column names. |
| US-6 | As a user, a corrupt line in one session file does not fail my sync; it is reported and skipped. |
| US-7 | As a user on a machine with only one vendor installed, the missing vendor's directory is silently fine. |

## MVP scope

**Commands:** `sync` (with `--vendor`, `--claude-path`, `--codex-path`), `export duckdb` (with `--out`), `version` — surface as specified in `README.md`.

**Adapters:** `claude` (Claude Code `~/.claude/projects`), `codex` (Codex `~/.codex/sessions`).

**Sinks:** `duckdb` (full-snapshot file export, with session fields denormalized onto `messages`/`tool_calls` for join-free querying).

**Config:** none. Flags for path overrides; `AGENT_LOGS_EXTRACTOR_HOME` for sandboxing. Zero-config must work.

## Success criteria

- The motivating queries (US-3, US-4) work against the author's real logs using only the README cookbook.
- A full `sync` over a year of real history completes in seconds.
- A vendor-format change is absorbed by editing only that vendor's adapter (+ fixtures).
- The exported `.duckdb` file is directly usable in the DuckDB CLI with no schema explanation beyond column names.

## Risks

- **Undocumented, unstable vendor formats.** The Claude Code mapping has been verified against a real session log; Codex's is reconstructed from community documentation. Mitigation: fixture-driven adapters (real, scrubbed samples committed as test fixtures), lenient parsing with skip-and-count, `raw` preservation, and a per-record vendor-version column to debug drift.
- **DuckDB dependency weight** (CGO vs subprocess). Addressed in `docs/technical/tdd-mvp.md`; the choice is behind a port either way.
- **Sensitive data concentration.** The store aggregates everything the agent ever saw. Mitigation: local-only by design, documented loudly; redaction stays a candidate post-MVP feature.

## Post-MVP candidates

In rough priority order, informed by real usage of the MVP:

- **Query surface:** an in-tool `query` command and/or convenience filter subcommands (`sessions`, `messages`, `tool-calls`), if cookbook SQL proves too much friction.
- **Incremental sync:** watermark state, if full rebuilds get slow.
- **Config file + `edit`:** if flag overrides stop being enough.
- Additional vendors (Gemini CLI, Cursor), additional sinks (parquet, sqlite), watch mode, redaction filters, saved queries, usage analytics.
