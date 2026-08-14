//go:build contract

// Package claudesource_test's contract file is the opt-in tier that parses
// the developer's *live* ~/.claude (never a fixture) and asserts the
// structural invariants every SessionDoc must satisfy, then reports drift
// observations — the early-warning system for vendor format drift
// (docs/technical/tdd-mvp.md core decision 8). It is read-only end to end:
// only Source.List and Source.Parse are ever called here, never a
// CanonicalStore, so this tier can never write anything, anywhere.
//
// It skips (never fails) when there is nothing to read: no root, no
// root/projects, or zero session files listed. That is deliberate — a
// contributor with no live Claude Code history, or CI (which never sets
// ALX_CONTRACT_CLAUDE_PATH and has no ~/.claude of its own), must see this
// tier skip cleanly rather than fail. See docs/development.md for how to
// point it at a real or fixture tree, and why `just test-contract` must
// never be run against a sandbox's own harness transcript.
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
	root, source := resolveContractRoot()

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

	var (
		violations       []invariants.Violation
		observationSets  [][]invariants.Observation
		unreadable       []string
		sessions         int
		messages         int
		toolCalls        int
		seenSessionFiles = map[string]string{} // session id -> first file that produced it
	)

	for _, p := range paths {
		parseOneFile(t, ctx, src, p, &violations, &observationSets, &unreadable, &sessions, &messages, &toolCalls, seenSessionFiles)
	}

	merged := invariants.MergeObservations(observationSets...)

	t.Logf("claude contract: root=%s (resolved from %s), files scanned=%d, files unreadable=%d, sessions=%d, messages=%d, tool_calls=%d",
		root, source, len(paths), len(unreadable), sessions, messages, toolCalls)
	for _, o := range merged {
		t.Logf("claude contract: observation %s: count=%d examples=%v", o.Kind, o.Count, o.Examples)
	}

	for _, v := range violations {
		t.Errorf("claude contract: violation %s: %s", v.Kind, v.Detail)
	}
}

// parseOneFile parses one session file under a per-file defer/recover, so a
// panic anywhere in Parse is caught, reported with the offending path, and
// does not abort the rest of the run — the contract tier's own "no panic"
// invariant is enforced by this harness, not by invariants.Check (a pure
// function over an already-built SessionDoc has nothing left to panic on).
func parseOneFile(
	t *testing.T,
	ctx context.Context,
	src *claudesource.Source,
	path string,
	violations *[]invariants.Violation,
	observationSets *[][]invariants.Observation,
	unreadable *[]string,
	sessions, messages, toolCalls *int,
	seenSessionFiles map[string]string,
) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("claude contract: PANIC parsing %s: %v", path, r)
		}
	}()

	doc, stats, err := src.Parse(ctx, path)
	if err != nil {
		// A Parse error means the file itself could not be read — counted,
		// exactly like sync.go's own FilesUnreadable accounting, never
		// fatal to the run.
		t.Logf("claude contract: %s could not be read; skipping (counted, not fatal): %v", path, err)
		*unreadable = append(*unreadable, path)
		return
	}

	for _, v := range invariants.Check(doc) {
		v.Detail = fmt.Sprintf("%s: %s", path, v.Detail)
		*violations = append(*violations, v)
	}
	*observationSets = append(*observationSets, invariants.Observe(doc))

	if len(stats.Skipped) > 0 {
		skipObs := make([]invariants.Observation, 0, len(stats.Skipped))
		for reason, n := range stats.Skipped {
			skipObs = append(skipObs, invariants.Observation{
				Kind:     string(reason),
				Count:    n,
				Examples: []string{path},
			})
		}
		*observationSets = append(*observationSets, skipObs)
	}

	if doc.Session.SessionID == "" {
		// A zero doc (nothing but bookkeeping records) — mirrors sync.go's
		// own "empty doc" leniency; nothing to tally or dedup-check.
		return
	}
	*sessions++
	*messages += len(doc.Messages)
	*toolCalls += len(doc.ToolCalls)

	if first, dup := seenSessionFiles[doc.Session.SessionID]; dup {
		*observationSets = append(*observationSets, []invariants.Observation{{
			Kind:     invariants.KindDuplicateSessionID,
			Count:    1,
			Examples: []string{fmt.Sprintf("%s also produced by %s", path, first)},
		}})
	} else {
		seenSessionFiles[doc.Session.SessionID] = path
	}
}

// resolveContractRoot mirrors main.go's defaultPaths() precedence for
// ~/.claude — AGENT_LOGS_EXTRACTOR_HOME wins when set, else the user's real
// home directory — with contractPathEnv layered on top as the one
// additional override this test-only tier accepts, so it can be pointed at
// a fixture tree in a sandbox with no live ~/.claude at all.
func resolveContractRoot() (root, source string) {
	if p := os.Getenv(contractPathEnv); p != "" {
		return p, contractPathEnv
	}

	if home := os.Getenv("AGENT_LOGS_EXTRACTOR_HOME"); home != "" {
		return filepath.Join(home, ".claude"), "AGENT_LOGS_EXTRACTOR_HOME"
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	return filepath.Join(h, ".claude"), "$HOME"
}
