---
name: validate-log-contracts
description: Internal maintainer skill for developing agent-logs-extractor adapters. Check format assumptions against explicitly selected local Claude or Codex logs, interpret contract drift, and update synthetic tests without capturing private transcripts. Use for repository contract testing, not importing, exporting, or querying a user's history.
---

# Internal log contract validation

Audience: maintainers working in an agent-logs-extractor source checkout.
Use this repository's local contract checker to verify vendor-format assumptions.
For operating the CLI or answering questions about conversation history, use the
public `agent-logs-extractor` skill instead.
Read `docs/log-contracts.md` for the supported checks and their limits, and the
relevant vendor mapping in `docs/technical/tdd-mvp.md`.

Use a source root already selected by the user in this task. Otherwise ask which
vendor and local root to check. Do not infer a HOME path. Live checks run only
locally; never add them to CI or gates, and do not unset `CI` to bypass the guard.

Run the appropriate command from the repository root, quoting the supplied path:

```sh
ALX_CONTRACT_CLAUDE_PATH='/explicit/claude/root' just test-contract
ALX_CONTRACT_CODEX_PATH='/explicit/codex/root' just test-contract
```

Report observed coverage, `NOT_OBSERVED` assumptions, `REVIEW` drift, and failures
separately. A skip or passing test with `PARTIAL` does not establish compatibility.
The checker does not prove every mapping; compare its coverage with the specific
assumption under investigation before drawing a conclusion.

Investigate failures locally using only the needed structural information.
Do not print or save prompts, tool arguments/results, raw records, IDs, paths,
or exception details in reports. Never copy live files or snippets into tests,
reports, commits, issues or PRs. No redaction or capture workflow is needed.
If a mapping needs changing, construct a minimal invented example, add a failing
test, fix the adapter, and rerun both regression tests and the local check.
Preserve uncertainty for shapes not observed. Pi and OpenCode require their own
mapping and source adapter before this checker can establish compatibility.
