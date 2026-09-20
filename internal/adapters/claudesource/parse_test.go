package claudesource_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/testlogs"
)

// TestPathologicalNeverErrors is the ticket's central done-when clause for
// the malformed fixtures: Parse must never return an error for any of
// them, however badly formed their content.
func TestPathologicalNeverErrors(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	paths := mustList(t, src, testlogs.PathologicalClaudeRoot(t))
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
	root := testlogs.Dir(t)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		found++
		rel := strings.TrimPrefix(p, root+string(filepath.Separator))
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
		t.Fatalf("walking %s: %v", root, err)
	}
	if found == 0 {
		t.Fatal("expected at least one .jsonl file under the fixture tree")
	}
}

// TestListSkipsSubagentTranscripts pins the ticket's enumeration contract:
// List returns only top-level session files, never a subagent transcript.
func TestListSkipsSubagentTranscripts(t *testing.T) {
	src := claudesource.New(nil)
	paths := mustList(t, src, testlogs.ClaudeRoot(t))
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

// TestSyntheticSessionFields specifies behavior using invented records.
func TestSyntheticSessionFields(t *testing.T) {
	src := claudesource.New(nil)
	for _, tc := range []struct {
		project, id            string
		messages, tools, skips int
	}{
		{testlogs.ClaudeSingleProject, "single", 4, 1, 1},
		{testlogs.ClaudeSidechainProject, "parent", 7, 2, 2},
		{testlogs.ClaudeToolErrorProject, "failure", 4, 1, 1},
	} {
		path, err := testlogs.SessionFile(t, tc.project)
		if err != nil {
			t.Fatal(err)
		}
		doc, stats, err := src.Parse(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Session.SessionID != "claude:"+tc.id || len(doc.Messages) != tc.messages || len(doc.ToolCalls) != tc.tools || stats.Skipped[model.SkipBookkeeping] != tc.skips {
			t.Fatalf("scenario %s: unexpected result %+v %+v", tc.id, doc, stats)
		}
		if doc.Session.VendorVersion != "test" || doc.Session.GitBranch != "main" {
			t.Fatal("session metadata lost")
		}
		for _, call := range doc.ToolCalls {
			want := model.StatusOK
			if tc.id == "failure" {
				want = model.StatusError
			}
			if call.Status != want {
				t.Fatalf("status %s, want %s", call.Status, want)
			}
		}
		if tc.id == "single" && (doc.ToolCalls[0].ToolName != "Bash" || doc.ToolCalls[0].Output != "hello fixture" || doc.Messages[0].Text != "run the example") {
			t.Fatal("content mapping changed")
		}
	}
	root := testlogs.PathologicalClaudeRoot(t)
	path := filepath.Join(root, "projects", "-home-user-demo", "03-pending.jsonl")
	doc, _, err := src.Parse(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.ToolCalls) != 1 || doc.ToolCalls[0].Status != model.StatusPending || doc.ToolCalls[0].Output != "" {
		t.Fatal("missing result must stay pending")
	}
	if doc.Messages[len(doc.Messages)-1].ParentMessageID != "claude:single-result" {
		t.Fatal("dangling parent reference must survive")
	}
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
