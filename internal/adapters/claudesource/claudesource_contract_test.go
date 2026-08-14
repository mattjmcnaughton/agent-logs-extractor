//go:build contract

// Package claudesource_test's contract file is the opt-in tier that parses
// a live ~/.claude (or a fixture tree standing in for one) and asserts the
// structural invariants every SessionDoc must satisfy, then reports drift
// observations — the early-warning system for vendor format drift
// (docs/technical/tdd-mvp.md core decision 8). It is read-only end to end:
// only Source.List and Source.Parse are ever called here, never a
// CanonicalStore, so this tier can never write anything, anywhere.
//
// It skips (never fails) when there is nothing to read: ALX_CONTRACT_CLAUDE_PATH
// unset, no root, no root/projects, or zero session files listed. The first
// of those is deliberate and mechanical (A5): this tier never defaults to
// AGENT_LOGS_EXTRACTOR_HOME or the real $HOME/.claude, so a contributor with
// no live Claude Code history, or CI (which never sets
// ALX_CONTRACT_CLAUDE_PATH), sees this tier skip cleanly rather than
// silently reading whatever ~/.claude happens to exist. It does, however,
// FAIL if the resolved root does yield session files but ingests zero
// sessions (or sessions but zero messages) — total ingestion loss is never
// a silent zero-row pass. See docs/development.md for how to point it at a
// real or fixture tree, and why `just test-contract` must never be run
// bare against a sandbox's own harness transcript.
package claudesource_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/invariants"
)

// contractPathEnv is a TEST-ONLY override for the root this tier reads,
// letting both the skip path and the found-logs path be validated without
// a developer's real ~/.claude — see docs/development.md's "Contract
// tier" section. It is never read by the CLI or by wiring, must never be
// documented in README.md, and must never be exposed as a flag.
const contractPathEnv = "ALX_CONTRACT_CLAUDE_PATH"

// TestClaudeSourceContractAgainstLiveLogs is the ticket's contract test:
// list and parse every session file under the resolved root, check every
// resulting SessionDoc against invariants.Check (hard failures) and
// invariants.Observe (drift signals, never fatal), and report a summary via
// t.Log. Run with `just test-contract` (go test -tags=contract -v
// -count=1 ./...); DO NOT run it against this sandbox's own ~/.claude
// (see the package doc and docs/development.md).
func TestClaudeSourceContractAgainstLiveLogs(t *testing.T) {
	root, source := resolveContractRoot(t)

	if _, err := os.Stat(root); err != nil {
		t.Skipf("claude contract: root %s (resolved from %s) does not exist; set %s to point at a real or fixture ~/.claude to run this tier (%v)", root, source, contractPathEnv, err)
	}
	projectsDir := filepath.Join(root, "projects")
	if _, err := os.Stat(projectsDir); err != nil {
		t.Skipf("claude contract: %s (root resolved from %s) does not exist; set %s to point at a real or fixture ~/.claude to run this tier (%v)", projectsDir, source, contractPathEnv, err)
	}

	log := slog.New(slog.DiscardHandler)
	src := claudesource.New(log)
	ctx := context.Background()

	paths, err := src.List(ctx, root)
	if err != nil {
		t.Fatalf("List(%s): %v", root, err)
	}
	if len(paths) == 0 {
		t.Skipf("claude contract: %s (resolved from %s) has a projects/ directory but List found no session files; set %s to point at a tree with real sessions", root, source, contractPathEnv)
	}

	run := newContractRun()
	for _, p := range paths {
		run.parseFile(t, ctx, src, p)
	}

	merged := invariants.MergeObservations(run.observationSets...)

	t.Logf("claude contract: root=%s (resolved from %s), files scanned=%d, files unreadable=%d, sessions=%d, messages=%d, tool_calls=%d",
		root, source, len(paths), len(run.unreadable), run.sessions, run.messages, run.toolCalls)
	for _, o := range merged {
		t.Logf("claude contract: observation %s: count=%d examples=%v", o.Kind, o.Count, o.Examples)
	}

	for _, v := range run.violations {
		t.Errorf("claude contract: violation %s: %s", v.Kind, v.Detail)
	}

	// A4: Observe being never-fatal is settled (docs/technical/tdd-mvp.md
	// core decision 8) and stays that way — but total ingestion loss is not
	// a judgment call. A vendor-format change that silently drops every
	// record (a renamed "uuid" field, a renamed "assistant" type literal,
	// ...) must fail this tier, not report sessions=0/messages=0 and exit
	// 0 as if there were simply nothing to find.
	if len(paths) > 0 && run.sessions == 0 {
		t.Errorf("claude contract: %d session file(s) scanned but 0 sessions ingested — total ingestion loss (vendor format drift?) must fail this tier, not pass silently", len(paths))
	}
	if run.sessions > 0 && run.messages == 0 {
		t.Errorf("claude contract: %d session(s) ingested but 0 messages — total message loss (vendor format drift?) must fail this tier, not pass silently", run.sessions)
	}
}

// contractRun accumulates one contract-tier run's tallies and findings
// across every session file parsed. Replaces what was previously six
// separate pointer parameters (*[]Violation, *[][]Observation, *[]string,
// three *int) threaded through a free function — T1: the caller already
// declared this exact state as one `var (...)` block, so making it a
// struct with a parseFile method collapses the parameter list to one line
// and turns every `*x++`/`*x = append(...)` back into a plain field
// access.
type contractRun struct {
	violations       []invariants.Violation
	observationSets  [][]invariants.Observation
	unreadable       []string
	sessions         int
	messages         int
	toolCalls        int
	seenSessionFiles map[string]string // session id -> first file that produced it
}

func newContractRun() *contractRun {
	return &contractRun{seenSessionFiles: map[string]string{}}
}

// parseFile parses one session file under a per-file defer/recover, so a
// panic anywhere in Parse is caught, reported with the offending path, and
// does not abort the rest of the run — the contract tier's own "no panic"
// invariant is enforced by this harness, not by invariants.Check (a pure
// function over an already-built SessionDoc has nothing left to panic on).
func (r *contractRun) parseFile(t *testing.T, ctx context.Context, src *claudesource.Source, path string) {
	t.Helper()
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("claude contract: PANIC parsing %s: %v", path, rec)
		}
	}()

	doc, stats, err := src.Parse(ctx, path)
	if err != nil {
		// A Parse error means the file itself could not be read — counted,
		// exactly like sync.go's own FilesUnreadable accounting, never
		// fatal to the run.
		t.Logf("claude contract: %s could not be read; skipping (counted, not fatal): %v", path, err)
		r.unreadable = append(r.unreadable, path)
		return
	}

	for _, v := range invariants.Check(doc) {
		v.Detail = fmt.Sprintf("%s: %s", path, v.Detail)
		r.violations = append(r.violations, v)
	}
	r.observationSets = append(r.observationSets, invariants.Observe(doc))

	if len(stats.Skipped) > 0 {
		skipObs := make([]invariants.Observation, 0, len(stats.Skipped))
		for reason, n := range stats.Skipped {
			skipObs = append(skipObs, invariants.Observation{
				Kind:     string(reason),
				Count:    n,
				Examples: []string{path},
			})
		}
		r.observationSets = append(r.observationSets, skipObs)
	}

	if doc.Session.SessionID == "" {
		// A zero doc (nothing but bookkeeping records) — mirrors sync.go's
		// own "empty doc" leniency; nothing to tally or dedup-check.
		return
	}
	r.sessions++
	r.messages += len(doc.Messages)
	r.toolCalls += len(doc.ToolCalls)

	if first, dup := r.seenSessionFiles[doc.Session.SessionID]; dup {
		r.observationSets = append(r.observationSets, []invariants.Observation{{
			Kind:     invariants.KindDuplicateSessionID,
			Count:    1,
			Examples: []string{fmt.Sprintf("%s also produced by %s", path, first)},
		}})
	} else {
		r.seenSessionFiles[doc.Session.SessionID] = path
	}
}

// resolveContractRoot resolves the root this tier reads. A5: it SKIPS
// outright — never reads anything — unless contractPathEnv is explicitly
// set. Earlier, an unset contractPathEnv fell through to
// AGENT_LOGS_EXTRACTOR_HOME, else the real $HOME/.claude: a documented
// "DO NOT run this bare" warning (see the package doc, docs/development.md,
// and justfile) with a default that walks straight into exactly that. This
// makes the warning mechanical instead of a comment a developer has to
// remember: `just test-contract` run with no override now SKIPs rather
// than reading whatever ~/.claude happens to exist in the sandbox.
func resolveContractRoot(t *testing.T) (root, source string) {
	t.Helper()
	p := os.Getenv(contractPathEnv)
	if p == "" {
		t.Skipf("claude contract: %s is unset; this tier refuses to default to a live ~/.claude. Set %s=$(mktemp -d) to exercise the skip path, or %s=$PWD/internal/testing/logfixture/claude (or your own real ~/.claude) to exercise the found-logs path", contractPathEnv, contractPathEnv, contractPathEnv)
	}
	return p, contractPathEnv
}
