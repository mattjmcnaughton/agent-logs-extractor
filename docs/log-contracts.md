# Local log contract checks for maintainers

This is an internal repository maintenance workflow requiring the source checkout
and Go/just test toolchain. For importing, exporting, or querying history, use
the public [agent-logs-extractor skill](../skills/agent-logs-extractor/SKILL.md).

Vendor formats are undocumented. We maintain explicit mapping assumptions and
synthetic regression tests, then check those assumptions against live examples
locally. **Live checks are opt-in and local-only. They never run in CI or in
`just gate` / `just gate-expensive`.** CI tests the checker itself using invented
data; it does not establish current vendor compatibility.

## Run

From the repository root, select the vendor directories explicitly:

```sh
ALX_CONTRACT_CLAUDE_PATH=/explicit/claude/root just test-contract
ALX_CONTRACT_CODEX_PATH=/explicit/codex/root just test-contract
```

Set both variables to check both. Claude roots contain `projects/`; Codex roots
contain `sessions/` and/or `archived_sessions/`. There is no HOME or product-config
fallback. Unset paths, missing/empty trees, and any set `CI` variable cause a
skip. A skip provides no evidence. Checks use `-count=1` to avoid cached results.

The internal maintainer skill `tests/skills/validate-log-contracts/SKILL.md` guides this
workflow. Use it after vendor updates, before changing mappings, or before
adding another vendor such as Pi or OpenCode.

## Assumptions and evidence

| Assumption | Checker evidence |
| --- | --- |
| Unified documents have valid roles, sequences, IDs, JSON and tool/message joins | Structural invariants over every parsed document; cross-session ID uniqueness over the winning session set |
| Claude conversational records have `uuid`, a message/content envelope and optional session metadata | Recognizes extractable user/assistant/system records independently; checks their original bytes appear in normalized messages. Missing IDs/content are reported for review |
| Claude `tool_use` and `tool_result` join by ID | Counts source tool uses and checks normalized calls and paired result status |
| Claude child transcripts belong to the parent | Reads adjacent `subagents/*.jsonl`, checks their messages survive in the parent's document; reports sidechain coverage |
| Codex `response_item` messages and both call families are retained | Checks original message/call bytes and corresponding normalized tool calls |
| Codex paired outputs resolve by `call_id` | Compares paired result success/error status with the normalized call; duplicate calls and usable outputs keep the first occurrence, with skipped records still reported for review |
| Codex mirrored events and inherited ordinal prefixes are excluded | Checks those raw lines are not emitted as messages; reports coverage |
| Active and archived Codex rollouts are discoverable | Reports files returned by the adapter, including archive coverage |
| Unknown shapes require review | Reports skipped records, unknown content blocks, mixed Claude result/text blocks, records lacking extractable content and structural drift |

Mapping details remain in `docs/technical/tdd-mvp.md`. The checker is a focused
compatibility probe, not a second full parser or an exhaustive vendor schema.
It does not independently prove discovery completeness, exact text/output
normalization, Claude sidecar-parent linkage, or every metadata field. Synthetic
adapter tests specify those behaviors. It also cannot prove compatibility for
versions or shapes absent from the selected logs. Duplicate IDs, incomplete
sessions and logs being appended during a run can require local investigation.
Prefer a stable, completed session tree when investigating a failure.

Only Claude and Codex have checker support. Other vendors fail explicitly before
source discovery; adding an adapter also requires adding its contract checks.

## Interpret the report

- `OBSERVED`: the run encountered this shape. Consult failures and drift before
  treating it as verified compatibility.
- `NOT_OBSERVED`: this assumption remains unverified by the selected examples.
- `REVIEW`: drift or skipped data needing local investigation. The Go test can
  pass while printing `PARTIAL`; do not turn that into an unconditional claim.
- `FAIL`: source/output mismatch, broken invariant, I/O error, panic, or total
  ingestion loss. The test exits unsuccessfully.

When investigating, inspect only the necessary structure locally. Add an
invented minimal example demonstrating the shape, write a failing parser test,
update the mapping and rerun the synthetic tests plus the local check. Keep
unobserved assumptions explicit; do not silently broaden support claims.

## Data boundary

The checker reads the selected source directly and keeps data in memory. It
never copies logs into the repository, creates canonical exports, or saves
normalized documents. Vendor loggers are discarded. Its report contains only
aggregate counts and fixed diagnostic labels: no paths, IDs, prompts, arguments,
results, raw records, exception details, or observation examples. Go still writes
its ordinary build/test cache and temporary build artifacts.

Do not commit live logs, metadata sidecars, captured JSON goldens, or copied
snippets. The former captured corpus and scrubbing tools have been removed;
`internal/testing/testlogs` generates invented records in temporary directories.
Deleting repository files does not remove their contents from older Git commits.
