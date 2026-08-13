# Log fixtures

Real vendor session logs, committed as the ground truth for the source adapters
(TDD core decision 8: fixtures define adapter behavior; these notes orient).

Each vendor directory mirrors the vendor's real on-disk layout, so a fixture root
can be passed directly as `--claude-path` / `--codex-path`:

```
claude/projects/<encoded-cwd>/<session-uuid>.jsonl                                   # mirrors ~/.claude
claude/projects/<encoded-cwd>/<session-uuid>/subagents/agent-<agentId>.jsonl         # a subagent sidechain transcript
                                                                                       # (same sessionId as the parent
                                                                                       # file; see the sidechain fixture
                                                                                       # below)
claude/projects/<encoded-cwd>/<session-uuid>/subagents/agent-<agentId>.meta.json     # sidecar: agentType, description,
                                                                                       # toolUseId, spawnDepth. Copied
                                                                                       # verbatim by scrub.Tree (not
                                                                                       # scrubbed line-by-line, since
                                                                                       # it isn't JSONL) but still
                                                                                       # findings-checked as a whole file.
codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl    # mirrors ~/.codex
pathological/…                                          # malformed inputs, same layout
```

Go helpers for locating these paths (so tests never hardcode a repo-relative
path or a session UUID) live in `logfixture.go` — `logfixture.SessionFile`,
`logfixture.SubagentFiles`, `logfixture.ClaudeSidechainProject`, etc.

## Provenance

All fixtures are committed **verbatim** — do not hand-edit; regenerate instead
with `just scrub-fixture` (see "Regenerating a fixture" below). Each
subsection gives the exact command used to generate and scrub it.

### `claude/projects/-tmp-claude-0--home-user-…-scratchpad-fixture-project/94ba8eae-….jsonl`

- **Vendor version:** Claude Code v2.1.228.
- **Generated:** 2026-08-12, in a sandboxed container, under a clean `HOME`
  so no account-specific skills, plugins, or MCP connectors leak into the
  log (the `skill_listing` / `deferred_tools_delta` attachments contain
  only harness defaults).
- **Exercises:** a complete `tool_use` → `tool_result` pair (Bash) plus the
  bookkeeping record types (`queue-operation`, `attachment`, `ai-title`,
  `last-prompt`).
- **Regenerate:**
  ```bash
  HOME=<clean-dir> claude -p "Use the Bash tool to run: echo hello fixture. Then reply with exactly: done" \
    --allowedTools Bash --max-turns 4
  ```
  This fixture predates `just scrub-fixture` and was hand-scrubbed; a
  regeneration should go through `just scrub-fixture` like the two fixtures
  below.

### `claude/projects/-home-user-fixture-sidechain/`

- **Vendor version:** Claude Code v2.1.231.
- **Generated:** 2026-08-13, in scratch space, never against a real
  `~/.claude`.
- **Exercises:** a subagent sidechain. The parent session file
  (`<session-uuid>.jsonl`, 12 records) contains an assistant `tool_use`
  with `name: "Agent"` (the CLI still accepts `--allowedTools Task`; the
  logged tool name is `Agent`) and its `tool_result`, whose
  `toolUseResult.agentId` is a join key back to the sidechain transcript.
  The sidechain transcript
  (`<session-uuid>/subagents/agent-<agentId>.jsonl`, 6 records) carries the
  *same* `sessionId` as the parent, `isSidechain: true` on every record,
  and a top-level `agentId`. Its first record's `parentUuid` is `null` —
  there is no inline UUID link back to the parent within the `.jsonl`
  transcripts alone; from the transcripts, the join is by
  `toolUseResult.agentId` (also mirrored in the subagent filename) and,
  more weakly, by the shared `promptId` between the `Agent` `tool_result`
  and the sidechain root.
  A third file sits alongside the transcript:
  `subagents/agent-<agentId>.meta.json`, a small non-JSONL sidecar
  (`{"agentType":"general-purpose","description":"Run echo command","toolUseId":"toolu_…","spawnDepth":1}`).
  Its `toolUseId` is a *direct* pointer to the parent's `Agent` `tool_use`
  id (verified to match exactly) — a stronger, one-step join than deriving
  the link from the transcripts. `scrub.Tree` copies it through verbatim
  (it isn't line-oriented JSONL, so it is not rewritten by `Line`), and
  both `just scrub-fixture` and `TestFixturesAreScrubbed` still
  findings-check it as a whole file. Whether the ticket #6 `claudesource`
  adapter reads this sidecar or derives the link from the transcripts
  alone is left open for that ticket; see
  `docs/technical/tdd-mvp.md`'s open question 1.
  See `TestSidechainFixtureExercisesSubagents` in
  `logfixture_test.go` for the pinned invariants, and
  `docs/technical/tdd-mvp.md`'s Claude mapping section / open question 1
  for how this shapes the MVP's flattening decision.
- **Regenerate:**
  ```bash
  WORK=$(mktemp -d)
  mkdir -p "$WORK/home" "$WORK/proj"
  cd "$WORK/proj"
  git init -q -b main . && git config user.email fixture@example.com && git config user.name fixture
  git commit -q --allow-empty -m init            # gives records a real gitBranch: "main"

  # Unset only the session-identity vars — a bare `env -i` breaks auth,
  # which arrives via CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR / ANTHROPIC_BASE_URL.
  # Unsetting CLAUDE_CODE_SESSION_ID is what stops the child run inheriting
  # the orchestrator's session id.
  env -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_ENTRYPOINT \
      -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_REMOTE_SESSION_ID \
    HOME="$WORK/home" claude -p \
    "Use the Task tool to launch one general-purpose subagent whose prompt is: run the bash command 'echo hello from subagent' and report its output. After the subagent finishes, reply with exactly: done" \
    --allowedTools Task,Bash --max-turns 8 < /dev/null
  ```
  Then scrub into the tree (from the repo root):
  ```bash
  just scrub-fixture -src "$WORK/home/.claude/projects/$(ls "$WORK/home/.claude/projects")" \
    -dst internal/testing/logfixture/claude/projects \
    -map "$WORK/proj=/home/user/fixture-sidechain" -branch main
  ```
  Use a `mktemp -d` template with no `.` in it (e.g.
  `mktemp -d -p "$SCRATCH" fixsidechainXXXXXX`) — the default
  `tmp.XXXXXXXXXX` template's dot gets encoded into the project directory
  name by Claude itself, and `scrub.Tree`'s path-renaming only rewrites
  `/` (per its documented contract), so a dot in the source path will not
  line up with the `-map` value and the directory will not be renamed.

### `claude/projects/-home-user-fixture-tool-error/`

- **Vendor version:** Claude Code v2.1.231.
- **Generated:** 2026-08-13, in scratch space, never against a real
  `~/.claude`.
- **Exercises:** a failed tool call. One `tool_result` record has
  `is_error: true`, its `content` is a plain string (not a block array),
  and `toolUseResult` at the top level is itself a **string**
  (`"Error: …"`), not an object. This is one of three shapes
  `toolUseResult` can take — object (top-level successful call), string
  (this fixture), or absent entirely (a successful call recorded inside a
  sidechain transcript; see the sidechain fixture above and
  `docs/technical/tdd-mvp.md`). See
  `TestToolErrorFixtureExercisesFailedCall` in `logfixture_test.go`.
- **Regenerate:**
  ```bash
  WORK=$(mktemp -d)
  mkdir -p "$WORK/home" "$WORK/proj"
  cd "$WORK/proj"
  git init -q -b main . && git config user.email fixture@example.com && git config user.name fixture
  git commit -q --allow-empty -m init

  env -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_ENTRYPOINT \
      -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_REMOTE_SESSION_ID \
    HOME="$WORK/home" claude -p \
    "Use the Bash tool to run exactly this command once: cat /nonexistent-fixture-file. It will fail; do not retry or investigate. Then reply with exactly: done" \
    --allowedTools Bash --max-turns 6 < /dev/null
  ```
  ```bash
  just scrub-fixture -src "$WORK/home/.claude/projects/$(ls "$WORK/home/.claude/projects")" \
    -dst internal/testing/logfixture/claude/projects \
    -map "$WORK/proj=/home/user/fixture-tool-error" -branch main
  ```

### `codex/`

- **Vendor version:** Codex CLI v0.147.0, generated with
  `codex exec --skip-git-repo-check "say hello"` **without OpenAI
  credentials**: the API call fails, but the rollout is real and contains
  `session_meta`, `event_msg`, `turn_context`, `world_state`, and
  user/developer `response_item` message records.
- Predates `just scrub-fixture`; see "Known gaps" below.

## Known gaps

- The Codex fixture has **no assistant `message`, `function_call`, or
  `function_call_output` records** (no credentials → no model turns). The
  Codex adapter's tool-call mapping needs a real authenticated session,
  scrubbed and added here, before it can be considered fixture-verified.
  Tracked by a deferred Codex ticket, not this one.
- The single-turn fixture
  (`claude/projects/-tmp-claude-0-…-scratchpad-fixture-project/`) predates
  `just scrub-fixture` and was hand-scrubbed, not tool-scrubbed — see its
  provenance note above. This is a documented exception to "never
  hand-edit"; a regeneration should go through `just scrub-fixture` like
  the two fixtures below it.

## Regenerating a fixture

Use `just scrub-fixture -src <real session file-or-dir> -dst <fixture dir> [-map OLD=NEW ...] [-branch NAME] [-dry-run] [-force]`
(a thin CLI over `internal/testing/logfixture/scrub`, see
`internal/tools/scrubfixture/main.go`). It:

- substitutes on raw bytes, never re-marshals JSON, so output stays a
  verbatim, byte-for-byte match except where a rule matched;
- redacts emails, common API-token/PAT shapes, and foreign home
  directories (`/home/<other-user>`, `/Users/<user>`) by default. `/root`
  is deliberately **not** rewritten: it is the generic root home, identical
  on every machine, so it identifies nobody — and rewriting it would
  corrupt real vendor paths that legitimately cite it (e.g. Codex's own
  `/root/.codex/skills/...` listing);
- rewrites `"gitBranch":"…"` to `-branch`'s value (default `main`; pass
  `-branch ""` to leave it alone);
- applies any `-map OLD=NEW` literal replacements first (longest `Old`
  first), including to output paths in Claude's own encoded form
  (`/` → `-`) — so an encoded-cwd project directory gets renamed to match
  the scrubbed cwd;
- copies non-`.jsonl` files (e.g. a subagent's `.meta.json` sidecar)
  through verbatim rather than rewriting them line-by-line, but still
  findings-checks their full contents before exiting 0;
- re-scans every file it wrote this run afterward and **fails (non-zero
  exit) if anything still matches a redaction rule**, unless run with
  `-force`;
- `-dry-run` scrubs into a throwaway scratch directory instead of `-dst`
  — useful for checking whether a set of `-map` rules leaves any findings
  before committing anything — and reports/verifies exactly as a real run
  would, but never touches `-dst` (which may not even exist yet).

**Always generate against a throwaway `HOME` in scratch space, never
against a real `~/.claude` or `~/.codex`** — those stay strictly read-only.
The tool independently refuses to run at all when `-src` and `-dst` are
nested inside one another in either direction (including through a
symlink), so it can never write onto the source tree it was told about —
but that guard is a backstop, not a reason to point `-src` at a real
vendor directory in the first place.

**Scrubbing is best-effort, not a guarantee.** Tool results can embed
arbitrary file contents the redaction rules were never designed to catch.
Always review the diff of what `just scrub-fixture` writes before
committing it, even though the tool itself will refuse (non-zero exit,
unless `-force`) to leave known-sensitive patterns in its output.

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
