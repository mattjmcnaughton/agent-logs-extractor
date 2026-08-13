package claudesource_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// TestSessionMetaPrefersParentAfterMergeSort is a regression test for the
// aliasing bug fixed in Parse (claudesource.go): a prior version aliased
// parentRecs to the exact backing array the cross-file merge sort permutes
// in place, so sessionMeta's documented "parent transcript wins" preference
// silently read merged-order records instead — a subagent transcript's
// cwd/gitBranch/version/sessionId could leak into the session row whenever
// it sorted chronologically ahead of the parent's own metadata-bearing
// record.
//
// The shapes below are chosen to trigger the aliasing deterministically:
// readRecords builds parentRecs with 5 incremental appends starting from
// nil, and Go's slice growth leaves cap(parentRecs) == 8 after the 5th
// append (verified) — exactly enough spare capacity for the 3-record
// subagent append to write in place instead of reallocating. Only the
// parent's *last* record carries cwd/gitBranch/version/sessionId (every
// earlier parent line is bookkeeping, carrying none of them); every
// subagent record carries them and is timestamped earlier than every
// parent record, so after the merge sort the subagent's records occupy the
// first slots of the buggy, aliased "parentRecs" a naive implementation
// would go on to scan.
func TestSessionMetaPrefersParentAfterMergeSort(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	bookkeeping := func(typ, ts string) string {
		return fmt.Sprintf(`{"type":%q,"timestamp":%q}`, typ, ts)
	}
	parentLines := []string{
		bookkeeping("queue-operation", "2026-01-01T00:10:00Z"),
		bookkeeping("attachment", "2026-01-01T00:10:01Z"),
		bookkeeping("ai-title", "2026-01-01T00:10:02Z"),
		bookkeeping("last-prompt", "2026-01-01T00:10:03Z"),
		`{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"parent prompt"},"uuid":"parent-root","timestamp":"2026-01-01T00:10:04Z","cwd":"/PARENT","gitBranch":"parent-branch","version":"9.9.9","sessionId":"sess-parent"}`,
	}

	subLine := func(uuid, parentUUID, ts string) string {
		pu := "null"
		if parentUUID != "" {
			pu = fmt.Sprintf("%q", parentUUID)
		}
		return fmt.Sprintf(`{"parentUuid":%s,"isSidechain":false,"type":"user","message":{"role":"user","content":"subagent turn"},"uuid":%q,"timestamp":%q,"cwd":"/SUBAGENT","gitBranch":"sub-branch","version":"0.0.1","sessionId":"sess-sub"}`,
			pu, uuid, ts)
	}
	subLines := []string{
		subLine("sub-root", "", "2026-01-01T00:00:01Z"),
		subLine("sub-2", "sub-root", "2026-01-01T00:00:02Z"),
		subLine("sub-3", "sub-2", "2026-01-01T00:00:03Z"),
	}

	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte(strings.Join(parentLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "session", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subPath := filepath.Join(subDir, "agent-a1.jsonl")
	if err := os.WriteFile(subPath, []byte(strings.Join(subLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, _, err := src.Parse(ctx, sessionPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Session.ProjectPath != "/PARENT" {
		t.Errorf("project_path = %q, want %q (parent transcript must win regardless of merge order)", doc.Session.ProjectPath, "/PARENT")
	}
	if doc.Session.GitBranch != "parent-branch" {
		t.Errorf("git_branch = %q, want %q", doc.Session.GitBranch, "parent-branch")
	}
	if doc.Session.VendorVersion != "9.9.9" {
		t.Errorf("vendor_version = %q, want %q", doc.Session.VendorVersion, "9.9.9")
	}
}

// TestZeroCreatedAtExcludedFromSessionSpan is a regression test for C1: a
// message whose own timestamp is empty/unparseable leaves Message.CreatedAt
// at its zero value, and normalize's start/end scan must skip that message
// entirely rather than letting a zero CreatedAt drag Session.StartedAt down
// to 0001-01-01.
// Both orderings are covered deliberately. With the untimestamped record
// FIRST the mutation is masked: start is still zero when the loop reaches
// it, so the assignment is immediately overwritten by the start.IsZero()
// sentinel on the next message, and dropping the guard changes nothing.
// The guard only bites when a zero CreatedAt arrives AFTER a non-zero one,
// where zero.Before(start) drags StartedAt to 0001-01-01. A test that
// pinned only the first ordering would pass with the guard deleted.
func TestZeroCreatedAtExcludedFromSessionSpan(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	const (
		untimestampedFirstUUID = "u1"
		stampedUUID            = "u2"
	)
	stamped := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"stamped"},"uuid":"` + stampedUUID + `","timestamp":"2026-01-01T00:00:05Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`
	untimestamped := `{"parentUuid":null,"isSidechain":false,"type":"assistant","message":{"model":"m","role":"assistant","content":"unstamped"},"uuid":"` + untimestampedFirstUUID + `","timestamp":"","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`

	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{"untimestamped first", []string{untimestamped, stamped}},
		// The ordering that actually detects a missing guard.
		{"untimestamped last", []string{stamped, untimestamped}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSyntheticSession(t, tc.lines)

			doc, _, err := src.Parse(ctx, path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			want := time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC)
			if doc.Session.StartedAt.IsZero() {
				t.Fatal("StartedAt is zero, want the untimestamped message skipped and the stamped message's timestamp used")
			}
			if !doc.Session.StartedAt.Equal(want) {
				t.Errorf("StartedAt = %s, want %s", doc.Session.StartedAt, want)
			}
			if !doc.Session.EndedAt.Equal(want) {
				t.Errorf("EndedAt = %s, want %s", doc.Session.EndedAt, want)
			}
		})
	}
}

// TestWithinFileTimestampInversionClamped is a regression test for C2: a
// record's own timestamp can invert against its predecessor's within the
// same file (observed live in the sidechain fixture); readRecords must
// clamp the sort key to non-decreasing so a file's own line order stays
// authoritative for records within it. Without the clamp, this
// tool_result's earlier timestamp would sort it before the tool_use it
// joins to, producing an orphan_tool_result and leaving the tool call
// pending instead of joined.
func TestWithinFileTimestampInversionClamped(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	lines := []string{
		`{"parentUuid":null,"isSidechain":false,"type":"assistant","message":{"model":"m","role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"x"}}]},"uuid":"a1","timestamp":"2026-01-01T00:00:09Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
		`{"parentUuid":"a1","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":false,"content":"joined"}]},"uuid":"u2","timestamp":"2026-01-01T00:00:01Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
	}
	path := writeSyntheticSession(t, lines)

	doc, stats, err := src.Parse(ctx, path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(doc.ToolCalls))
	}
	tc := doc.ToolCalls[0]
	if tc.Status != model.StatusOK {
		t.Errorf("status = %q, want ok (unclamped gives pending + orphan_tool_result)", tc.Status)
	}
	if tc.Output != "joined" {
		t.Errorf("output = %q, want %q", tc.Output, "joined")
	}
	if got := stats.Skipped[claudesource.SkipOrphanToolResult]; got != 0 {
		t.Errorf("SkipOrphanToolResult count = %d, want 0", got)
	}
}

// TestWhitespaceOnlyLinesNotMalformed is a regression test for C6: a blank
// or whitespace-only line (after trimming its terminator) must be skipped
// silently, never counted as SkipMalformedLine.
func TestWhitespaceOnlyLinesNotMalformed(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	lines := []string{"   ", "\t", "", syntheticUserLine}
	path := writeSyntheticSession(t, lines)

	doc, stats, err := src.Parse(ctx, path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(doc.Messages))
	}
	if stats.Skipped != nil {
		t.Errorf("Skipped = %v, want nil (whitespace-only lines must not be counted as malformed_line)", stats.Skipped)
	}
}
