package claudesource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// writeSyntheticSession writes lines (each already a complete JSON record)
// as a session file in a fresh temp directory and returns its path.
func writeSyntheticSession(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

const syntheticUserLine = `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`

// syntheticUserLineWith is syntheticUserLine, generalized: a caller picks
// its own uuid/parentUuid/text instead of the fixed "u1"/null/"hi" the
// const carries. Used wherever a test needs more than one distinct-but-
// similar user record (e.g. two records that deliberately share a uuid).
func syntheticUserLineWith(uuid, parentUUID, text string) string {
	pu := "null"
	if parentUUID != "" {
		pu = fmt.Sprintf("%q", parentUUID)
	}
	return fmt.Sprintf(`{"parentUuid":%s,"isSidechain":false,"type":"user","message":{"role":"user","content":%q},"uuid":%q,"timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
		pu, text, uuid)
}

func syntheticToolUseLine(uuid, parentUUID, toolUseID string) string {
	return fmt.Sprintf(`{"parentUuid":%q,"isSidechain":false,"type":"assistant","message":{"model":"m","role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"x"}}]},"uuid":%q,"timestamp":"2026-01-01T00:00:01.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
		parentUUID, toolUseID, uuid)
}

func syntheticToolResultLine(uuid, parentUUID, toolUseID string, content json.RawMessage, omitContent, isError bool) string {
	var block string
	if omitContent {
		block = fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"is_error":%v}`, toolUseID, isError)
	} else {
		block = fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"is_error":%v,"content":%s}`, toolUseID, isError, content)
	}
	return fmt.Sprintf(`{"parentUuid":%q,"isSidechain":false,"type":"user","message":{"role":"user","content":[%s]},"uuid":%q,"timestamp":"2026-01-01T00:00:02.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
		parentUUID, block, uuid)
}

// TestToolResultShapes is a pure, table-driven, in-code exercise of every
// tool_result content shape and the orphan case, built from synthetic
// records rather than fixtures — the same coverage §D5/§C.6 of the ticket
// plan describes for resultOutput and applyToolResults, driven entirely
// through the public Source.Parse API.
func TestToolResultShapes(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	tests := []struct {
		name        string
		content     json.RawMessage
		omitContent bool
		isError     bool
		toolUseID   string
		wantOrphan  bool
		wantStatus  model.ToolCallStatus
		wantOutput  string
	}{
		{
			name:       "string content, success",
			content:    json.RawMessage(`"hello"`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "hello",
		},
		{
			name:       "string content, error",
			content:    json.RawMessage(`"boom"`),
			isError:    true,
			toolUseID:  "toolu_1",
			wantStatus: model.StatusError,
			wantOutput: "boom",
		},
		{
			name:       "block array content, text blocks join with newline",
			content:    json.RawMessage(`[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "line one\nline two",
		},
		{
			name:       "block array content, non-text blocks contribute nothing",
			content:    json.RawMessage(`[{"type":"text","text":"kept"},{"type":"tool_use","id":"x","name":"y"}]`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "kept",
		},
		{
			name:        "absent content",
			omitContent: true,
			toolUseID:   "toolu_1",
			wantStatus:  model.StatusOK,
			wantOutput:  "",
		},
		{
			name:       "null content",
			content:    json.RawMessage(`null`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "",
		},
		{
			name:       "orphan tool_use_id",
			content:    json.RawMessage(`"whatever"`),
			toolUseID:  "toolu_does_not_exist",
			wantOrphan: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := []string{
				syntheticUserLine,
				syntheticToolUseLine("u2", "u1", "toolu_1"),
				syntheticToolResultLine("u3", "u2", tt.toolUseID, tt.content, tt.omitContent, tt.isError),
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

			if tt.wantOrphan {
				if got := stats.Skipped[claudesource.SkipOrphanToolResult]; got != 1 {
					t.Errorf("SkipOrphanToolResult count = %d, want 1", got)
				}
				if tc.Status != model.StatusPending {
					t.Errorf("status = %q, want pending (the result never matched)", tc.Status)
				}
				return
			}

			if tc.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", tc.Status, tt.wantStatus)
			}
			if tc.Output != tt.wantOutput {
				t.Errorf("output = %q, want %q", tc.Output, tt.wantOutput)
			}
		})
	}
}

// TestTextOf is a pure, table-driven, in-code exercise of message text
// extraction (record.go's textOf), driven entirely through the public
// Source.Parse API: string content, a block array mixing text/thinking/
// tool_use blocks, an empty array, and null.
func TestTextOf(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		content string // raw JSON for message.content
		want    string
	}{
		{"string content", `"hello world"`, "hello world"},
		{
			"block array: thinking contributes nothing, text blocks join with newline, tool_use ignored",
			`[{"type":"thinking","thinking":"secret"},{"type":"text","text":"first"},{"type":"tool_use","id":"t1","name":"Bash","input":{}},{"type":"text","text":"second"}]`,
			"first\nsecond",
		},
		{"empty array", `[]`, ""},
		{"null", `null`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"type":"assistant","message":{"model":"m","role":"assistant","content":%s},"uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`, tt.content)
			path := writeSyntheticSession(t, []string{line})

			doc, _, err := src.Parse(ctx, path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(doc.Messages) != 1 {
				t.Fatalf("got %d messages, want 1", len(doc.Messages))
			}
			if got := doc.Messages[0].Text; got != tt.want {
				t.Errorf("Text = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDuplicateIDsAreDeduped pins C4/A3: a repeated uuid or tool_use id
// must not produce two rows sharing one primary key. The first row wins;
// every later duplicate is dropped and counted, never silently emitted.
func TestDuplicateIDsAreDeduped(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	t.Run("duplicate message uuid", func(t *testing.T) {
		lines := []string{
			syntheticUserLine, // uuid "u1", text "hi"
			syntheticUserLineWith("u1", "", "second copy, should be dropped"),
		}
		path := writeSyntheticSession(t, lines)
		doc, stats, err := src.Parse(ctx, path)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(doc.Messages) != 1 {
			t.Fatalf("got %d messages, want 1 (the duplicate must be dropped, not appended)", len(doc.Messages))
		}
		if doc.Messages[0].Text != "hi" {
			t.Errorf("kept message Text = %q, want %q (the first row must win)", doc.Messages[0].Text, "hi")
		}
		if got := stats.Skipped[claudesource.SkipDuplicateMessageID]; got != 1 {
			t.Errorf("SkipDuplicateMessageID count = %d, want 1", got)
		}
	})

	t.Run("duplicate tool_use id", func(t *testing.T) {
		lines := []string{
			syntheticUserLine,
			syntheticToolUseLine("u2", "u1", "toolu_1"),
			syntheticToolUseLine("u3", "u2", "toolu_1"), // same tool_use id, from a different message
		}
		path := writeSyntheticSession(t, lines)
		doc, stats, err := src.Parse(ctx, path)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1 (the duplicate must be dropped, not appended)", len(doc.ToolCalls))
		}
		if doc.ToolCalls[0].MessageID != "claude:u2" {
			t.Errorf("kept tool call MessageID = %q, want %q (the first row must win)", doc.ToolCalls[0].MessageID, "claude:u2")
		}
		if got := stats.Skipped[claudesource.SkipDuplicateToolUseID]; got != 1 {
			t.Errorf("SkipDuplicateToolUseID count = %d, want 1", got)
		}
		// Deduping the tool_calls row must not swallow the message it was
		// attached to — both assistant messages still get their own row.
		if len(doc.Messages) != 3 {
			t.Errorf("got %d messages, want 3", len(doc.Messages))
		}
	})
}

// TestEmptyIDBlocksDoNotFabricateJoins pins C3/A4: a tool_use with no id
// must never be indexed under the literal key "claude:" (nsID of ""), and
// a tool_result with no tool_use_id must count as an orphan rather than
// join to it — the two blocks are unrelated and must never be connected
// just because both happen to be id-less.
func TestEmptyIDBlocksDoNotFabricateJoins(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	lines := []string{
		syntheticUserLine,
		syntheticToolUseLine("u2", "u1", ""),
		syntheticToolResultLine("u3", "u2", "", json.RawMessage(`"leaked"`), false, false),
	}
	path := writeSyntheticSession(t, lines)
	doc, stats, err := src.Parse(ctx, path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.ToolCalls) != 0 {
		t.Fatalf("got %d tool calls, want 0 (an id-less tool_use must never become a row)", len(doc.ToolCalls))
	}
	if got := stats.Skipped[claudesource.SkipMissingToolUseID]; got != 1 {
		t.Errorf("SkipMissingToolUseID count = %d, want 1", got)
	}
	if got := stats.Skipped[claudesource.SkipOrphanToolResult]; got != 1 {
		t.Errorf("SkipOrphanToolResult count = %d, want 1", got)
	}
}

// TestSkipMissingUUIDAndMissingMessage covers C9's gap: SkipMissingUUID and
// SkipMissingMessage fired correctly but were exercised by no fixture and
// no synthetic test, unlike every other skip reason.
func TestSkipMissingUUIDAndMissingMessage(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	t.Run("missing uuid", func(t *testing.T) {
		line := `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hi"},"timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`
		path := writeSyntheticSession(t, []string{line})
		doc, stats, err := src.Parse(ctx, path)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(doc.Messages) != 0 {
			t.Errorf("got %d messages, want 0", len(doc.Messages))
		}
		if got := stats.Skipped[claudesource.SkipMissingUUID]; got != 1 {
			t.Errorf("SkipMissingUUID count = %d, want 1", got)
		}
	})

	t.Run("missing message and content", func(t *testing.T) {
		line := `{"parentUuid":null,"isSidechain":false,"type":"assistant","uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`
		path := writeSyntheticSession(t, []string{line})
		doc, stats, err := src.Parse(ctx, path)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(doc.Messages) != 0 {
			t.Errorf("got %d messages, want 0", len(doc.Messages))
		}
		if got := stats.Skipped[claudesource.SkipMissingMessage]; got != 1 {
			t.Errorf("SkipMissingMessage count = %d, want 1", got)
		}
	})
}

// TestMalformedNullAndEmptyObjectLines pins C12/A7: a bare `null` line, or
// any object carrying neither a type nor a uuid, must classify as
// malformed_line — not unknown_record_type, which exists to signal a
// genuinely new *vendor* record type, not a broken line.
func TestMalformedNullAndEmptyObjectLines(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	lines := []string{
		"null",
		"{}",
		syntheticUserLine,
	}
	path := writeSyntheticSession(t, lines)
	doc, stats, err := src.Parse(ctx, path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Messages) != 1 {
		t.Errorf("got %d messages, want 1", len(doc.Messages))
	}
	if got := stats.Skipped[model.SkipMalformedLine]; got != 2 {
		t.Errorf("SkipMalformedLine count = %d, want 2 (bare null and {} both carry neither type nor uuid)", got)
	}
	if got := stats.Skipped[model.SkipUnknownRecordType]; got != 0 {
		t.Errorf("SkipUnknownRecordType count = %d, want 0 (a broken line must not inflate the new-vendor-record-type signal)", got)
	}
}
