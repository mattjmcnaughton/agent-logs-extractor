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
7. **Lenient, lossless parsing.** Unknown record types and malformed lines are counted and skipped, never fatal; every normalized row keeps the vendor record verbatim in a `raw` column; sessions carry the vendor version that wrote them, as a drift-debugging aid. **Invariant (settled post-review, #6):** a `SessionDoc` a source adapter returns has unique `message_id` and unique `tool_call_id` across its `messages`/`tool_calls` rows — both are primary keys in the schema below, so a vendor log that itself contains a duplicate id (a repeated `uuid` or `tool_use_id`) must have its first occurrence win and every later duplicate skipped-and-counted, the same lenient contract as an unknown record type, never emitted as a colliding row. Downstream consumers (the canonical store, #7; the export, #9) may rely on this without re-deduping.
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

- Layout: one directory per project (encoded cwd), containing `<session-uuid>.jsonl`. A session that spawns subagents also writes `<session-uuid>/subagents/agent-<agentId>.jsonl` alongside `<session-uuid>.jsonl` — the sidechain transcript. Those files carry the *same* `sessionId` as the parent, so the source adapter must enumerate only top-level `*.jsonl` as sessions and treat `subagents/` as belonging to the parent, never as a second session with a duplicate `sessionId`. See `internal/testing/logfixture/claude/projects/-home-user-fixture-sidechain/`.
- Conversational records (`type` ∈ `user`, `assistant`, `system`) carry `uuid`, `parentUuid`, `sessionId`, `timestamp`, `cwd`, `gitBranch`, `version`, `isSidechain`, and an API-shaped `message` with a content-block array. Additional bookkeeping types observed (`queue-operation`, `attachment`, `last-prompt`, `mode`, `summary`) are skipped.
- Mapping: `tool_use` blocks in assistant messages → `tool_calls` rows (`id`, `name`, `input`); `tool_result` blocks in subsequent user-type records are joined back by `tool_use_id` to fill `output`/`status`, and do **not** produce `messages` rows; text blocks concatenate into `messages.text`. Corrections from the sidechain and tool-error fixtures (Claude Code v2.1.231):
  - `caller` is `{"type":"direct"}` on every observed `tool_use` block, including inside sidechains — it does **not** distinguish direct calls from subagent calls, contrary to an earlier version of this document. Subagent attribution instead comes from file location (`subagents/`), the sidechain record's `agentId`/`attributionAgent`, and the parent's `toolUseResult.agentId`.
  - The top-level `toolUseResult` field is **polymorphic** across three shapes, not simply "richer": an object on a top-level successful call; a plain string (`"Error: …"`) when the matching `tool_result` has `is_error: true`; and **absent entirely** on a successful call recorded inside a `subagents/` sidechain transcript (observed on the sidechain's own Bash `tool_result` — the record has no `toolUseResult` key at all, not even `null`). An adapter that types it as an object unconditionally will fail on error records and on every successful in-sidechain tool call. See `internal/testing/logfixture/claude/projects/-home-user-fixture-tool-error/` for the string case and `internal/testing/logfixture/claude/projects/-home-user-fixture-sidechain/.../subagents/agent-a0fb4161db4e2c307.jsonl` for the absent case (pinned by `TestSidechainFixtureExercisesSubagents` in `internal/testing/logfixture/logfixture_test.go`).
  - `tool_result.content` may be a string (the common case, and always on error) or a list of content blocks (observed on the subagent-spawning tool's result).
  - The subagent-spawning tool is recorded as `name: "Agent"`, not `"Task"` — the CLI flag is still `--allowedTools Task`, but the logged `tool_use.name` is `Agent`. An adapter keyed on `tool_name == "Task"` would silently miss every subagent spawn.
- Settled by `internal/adapters/claudesource` (#6), the source adapter fixture-tested against every committed Claude fixture (the package's `*_test.go` files — `golden_test.go`, `parse_test.go`, `sidechain_test.go`, `sidechain_multi_test.go`, `content_test.go`, `regression_test.go` — goldens under `testdata/`):
  - **`session_id`:** the first non-empty `sessionId` across records, parent transcript first; if no record supplies one, the filename stem (`<session-uuid>` from `<session-uuid>.jsonl`) is the fallback.
  - **Session metadata (`project_path`/`git_branch`/`vendor_version`) is first-non-empty-*per-field*, not "read off the first record"** — every committed fixture opens with a bookkeeping `queue-operation` record that carries none of these fields, so the literal "first record" reading yields nothing. `project_name` is `path.Base(project_path)` (POSIX `path`, not the host's `filepath` — the vendor always writes slash-separated cwd paths).
  - **Text blocks join with `"\n"`; `thinking` blocks contribute nothing to `messages.text`** (they stay available in `raw` only, never dropped from the record).
  - **`toolUseResult` is never read for `tool_calls.output`/`status`**, in any of its three shapes (object/string/absent) — only the `tool_result` block's own `content` and `is_error` are, since that shape is uniform across all three cases. `toolUseResult` is decoded opportunistically in exactly one place: the `agentId`-join sidechain-link fallback below, where a failed decode (the string/absent shapes) is simply ignored.
  - **`status`:** `ok` unless the joined `tool_result` has `is_error: true` (→ `error`); a `tool_use` that never receives a `tool_result` keeps `pending`.
  - **Skip taxonomy:** the three reasons this document already names (`malformed_line`, `unknown_record_type`, `bookkeeping_record`) plus six adapter-local ones — `missing_message` (no `message` and, for `type: "system"`, no top-level `content` either), `missing_uuid` (no `uuid` to key a `messages` row on), `orphan_tool_result` (a `tool_result` block whose `tool_use_id` is empty, or matches no earlier `tool_use`), `missing_tool_use_id` (a `tool_use` block with an empty `id`), `duplicate_message_id` (a record whose `uuid` collides with one already turned into a `Message` this session), `duplicate_tool_use_id` (a `tool_use` block whose `id` collides with one already turned into a `ToolCall` this session).

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

**Settled by `internal/adapters/jsonlstore` (#7):**

- **Filename mapping.** A session's file is `<vendor>/<stem>.json`, where `<stem>` is the session id with its exact `"<vendor>:"` prefix stripped, then every byte outside `[A-Za-z0-9._-]` percent-escaped (`%XX`, uppercase hex) into one path element. `Put` **rejects** (`jsonlstore.ErrInvalidSessionDoc`, which wraps `ports.ErrInvalidSessionDoc`) a doc whose `Session.SessionID` does not carry that exact vendor prefix, whose vendor/id is empty, or whose escaped vendor element is `"."` or `".."` (a traversal element, rejected outright rather than written as a directory name) — it does not fall back to escaping the whole id. Escaping every byte, not just `:`, is load-bearing: mapping `:` → `_` is not injective (`claude:a_b` and `claude:a:b` would collide), and a session id that falls back to a source *filename stem* (e.g. `../../etc/passwd`) must never be able to write outside the store. An id whose escaped stem exceeds 200 bytes is truncated to 160 bytes plus `%2D` plus a 16-hex-char sha256 suffix of the full id. The `%2D` separator (rather than a literal `-`) is load-bearing, not cosmetic: `-` is itself unreserved, so a short id can pass straight through escaping, and with a literal `-` separator a short id can land exactly on a long id's truncated+hashed stem (concretely, `claude:` + 250×`a` hashes to the same bytes as the literal escaped stem of `claude:` + 160×`a` + `-8a9d854c5de25ce2`), silently overwriting it. `%2D` can never appear in an escaped stem — `-` always passes through unescaped, and a literal `%2D` in the input escapes its `%` first, yielding `%252D` — so the hashed form is now unreachable by any pass-through id.
- **File format.** One session doc per file: a single compact JSON object, HTML-escaping disabled (`json.Encoder.SetEscapeHTML(false)`), terminated by exactly one trailing newline. This makes every file, incidentally, a valid one-record newline-delimited-JSON file that `read_json` (or auto-detection) can read directly — the "JSONL, not a database" of core decision 3 is about engine-agnostic line-oriented text versus a DB, not a promise that every file holds multiple records.
- **Staging and swap.** A rebuild stages into a sibling `<root>/.staging-<rand>/` (created by the same `BeginRebuild` that also sweeps any `.staging-*`/`.trash-*` leftovers from a previous interrupted rebuild). `Commit` performs two renames — live `sessions/` aside to `.trash-<rand>`, then `.staging-<rand>` into `sessions/` — and best-effort deletes the trash directory afterward. **Guarantee, stated precisely:** a rebuild that returns an error from `Put` or `Commit`, or any `Discard`, leaves the previously committed generation exactly as it was — nothing under `sessions/` is touched until both renames in `Commit` have succeeded, and `Commit` refuses to even attempt the swap once any `Put` on that rebuild has already failed (`rebuild.firstErr`), so a generation known to be missing a doc can never overwrite a good one. **The one exception:** if the swap rename fails and the rollback rename that tries to restore the previous generation *also* fails, `sessions/` is left absent rather than restored; `Commit` best-effort relocates the previous generation from `.trash-<rand>` to `.orphan-<rand>` (a prefix the leftover sweep never touches, unlike `.trash-*`) and names that path in the returned error, so the recovery pointer survives a subsequent `sync` instead of expiring on the next sweep. **Not covered:** crash-atomicity. A process killed between the two renames inside `Commit` can leave `sessions/` absent, with the previous generation at `.trash-<rand>` and the new one at `.staging-<rand>`; the store is fully reconstructible by re-running `sync` (it derives entirely from read-only vendor logs), so this one-rename-wide window is an accepted cost of a portable, dependency-free swap rather than a platform-specific atomic rename or a symlink flip (no `afero.MemMapFs` support, privileged on Windows). There is no `fsync` and no concurrency locking: two rebuilds against the same root will interfere via the leftover sweep, so callers must serialize rebuilds against one root themselves.
- **`Put`-rejects-zero-doc contract for #8.** `Put` errors (`ErrInvalidSessionDoc`) on any doc `ports.ValidSessionDoc` reports invalid — not just an empty `Session.SessionID`, but the strictly larger set of empty vendor, non-namespaced id, bare-prefix id (`"<vendor>:"` with nothing after it), or a vendor that is itself a path-traversal element (`"."`/`".."`) — rather than inventing a filename. `sync` (#8) must pre-filter with **`ports.ValidSessionDoc(doc.Session)`** — the exact same predicate `jsonlstore.relPath` and `fakes.FakeStoreRebuild.Put` call, exported from `internal/ports` for this reason — **before** calling `Put`, and must not treat a doc it filters out as an error — the same lenient, counted-not-fatal contract as every other skip reason in core decision 7. Filtering with a narrower, hand-rolled check (e.g. "empty `SessionID`" alone) leaves a gap: a doc that is non-empty but outside the predicate's accepted set would sail through #8's filter, get rejected by `Put`, and poison the whole rebuild (see the next paragraph) — one bad doc freezing every subsequent sync at a stale generation until a human finds and removes the offending log file. **This leniency is bounded to parse-level records, not store writes.** Core decision 7's "never fatal" covers malformed lines, unknown record types, and other bookkeeping a *source* adapter skips while parsing — a record that fails to parse is data the tool never had. A doc that `Put` rejects or fails to write is data the tool *did* have and is now dropping, which is a different, worse failure: `rebuild.firstErr` makes exactly that case fatal to the sync *run* (Commit refuses to swap), because publishing a silently truncated store is worse than failing loudly and leaving the previous generation in place. So: skip a `ports.ValidSessionDoc`-invalid doc before calling `Put` and don't count it as an error (parse-level leniency, applied here at the naming boundary); but if `Put` is called anyway and fails — for that reason or any other, including a genuine I/O fault — treat the whole run as failed, not as one more counted skip. `Put`'s own rejection stays fatal as defense-in-depth even though #8 is expected to pre-filter with the same predicate — belt and braces, not an either/or.
- **`read_json`-empty-glob note for #9 (flagged, not fixed here).** A fresh or all-empty store legitimately commits a present-but-empty `sessions/` directory (`Root()` never dangles), but DuckDB's `read_json` errors on a glob that matches zero files — #9 must handle that case rather than let `export` fail on an empty store. Also for #9: each store file is a nested doc (`session`/`messages`/`tool_calls`), so reading it back needs `UNNEST` plus the explicit `columns={…}` schema `model.go` already mandates, not column auto-detection.

**Settled by `internal/core/sync` (#8):**

- **The loop, and where `defer r.Discard()` sits.** `Run` resolves every requested vendor to a registered source **before** calling `BeginRebuild` at all — a bad `--vendor` value must never leave a partially-built generation behind. Once `BeginRebuild` succeeds, `defer func() { _ = r.Discard() }()` is registered immediately, before the first `Put`, so every later return path (a `List`/`Put` error, a cancelled context, a `Commit` failure) unwinds through it; `Discard` is a no-op after a successful `Commit`, so the deferred call is always safe to leave in place. Then, per vendor: `List → Parse (per file) → filter → Put`, accumulating one `VendorSummary`; once every vendor has run cleanly, one `Commit` swaps the whole rebuild in.
- **Severity is a three-way split, not two — this overturns an earlier draft's plan to make `Parse` errors fatal.** (1) A malformed record or unrecognized record type *within* a file (`ParseStats.Skipped`) is data the tool read but chose not to turn into a row — counted, lenient, never fatal (core decision 7), same as always. (2) A `Parse` error — the file itself could not be read at all (`ports.ConversationSource.Parse`'s own doc) — is data the tool never had, not data it is dropping. This is *also* counted and lenient: warned at the call site, tallied into `VendorSummary.FilesUnreadable`, and the run continues past it. Treating this as fatal would be an indefensible asymmetry with `claudesource.List`, which already warns-and-continues past an unreadable project *directory*; it would also mean one permissions-denied file in a real `~/.claude` permanently freezes every future sync at a stale generation — reintroducing, by another path, exactly the "store frozen at a stale generation" failure #7's atomic rebuild exists to prevent. (3) A `List`, `Put`, or `Commit` failure is data the tool *had* and would be silently dropping (`Put`/`Commit`), or a failure to even enumerate what there is to ingest (`List`) — these abort the run and leave the previous store generation intact, exactly as jsonlstore's own `rebuild.firstErr` guarantees for `Put`/`Commit`.
- **The `ValidSessionDoc` pre-filter.** Before calling `Put`, `ingestVendor` skips (at warn, uncounted, not an error) any doc `ports.ValidSessionDoc` reports invalid, and any doc with zero messages, zero tool calls, and an empty session id (a file that parsed to nothing at all). This is the exact predicate `jsonlstore.relPath` and `fakes.FakeStoreRebuild.Put` also call — see #7's note above for why a narrower hand-rolled check would be a gap, not a simplification.
- **Session accounting is keyed by session id, not by file.** Two files that both parse to the same session id (three of the four pathological fixtures do) both reach `Put` — the later one wins, both in the store and in the printed counts — but must only ever be counted once. `ingestVendor` keeps a `map[string][2]int` of session id → `(messages, toolCalls)`, overwriting on collision, and derives `VendorSummary.Sessions`/`Messages`/`ToolCalls` from that map once the vendor's files are exhausted; counting per-`Put`-call instead would print numbers that don't reconcile against what the store actually holds.
- **An empty `Request.Sources` performs no rebuild at all — not even an empty `Commit`.** A full-rebuild `Commit` with zero `Put`s would wipe out a live store that the request simply wasn't asked to touch; `Run` returns `Summary{}, nil` immediately instead, without calling `BeginRebuild`.
- **The deferred-Codex availability rule.** `Sync` exposes `Vendors() []model.Vendor` — sorted, nil-receiver-safe — over its own registered-source map. The CLI's `selectedVendors` fans a bare `sync` out over whatever `Vendors()` reports rather than hardcoding both vendor names, so a build with only `claudesource` wired syncs Claude only and exits 0; `sync --vendor codex` fails loudly, naming what *is* available, instead of either silently no-op'ing or crashing on a nil map access. **TODO(#10):** once `codexsource` is registered in `main.go`, this rule needs no change at all — `Vendors()` picks it up automatically.
- **The printed summary format.** One line per vendor, in request order, to stdout: `claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped`, singular nouns where a count is exactly 1, with `, N file(s) unreadable` appended only when `FilesUnreadable` is non-zero (so the common, fully-readable case stays a clean four-field line). The full by-reason skip breakdown is not printed — `Run` logs it at debug level, one line per reason, with `SkipCounts`' map keys sorted first so the log output is deterministic despite Go's randomized map iteration.
- **Idempotency.** Two consecutive `sync` runs over unchanged sources produce byte-identical summaries and a byte-identical `sessions/` tree, with no leftover `.staging-*`/`.trash-*`/`.orphan-*`. This holds without any special-casing in `sync` itself, resting entirely on properties #6 and #7 already guarantee: `claudesource.List` ends in `slices.Sort` and `Parse` uses a stable sort on a total order key, so file and record iteration order is deterministic; `jsonlstore.encodeDoc` marshals a struct (no maps) via `json.Marshal`, so the on-disk bytes for a given doc are stable; `jsonlstore`'s `Put` path is a pure function of the session id, so the same doc always lands at the same path; and staging directory names, though random, never appear in a committed path. The only nondeterminism left is `SkipCounts` map iteration order, which affects debug log *order* only, never the summary or the store.
- **Open decision, must resolve before `codexsource` (#10) ships: `--vendor` vs. full rebuild.** `sync --vendor claude` is a full rebuild scoped to *reading* claude sources only, but `Commit` still swaps in the *entire* pending generation — so it silently erases any other vendor's sessions the store held. Unreachable today (only `claudesource` is registered, so there is nothing else in the store for `--vendor claude` to erase), but it stops being unreachable the moment a second adapter exists. Three options, undecided:
  1. **Document only** (the MVP's current answer — see `README.md`'s `sync` section): a bare `sync` is the supported way to keep every vendor's sessions together; `--vendor` is an intentionally destructive single-vendor rebuild.
  2. **Warn** when `--vendor` is passed and the store already holds sessions for a vendor not in the request, before committing.
  3. **Scope the rebuild per vendor subtree** (`sessions/<vendor>/` swapped independently, rather than the whole `sessions/` tree) — the likely right answer, since it makes `--vendor claude` additive instead of destructive, but it changes `jsonlstore`'s single-swap contract (#7) and can't be exercised or tested until a second `ConversationSource` actually exists to prove the per-vendor boundary against.

## Export path

`export duckdb` shells out to the `duckdb` CLI: a generated script builds the three relations from the store via `read_json`, applies the denormalization joins, and `COPY`s each into a fresh snapshot `.duckdb` file (written to a temp path, then renamed over the target). The export is a full snapshot each run, matching the full-rebuild sync.

## Ports and adapters

| Port | Purpose | MVP adapter | Fake |
|---|---|---|---|
| `ConversationSource` | enumerate session files for a vendor; parse one file → normalized session doc | `claudesource`, `codexsource` | canned-sessions source |
| `CanonicalStore` | read/write store docs; atomic swap | `jsonlstore` (afero) — landed #7, repo's first third-party dependency | in-memory store |
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

1. ~~Claude Code sidechains (subagent conversations, flagged by `isSidechain` and `tool_use.caller`): flatten into the parent session, or model as child sessions? MVP: flatten, keep `parentUuid` linkage in `parent_message_id`; revisit with real fixtures.~~ **Settled and closed by #6.** Real fixture evidence (`internal/testing/logfixture/claude/projects/-home-user-fixture-sidechain/`, ticket #5) confirms **flattening into the parent session** as the MVP approach:
   - Modelling a sidechain as a *child session* would collide on the `sessions` primary key — the sidechain transcript carries the exact same `sessionId` as its parent, not a distinct one.
   - `tool_use.caller` cannot drive the flatten/split decision either way: it is `{"type":"direct"}` on every observed block, in the parent file and inside the sidechain alike, so it carries no sidechain signal (see the Claude mapping corrections above).
   - Concretely: subagent messages append to the parent session as ordinary `messages` rows. The sidechain root's own `parentUuid` is `null` (no inline UUID link back to the parent), so its `parent_message_id` cannot be read off `parentUuid` the way an in-file record's can. Two independent joins back to the `Agent` `tool_use` message the sidechain answers are available, from the *transcripts alone* and from a committed sidecar:
     - **Primary (sidecar):** the subagent's own `subagents/agent-<agentId>.meta.json` file (copied verbatim by `scrub.Tree`, not scrubbed line-by-line — see `internal/testing/logfixture/README.md`) carries `toolUseId`, a *direct* pointer to the parent's `Agent` `tool_use` id — verified to match exactly in the sidechain fixture. No join/search required if the adapter reads this file.
     - **Fallback (transcripts only):** join `toolUseResult.agentId` (on the parent's `Agent` `tool_result` record) to the subagent file's own `agentId` (and filename suffix) — the only option if `.meta.json` is unavailable or the adapter chooses not to depend on it.
   - This linkage is pinned by `TestSidechainFixtureExercisesSubagents` in `internal/testing/logfixture/logfixture_test.go` (which now also asserts the `.meta.json` `toolUseId` join where the sidecar is present), so ticket #6's `claudesource` adapter has a fixture-backed contract to implement and prove the mapping against, rather than re-deriving it from scratch.
   - **Settled by #6:** `internal/adapters/claudesource` reads `.meta.json` as the **primary** link (its `toolUseId` is a direct pointer to the parent's spawning `Agent` `tool_use` id), falls back to the `toolUseResult.agentId` join against the parent's records when the sidecar is missing/unreadable/malformed, and — if both are unresolved — leaves `parent_message_id` empty. None of the three outcomes is an error; an unresolved link is logged at debug only. Both link paths are fixture-tested: `TestSidechainOrderAndParentLink` pins the primary (sidecar) path against the committed fixture, and `TestSidechainLinkFallbacks` pins both fallback tiers against a mutated copy of it.
   - **Merge ordering, also settled by #6 (narrowed post-review):** flattened records — the parent transcript plus every `subagents/*.jsonl` transcript — are merged **chronologically across files, but a file's own line order is authoritative for records within it**: each record's sort key is clamped to be monotonically non-decreasing against its predecessor *within the same file* before the cross-file merge runs, so a within-file timestamp inversion (observed live in the committed sidechain fixture — an `attachment` record stamped 2ms before the `user` record that precedes it) can never permute two records from the same file against each other. The merge itself is then a stable sort on `(order, fileRank, lineIndex)`, with the parent transcript winning ties and an untimestamped record carrying its predecessor's clamped order forward. This reproduces true reading order without depending on the parent link resolving, and generalizes to a `run_in_background` agent whose records interleave with everything the parent did while it ran — a "splice in right after the spawning message" rule would not.
   - A subagent transcript is folded entirely into its parent's `SessionDoc` and never gets a `source_path`/session row of its own.
2. Message-level project override: Claude records `cwd` per record, and a session's cwd can change mid-conversation. MVP: session-level `project_path` from the first record; flag drift in the contract tests if it occurs in practice.
