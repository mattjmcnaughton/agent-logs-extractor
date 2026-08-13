package claudesource_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// TestPathologicalNeverErrors is the ticket's central done-when clause for
// the malformed fixtures: Parse must never return an error for any of
// them, however badly formed their content.
func TestPathologicalNeverErrors(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	paths := mustList(t, src, logfixture.PathologicalClaudeRoot())
	if len(paths) != 4 {
		t.Fatalf("expected 4 pathological Claude fixtures, got %d", len(paths))
	}
	for _, p := range paths {
		if _, _, err := src.Parse(ctx, p); err != nil {
			t.Errorf("Parse(%s) returned an error: %v", p, err)
		}
	}
}

// TestNoPanicOnAnyFixture parses every *.jsonl under the whole fixture
// tree directly, including subagent transcripts read as if they were
// top-level sessions and the Codex fixtures — inputs this adapter was
// never designed for — asserting only that it never panics.
func TestNoPanicOnAnyFixture(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	found := 0
	err := filepath.WalkDir(logfixture.Dir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		found++
		rel := strings.TrimPrefix(p, logfixture.Dir()+string(filepath.Separator))
		t.Run(rel, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse(%s) panicked: %v", p, r)
				}
			}()
			_, _, _ = src.Parse(ctx, p)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", logfixture.Dir(), err)
	}
	if found == 0 {
		t.Fatal("expected at least one .jsonl file under the fixture tree")
	}
}

// TestListSkipsSubagentTranscripts pins the ticket's enumeration contract:
// List returns only top-level session files, never a subagent transcript.
func TestListSkipsSubagentTranscripts(t *testing.T) {
	src := claudesource.New(nil)
	paths := mustList(t, src, logfixture.ClaudeRoot())
	if len(paths) != 3 {
		t.Fatalf("List(ClaudeRoot()) returned %d paths, want 3: %v", len(paths), paths)
	}
	marker := string(filepath.Separator) + "subagents" + string(filepath.Separator)
	for _, p := range paths {
		if strings.Contains(p, marker) {
			t.Errorf("List returned a subagent transcript: %s", p)
		}
	}
}

// TestListMissingRootIsNotAnError pins US-7: a vendor root (or its
// projects/ directory) that does not exist yields no paths and no error,
// same as one that exists but is empty.
func TestListMissingRootIsNotAnError(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	paths, err := src.List(ctx, filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if paths != nil {
		t.Errorf("List = %v, want nil", paths)
	}

	emptyRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(emptyRoot, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err = src.List(ctx, emptyRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if paths != nil {
		t.Errorf("List = %v, want nil", paths)
	}
}

// TestFixtureShape pins the exact message/tool_call/skip counts (and a
// handful of load-bearing field values) each committed fixture must
// produce. This is what stops a blind `-update` after a fixture
// regeneration from silently accepting a behaviour change: id/timestamp
// churn is expected, but a changed count here is not.
func TestFixtureShape(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	sidechainPath, err := logfixture.SessionFile(logfixture.ClaudeSidechainProject)
	if err != nil {
		t.Fatal(err)
	}
	toolErrorPath, err := logfixture.SessionFile(logfixture.ClaudeToolErrorProject)
	if err != nil {
		t.Fatal(err)
	}

	singleTurnDir := filepath.Join(logfixture.ClaudeProjectsDir(), "-tmp-claude-0--home-user-94ba8eae-3476-51cf-a4d4-b0b5339db735-scratchpad-fixture-project")
	singleTurnEntries, err := os.ReadDir(singleTurnDir)
	if err != nil {
		t.Fatal(err)
	}
	var singleTurnPath string
	for _, e := range singleTurnEntries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			singleTurnPath = filepath.Join(singleTurnDir, e.Name())
		}
	}
	if singleTurnPath == "" {
		t.Fatal("single-turn fixture session file not found")
	}

	pathDemoDir := filepath.Join(logfixture.PathologicalClaudeRoot(), "projects", "-home-user-demo")
	truncatedPath := filepath.Join(pathDemoDir, "11111111-1111-4111-8111-111111111111.jsonl")
	unknownTypePath := filepath.Join(pathDemoDir, "22222222-2222-4222-8222-222222222222.jsonl")
	missingResultPath := filepath.Join(pathDemoDir, "33333333-3333-4333-8333-333333333333.jsonl")
	emptyPath := filepath.Join(pathDemoDir, "44444444-4444-4444-8444-444444444444.jsonl")

	type want struct {
		messages  int
		toolCalls int
		skips     model.SkipCounts
	}
	tests := []struct {
		name string
		path string
		want want
	}{
		{"sidechain", sidechainPath, want{7, 2, model.SkipCounts{model.SkipBookkeeping: 9}}},
		{"tool-error", toolErrorPath, want{4, 1, model.SkipCounts{model.SkipBookkeeping: 7}}},
		{"single-turn", singleTurnPath, want{4, 1, model.SkipCounts{model.SkipBookkeeping: 7}}},
		{"pathological/truncated", truncatedPath, want{1, 0, model.SkipCounts{model.SkipBookkeeping: 3, model.SkipMalformedLine: 1}}},
		{"pathological/unknown-type", unknownTypePath, want{1, 0, model.SkipCounts{model.SkipBookkeeping: 2, model.SkipUnknownRecordType: 1}}},
		{"pathological/missing-tool-result", missingResultPath, want{4, 1, model.SkipCounts{model.SkipBookkeeping: 7}}},
		{"pathological/empty", emptyPath, want{0, 0, nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, stats, err := src.Parse(ctx, tt.path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(doc.Messages) != tt.want.messages {
				t.Errorf("messages = %d, want %d", len(doc.Messages), tt.want.messages)
			}
			if len(doc.ToolCalls) != tt.want.toolCalls {
				t.Errorf("tool_calls = %d, want %d", len(doc.ToolCalls), tt.want.toolCalls)
			}
			if !maps.Equal(stats.Skipped, tt.want.skips) {
				t.Errorf("skips = %v, want %v", stats.Skipped, tt.want.skips)
			}
		})
	}

	t.Run("sidechain fields", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, sidechainPath)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Session.SessionID != "claude:40def079-df87-46bd-ac03-5ffbf1a74ca2" {
			t.Errorf("session_id = %q", doc.Session.SessionID)
		}
		if doc.Session.ProjectPath != "/home/user/fixture-sidechain" {
			t.Errorf("project_path = %q", doc.Session.ProjectPath)
		}
		if doc.Session.ProjectName != "fixture-sidechain" {
			t.Errorf("project_name = %q", doc.Session.ProjectName)
		}
		if doc.Session.GitBranch != "main" {
			t.Errorf("git_branch = %q", doc.Session.GitBranch)
		}
		if doc.Session.VendorVersion != "2.1.231" {
			t.Errorf("vendor_version = %q", doc.Session.VendorVersion)
		}
		wantStart := time.Date(2026, 8, 13, 11, 16, 42, 550_000_000, time.UTC)
		wantEnd := time.Date(2026, 8, 13, 11, 16, 49, 806_000_000, time.UTC)
		if !doc.Session.StartedAt.Equal(wantStart) {
			t.Errorf("started_at = %s, want %s", doc.Session.StartedAt, wantStart)
		}
		if !doc.Session.EndedAt.Equal(wantEnd) {
			t.Errorf("ended_at = %s, want %s", doc.Session.EndedAt, wantEnd)
		}

		var agentTC, bashTC *model.ToolCall
		for i := range doc.ToolCalls {
			switch doc.ToolCalls[i].ToolName {
			case "Agent":
				agentTC = &doc.ToolCalls[i]
			case "Bash":
				bashTC = &doc.ToolCalls[i]
			}
		}
		if agentTC == nil || bashTC == nil {
			t.Fatal("expected both an Agent and a Bash tool call")
		}
		if agentTC.ToolCallID != "claude:toolu_01BDUbYNjgU13kKTWciYEUEm" {
			t.Errorf("agent tool_call_id = %q", agentTC.ToolCallID)
		}
		if agentTC.Seq != 0 {
			t.Errorf("agent seq = %d, want 0", agentTC.Seq)
		}
		if agentTC.Status != model.StatusOK {
			t.Errorf("agent status = %q, want ok", agentTC.Status)
		}
		wantAgentOutput := "The command output: `hello from subagent`\n" +
			"agentId: a0fb4161db4e2c307 (use SendMessage with to: 'a0fb4161db4e2c307', summary: '<5-10 word recap>' to continue this agent)\n" +
			"<usage>subagent_tokens: 14066\ntool_uses: 1\nduration_ms: 3280</usage>"
		if agentTC.Output != wantAgentOutput {
			t.Errorf("agent output = %q, want %q", agentTC.Output, wantAgentOutput)
		}

		if bashTC.ToolCallID != "claude:toolu_01LT6HE1Vb4do18ShHMyCNJ8" {
			t.Errorf("bash tool_call_id = %q", bashTC.ToolCallID)
		}
		if bashTC.Seq != 1 {
			t.Errorf("bash seq = %d, want 1", bashTC.Seq)
		}
		if bashTC.Status != model.StatusOK {
			t.Errorf("bash status = %q, want ok", bashTC.Status)
		}
		if bashTC.Output != "hello from subagent" {
			t.Errorf("bash output = %q, want %q", bashTC.Output, "hello from subagent")
		}
	})

	t.Run("tool-error fields", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, toolErrorPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatal("expected exactly one tool call")
		}
		tc := doc.ToolCalls[0]
		if tc.ToolCallID != "claude:toolu_01FUATKH4b5XsMviFXq4VNcj" {
			t.Errorf("tool_call_id = %q", tc.ToolCallID)
		}
		if tc.ToolName != "Bash" {
			t.Errorf("tool_name = %q", tc.ToolName)
		}
		if tc.Status != model.StatusError {
			t.Errorf("status = %q, want error", tc.Status)
		}
		wantOutput := "Exit code 1\ncat: /nonexistent-fixture-file: No such file or directory"
		if tc.Output != wantOutput {
			t.Errorf("output = %q, want %q", tc.Output, wantOutput)
		}
		if doc.Session.SessionID != "claude:8f1900e4-5b83-46c2-a244-2811996f87c3" {
			t.Errorf("session_id = %q", doc.Session.SessionID)
		}
		if doc.Session.ProjectPath != "/home/user/fixture-tool-error" {
			t.Errorf("project_path = %q", doc.Session.ProjectPath)
		}
		if doc.Session.GitBranch != "main" {
			t.Errorf("git_branch = %q", doc.Session.GitBranch)
		}
		if doc.Session.VendorVersion != "2.1.231" {
			t.Errorf("vendor_version = %q", doc.Session.VendorVersion)
		}
	})

	t.Run("single-turn fields", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, singleTurnPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatal("expected exactly one tool call")
		}
		tc := doc.ToolCalls[0]
		if tc.ToolCallID != "claude:toolu_01FrTAL9gbc7Fp3VgKpvw6VH" {
			t.Errorf("tool_call_id = %q", tc.ToolCallID)
		}
		if tc.Status != model.StatusOK {
			t.Errorf("status = %q, want ok", tc.Status)
		}
		if tc.Output != "hello fixture" {
			t.Errorf("output = %q, want %q", tc.Output, "hello fixture")
		}
		if doc.Session.ProjectName != "fixture-project" {
			t.Errorf("project_name = %q", doc.Session.ProjectName)
		}
		if doc.Session.GitBranch != "HEAD" {
			t.Errorf("git_branch = %q, want HEAD (passed through unchanged)", doc.Session.GitBranch)
		}
		if doc.Session.VendorVersion != "2.1.228" {
			t.Errorf("vendor_version = %q", doc.Session.VendorVersion)
		}
	})

	t.Run("pathological/missing-tool-result status pending", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, missingResultPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatal("expected exactly one tool call")
		}
		tc := doc.ToolCalls[0]
		if tc.Status != model.StatusPending {
			t.Errorf("status = %q, want pending", tc.Status)
		}
		if tc.Output != "" {
			t.Errorf("output = %q, want empty", tc.Output)
		}

		var lastMsg *model.Message
		for i := range doc.Messages {
			if doc.Messages[i].Text == "done" {
				lastMsg = &doc.Messages[i]
			}
		}
		if lastMsg == nil {
			t.Fatal(`expected a "done" message`)
		}
		// The dangling parentUuid (pointing at a uuid that appears nowhere
		// else in the file, because the tool_result record that carried
		// it was deleted) is preserved verbatim, not repaired or dropped
		// — it is what the vendor recorded.
		wantParent := "claude:75ca9980-2b60-471f-98a9-c508c157a032"
		if lastMsg.ParentMessageID != wantParent {
			t.Errorf("ParentMessageID = %q, want %q (dangling reference preserved verbatim)", lastMsg.ParentMessageID, wantParent)
		}
	})

	t.Run("pathological fixtures share the single-turn fixture's inner session_id/cwd", func(t *testing.T) {
		// Known artifact of how these fixtures were derived
		// (internal/testing/logfixture/README.md): despite their
		// synthetic filenames, all four carry the single-turn fixture's
		// real sessionId/cwd. Orchestrator adjudication 6: not "fixed" by
		// keying off the filename — pinned here so a future change to
		// that call is a deliberate, visible diff.
		want := "claude:94ba8eae-3476-51cf-a4d4-b0b5339db735"
		wantPath := "/tmp/claude-0/-home-user/94ba8eae-3476-51cf-a4d4-b0b5339db735/scratchpad/fixture-project"
		for _, p := range []string{truncatedPath, unknownTypePath, missingResultPath} {
			doc, _, err := src.Parse(ctx, p)
			if err != nil {
				t.Fatalf("Parse(%s): %v", p, err)
			}
			if doc.Session.SessionID != want {
				t.Errorf("%s: session_id = %q, want %q", p, doc.Session.SessionID, want)
			}
			if doc.Session.ProjectPath != wantPath {
				t.Errorf("%s: project_path = %q, want %q", p, doc.Session.ProjectPath, wantPath)
			}
		}
	})
}

// cancelAfterContext is a context.Context whose Err() returns nil for the
// first `after` calls and context.Canceled for every call after that. It
// exists to make A6's fix (a cancelled context during a subagent read must
// propagate, not be swallowed) reproducible deterministically: readRecords
// only calls ctx.Err() every 1024 lines, so a real context.WithCancel
// racing a goroutine against file I/O would be timing-flaky, but this lets
// the test decide exactly which Err() call goes live.
type cancelAfterContext struct {
	context.Context
	n     atomic.Int32
	after int32
}

func (c *cancelAfterContext) Err() error {
	if c.n.Add(1) > c.after {
		return context.Canceled
	}
	return nil
}

// TestSubagentContextCancellationPropagates pins C5/A6: a context that
// goes Done partway through reading a subagent transcript must make Parse
// return that error, not silently continue with a partial doc and
// err == nil. The parent transcript is kept under the 1024-line periodic
// check's threshold so it reads to completion untouched, and the fake
// context is tuned to report cancellation starting on the very next
// Err() call — the subagent file's first periodic check — so the failure
// is attributed to the subagent read specifically, not Parse's top-level
// check or the parent read.
func TestSubagentContextCancellationPropagates(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte(syntheticUserLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(dir, "session", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var subLines []string
	for i := 0; i < 1100; i++ {
		subLines = append(subLines, syntheticUserLine)
	}
	subPath := filepath.Join(subDir, "agent-a1.jsonl")
	if err := os.WriteFile(subPath, []byte(strings.Join(subLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := claudesource.New(nil)
	// Call #1 is Parse's own top-of-function ctx.Err() check; the parent
	// file (1 line) never reaches the periodic 1024-line check, so call #2
	// is the subagent file's first periodic check, right after it crosses
	// the 1024-line mark.
	ctx := &cancelAfterContext{Context: context.Background(), after: 1}

	doc, stats, err := src.Parse(ctx, sessionPath)
	if err == nil {
		t.Fatalf("Parse returned err == nil; want the propagated cancellation (doc=%+v stats=%+v)", doc, stats)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Parse error = %v, want context.Canceled", err)
	}
}
