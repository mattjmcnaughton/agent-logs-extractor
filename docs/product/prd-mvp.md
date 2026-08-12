# PRD — agent-logs-extractor MVP

## Problem

Coding agents keep a complete record of every conversation — prompts, responses, tool calls, command output — but each vendor writes it in its own format to its own directory (`~/.claude/projects/**/*.jsonl` for Claude Code, `~/.codex/sessions/**/*.jsonl` for Codex). That history is effectively write-only today:

- There is no way to ask a question across vendors ("what did I work on yesterday?" spans both).
- The raw formats are nested JSONL keyed by vendor-internal concepts; even single-vendor questions require throwaway `jq` archaeology.
- The formats are undocumented and change between vendor versions, so every ad-hoc script rots.

The data to answer "show me every message I sent in project A over the last day" or "find every `Bash` tool call that ran `git push --force`" already exists on disk. It just isn't queryable.

## Solution

A local-first Go CLI that:

1. **Parses** vendor logs through per-vendor adapters into **one unified data model** (sessions, messages, tool calls).
2. **Stores** the normalized result in a canonical local store, incrementally — re-running `sync` picks up only new activity.
3. **Queries** it: raw SQL for power users, convenience commands (`messages`, `tool-calls`, `sessions`) for the common filters.
4. **Exports** it to sinks for use outside the CLI — DuckDB in the MVP, more later.

The vendor adapters are the isolation boundary: when a vendor changes its format, one adapter changes; the data model, store, queries, and sinks do not.

## Users

- **Primary: the tool's author** and developers like them — heavy daily users of Claude Code and/or Codex who want to search, audit, and analyze their own agent history.
- **Secondary: agent tinkerers** — people building retrospectives, usage dashboards, or prompt-mining pipelines on top of their logs, who want a clean DuckDB file instead of raw JSONL.

## Goals

- One unified schema across vendors; no query ever needs vendor-specific knowledge.
- Answer the motivating queries out of the box:
  - all messages I sent in project `$A` over the last day;
  - all tool calls with name `$T` (optionally matching pattern `$P`) in a time window;
  - all sessions per project / per vendor.
- Lossless enough to trust: anything the normalizer doesn't map is preserved in a `raw` column; malformed input is skipped and counted, never silently dropped.
- Local-only: nothing leaves the machine.
- `sync` is idempotent and incremental; running it in a cron/hook loop is cheap.

## Non-goals (MVP)

- **Live tailing / watch mode** — batch `sync` only.
- **Vendors beyond Claude Code and Codex** (Gemini CLI, Cursor, aider, …) — the adapter port is designed for them, but none ship in the MVP.
- **Sinks beyond DuckDB** — parquet/sqlite/postgres hang off the same port later.
- **Secret redaction** — logs are ingested verbatim; the store inherits the sensitivity of the source logs.
- **A TUI or web UI** — output is table/JSON/JSONL on stdout.
- **Modifying vendor logs** — sources are read-only; this is not a log rotation or cleanup tool.
- **Analytics/derived metrics** (token costs, usage stats) — expressible via SQL, but not productized.

## User stories

| ID | Story |
|----|-------|
| US-1 | As a user, I run `sync` and my Claude Code and Codex histories are parsed into the store, with a summary of sessions/messages ingested and records skipped. |
| US-2 | As a user, I run `sync` again after a day of work and only the new activity is parsed. |
| US-3 | As a user, I can list all messages I sent in project `$A` over the last day with a single `messages` invocation. |
| US-4 | As a user, I can find all `Bash` tool calls whose arguments or output match a regexp, across vendors, in a time window. |
| US-5 | As a user, I can run an arbitrary SQL query over `sessions` / `messages` / `tool_calls` and get table, JSON, or JSONL output. |
| US-6 | As a user, I can export everything to a single `.duckdb` file and open it in the DuckDB CLI or a notebook. |
| US-7 | As a user, a corrupt line in one session file does not fail my sync; it is reported and skipped. |
| US-8 | As a user on a machine with only one vendor installed, the missing vendor's directory is silently fine. |

## MVP scope

**Commands:** `sync`, `query`, `sessions`, `messages`, `tool-calls`, `export duckdb`, `edit`, `version` — surface as specified in `README.md`.

**Adapters:** `claude` (Claude Code `~/.claude/projects`), `codex` (Codex `~/.codex/sessions`).

**Sinks:** `duckdb` (full-snapshot file export).

**Config:** optional `~/.config/agent-logs-extractor/config.yaml` (source enable/path overrides, store path); `AGENT_LOGS_EXTRACTOR_HOME` sandboxing. Zero-config must work.

## Success criteria

- Both motivating queries (US-3, US-4) work against the author's real logs with no manual pre-processing.
- `sync` over a year of real history completes in seconds; a no-op re-`sync` in well under a second of parsing work.
- A vendor-format change is absorbed by editing only that vendor's adapter (+ fixtures).
- The exported `.duckdb` file is directly usable in the DuckDB CLI with no schema explanation beyond column names.

## Risks

- **Undocumented, unstable vendor formats.** Mitigation: fixture-driven adapters (real, scrubbed samples committed as test fixtures), lenient parsing with skip-and-count, `raw` preservation, and a per-record vendor-version column to debug drift.
- **DuckDB dependency weight** (CGO vs subprocess). Addressed in `docs/technical/tdd-mvp.md`; the choice is behind a port either way.
- **Sensitive data concentration.** The store aggregates everything the agent ever saw. Mitigation: local-only by design, documented loudly; redaction stays a candidate post-MVP feature.

## Post-MVP candidates

Additional vendors (Gemini CLI, Cursor), additional sinks (parquet, sqlite), incremental export, watch mode, redaction filters, saved/named queries, usage analytics.
