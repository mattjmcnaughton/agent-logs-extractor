# Log fixtures

Real vendor session logs, committed as the ground truth for the source adapters
(TDD core decision 8: fixtures define adapter behavior; these notes orient).

Each vendor directory mirrors the vendor's real on-disk layout, so a fixture root
can be passed directly as `--claude-path` / `--codex-path`:

```
claude/projects/<encoded-cwd>/<session-uuid>.jsonl     # mirrors ~/.claude
codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl    # mirrors ~/.codex
pathological/…                                          # malformed inputs, same layout
```

## Provenance

All fixtures were generated in a sandboxed container (2026-08-12) and contain no
personal data or secrets. They are committed **verbatim** — do not hand-edit;
regenerate instead.

- **`claude/`** — Claude Code v2.1.228, generated with a headless run:
  `claude -p "Use the Bash tool to run: echo hello fixture. Then reply with exactly: done" --allowedTools Bash --max-turns 4`.
  Contains a complete `tool_use` → `tool_result` pair plus the bookkeeping record
  types (`queue-operation`, `attachment`, `ai-title`, `last-prompt`).
- **`codex/`** — Codex CLI v0.147.0, generated with
  `codex exec --skip-git-repo-check "say hello"` **without OpenAI credentials**:
  the API call fails, but the rollout is real and contains `session_meta`,
  `event_msg`, `turn_context`, `world_state`, and user/developer `response_item`
  message records.

## Known gaps

- The Codex fixture has **no assistant `message`, `function_call`, or
  `function_call_output` records** (no credentials → no model turns). The
  Codex adapter's tool-call mapping needs a real authenticated session,
  scrubbed and added here, before it can be considered fixture-verified.
- Neither fixture exercises Claude subagent sidechains (`isSidechain: true`).

## Pathological cases (`pathological/`)

Derived from the real fixtures; exercise the skip-and-count contract — none of
these may fail a sync:

| File | Defect |
|---|---|
| `claude/.../11111111-….jsonl` | final line truncated mid-JSON |
| `claude/.../22222222-….jsonl` | unrecognized record type between valid records |
| `claude/.../33333333-….jsonl` | `tool_use` whose `tool_result` record is missing (→ `status: pending`) |
| `claude/.../44444444-….jsonl` | empty file |
| `codex/.../rollout-…-11111111-….jsonl` | third line truncated mid-JSON |
