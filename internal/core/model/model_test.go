package model

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
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

	// Assert the exact key set per relation, not a substring search over
	// the whole marshaled document: a substring check over the full doc
	// can't tell a tag on the wrong struct from a tag on the right one,
	// and reflect.DeepEqual on the round trip can't catch a tag rename at
	// all (encoding/json falls back to case-insensitive field-name
	// matching on decode). Exact key sets catch removals, renames, and
	// accidental additions alike.
	assertExactKeys(t, doc.Session, []string{
		"session_id", "vendor", "project_path", "project_name",
		"started_at", "ended_at", "git_branch", "vendor_version",
		"source_path",
	})
	assertExactKeys(t, doc.Messages[0], []string{
		"message_id", "session_id", "seq", "parent_message_id",
		"role", "created_at", "text", "model", "raw",
	})
	assertExactKeys(t, doc.ToolCalls[0], []string{
		"tool_call_id", "session_id", "message_id", "seq", "tool_name",
		"arguments", "output", "status", "created_at",
	})

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got SessionDoc
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(doc, got) {
		t.Errorf("round trip mismatch:\n got  = %+v\n want = %+v", got, doc)
	}
}

// assertExactKeys marshals v and fails the test unless its top-level JSON
// object keys are exactly want.
func assertExactKeys(t *testing.T, v any, want []string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := slices.Sorted(maps.Keys(m))
	wantSorted := slices.Sorted(slices.Values(want))
	if !slices.Equal(got, wantSorted) {
		t.Errorf("keys = %v, want %v", got, wantSorted)
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
		`"git_branch"`, `"parent_message_id"`, `"model"`, `"output"`,
	}
	for _, key := range absent {
		if strings.Contains(body, key) {
			t.Errorf("expected key %s to be absent from JSON when empty; got %s", key, body)
		}
	}

	// vendor_version is NOT in the TDD's nullable-column set
	// (docs/technical/tdd-mvp.md), so it must stay present (as an empty
	// string) rather than being omitted like the genuinely nullable
	// fields above.
	if !strings.Contains(body, `"vendor_version":""`) {
		t.Errorf(`expected "vendor_version":"" to be present for an empty VendorVersion; got %s`, body)
	}

	if !strings.Contains(body, `"seq":0`) {
		t.Errorf(`expected "seq":0 to be present for a zero-value Seq; got %s`, body)
	}
}

func TestSessionDocRawNilAndEmpty(t *testing.T) {
	t.Run("nil Raw round-trips to JSON null, not back to nil", func(t *testing.T) {
		msg := Message{Raw: nil}
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var got Message
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.Raw == nil {
			t.Error("round-tripped Raw = nil, want json.RawMessage(\"null\") — round trip is not identity for nil Raw")
		}
		if string(got.Raw) != "null" {
			t.Errorf("round-tripped Raw = %s, want null", got.Raw)
		}
	})

	t.Run("non-nil empty Raw fails to marshal", func(t *testing.T) {
		msg := Message{Raw: json.RawMessage{}}
		if _, err := json.Marshal(msg); err == nil {
			t.Error("Marshal with empty (non-nil) Raw: want error, got nil — source adapters must never emit an empty json.RawMessage, only valid JSON or nil")
		}
	})
}
