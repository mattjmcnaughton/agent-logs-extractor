package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fullSessionDoc returns a SessionDoc with every field populated, so
// round-trip and column-name assertions exercise the whole shape.
func fullSessionDoc() SessionDoc {
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	return SessionDoc{
		Session: Session{
			SessionID:     "claude:abc-123",
			Vendor:        VendorClaude,
			ProjectPath:   "/home/u/proj",
			ProjectName:   "proj",
			StartedAt:     started,
			EndedAt:       ended,
			GitBranch:     "main",
			VendorVersion: "1.2.3",
			SourcePath:    "/home/u/.claude/projects/proj/session.jsonl",
		},
		Messages: []Message{
			{
				MessageID:       "claude:msg-1",
				SessionID:       "claude:abc-123",
				Seq:             0,
				ParentMessageID: "claude:msg-0",
				Role:            RoleUser,
				CreatedAt:       started,
				Text:            "hello",
				Model:           "claude-opus",
				Raw:             json.RawMessage(`{"type":"user"}`),
			},
		},
		ToolCalls: []ToolCall{
			{
				ToolCallID: "claude:tool-1",
				SessionID:  "claude:abc-123",
				MessageID:  "claude:msg-1",
				Seq:        1,
				ToolName:   "Read",
				Arguments:  json.RawMessage(`{"path":"/tmp/x"}`),
				Output:     "file contents",
				Status:     StatusOK,
				CreatedAt:  started,
			},
		},
	}
}

func TestSessionDocJSONRoundTrip(t *testing.T) {
	doc := fullSessionDoc()

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The TDD's column names, verbatim, must appear in the marshaled bytes.
	wantColumns := []string{
		`"session_id"`, `"vendor"`, `"project_path"`, `"project_name"`,
		`"started_at"`, `"ended_at"`, `"git_branch"`, `"vendor_version"`,
		`"source_path"`, `"message_id"`, `"seq"`, `"parent_message_id"`,
		`"role"`, `"created_at"`, `"text"`, `"model"`, `"raw"`,
		`"tool_call_id"`, `"tool_name"`, `"arguments"`, `"output"`, `"status"`,
	}
	body := string(b)
	for _, col := range wantColumns {
		if !strings.Contains(body, col) {
			t.Errorf("marshaled SessionDoc missing column %s; got %s", col, body)
		}
	}

	var got SessionDoc
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(doc, got) {
		t.Errorf("round trip mismatch:\n got  = %+v\n want = %+v", got, doc)
	}
}

func TestSessionDocOmitsNullableFieldsAndKeepsZeroSeq(t *testing.T) {
	doc := SessionDoc{
		Session: Session{
			SessionID:   "claude:abc-123",
			Vendor:      VendorClaude,
			ProjectPath: "/home/u/proj",
			ProjectName: "proj",
			SourcePath:  "/home/u/.claude/projects/proj/session.jsonl",
			// GitBranch and VendorVersion left empty.
		},
		Messages: []Message{
			{
				MessageID: "claude:msg-1",
				SessionID: "claude:abc-123",
				Seq:       0,
				Role:      RoleUser,
				Text:      "hello",
				Raw:       json.RawMessage(`{}`),
				// ParentMessageID and Model left empty.
			},
		},
		ToolCalls: []ToolCall{
			{
				ToolCallID: "claude:tool-1",
				SessionID:  "claude:abc-123",
				MessageID:  "claude:msg-1",
				Seq:        1,
				ToolName:   "Read",
				Arguments:  json.RawMessage(`{}`),
				Status:     StatusPending,
				// Output left empty.
			},
		},
	}

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)

	absent := []string{
		`"git_branch"`, `"vendor_version"`, `"parent_message_id"`,
		`"model"`, `"output"`,
	}
	for _, key := range absent {
		if strings.Contains(body, key) {
			t.Errorf("expected key %s to be absent from JSON when empty; got %s", key, body)
		}
	}

	if !strings.Contains(body, `"seq":0`) {
		t.Errorf(`expected "seq":0 to be present for a zero-value Seq; got %s`, body)
	}
}
