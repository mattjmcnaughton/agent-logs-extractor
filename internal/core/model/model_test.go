package model

import (
	"encoding/json"
	"reflect"
	"sort"
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

	// Assert the exact key-to-value map per relation, not just the key
	// set and not a substring search over the whole marshaled document.
	// A substring check over the full doc can't tell a tag on the wrong
	// struct from a tag on the right one; an exact key *set* catches
	// removals, renames, and additions but can't detect a tag *swap*
	// between two same-type fields (e.g. project_path/project_name); and
	// reflect.DeepEqual on the round trip can't catch that either — a
	// swap is an involution, so it round-trips as identity. Comparing
	// key->value (fullSessionDoc gives each field a distinct sentinel)
	// catches all four failure modes with one assertion.
	assertJSONObject(t, doc.Session, map[string]string{
		"session_id":     `"claude:abc-123"`,
		"vendor":         `"claude"`,
		"project_path":   `"/home/u/proj"`,
		"project_name":   `"proj"`,
		"started_at":     `"2026-08-01T12:00:00Z"`,
		"ended_at":       `"2026-08-01T13:00:00Z"`,
		"git_branch":     `"main"`,
		"vendor_version": `"1.2.3"`,
		"source_path":    `"/home/u/.claude/projects/proj/session.jsonl"`,
	})
	assertJSONObject(t, doc.Messages[0], map[string]string{
		"message_id":        `"claude:msg-1"`,
		"session_id":        `"claude:abc-123"`,
		"seq":               `0`,
		"parent_message_id": `"claude:msg-0"`,
		"role":              `"user"`,
		"created_at":        `"2026-08-01T12:00:00Z"`,
		"text":              `"hello"`,
		"model":             `"claude-opus"`,
		"raw":               `{"type":"user"}`,
	})
	assertJSONObject(t, doc.ToolCalls[0], map[string]string{
		"tool_call_id": `"claude:tool-1"`,
		"session_id":   `"claude:abc-123"`,
		"message_id":   `"claude:msg-1"`,
		"seq":          `1`,
		"tool_name":    `"Read"`,
		"arguments":    `{"path":"/tmp/x"}`,
		"output":       `"file contents"`,
		"status":       `"ok"`,
		"created_at":   `"2026-08-01T12:00:00Z"`,
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

// assertJSONObject marshals v and fails the test unless its top-level JSON
// object is exactly want: same keys, same values. Comparing values (not
// just keys) is what catches a tag swapped between two same-type fields,
// which an exact-key-set check cannot.
func assertJSONObject(t *testing.T, v any, want map[string]string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := make(map[string]string, len(m))
	for k, v := range m {
		got[k] = string(v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
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

// TestZeroValueKeySets guards against someone spuriously adding omitempty
// to an always-populated field. TestSessionDocOmitsNullableFieldsAndKeepsZeroSeq
// only checks that a handful of specific keys are absent/present; it would
// not notice if, say, "text" or "session_id" also started disappearing on
// zero values. Here we marshal the zero value of each struct and assert
// the exact key set: only the four genuinely nullable columns (git_branch,
// parent_message_id, model, output) may vanish. Anything else missing from
// the zero-value marshal means a field just gained omitempty it shouldn't
// have — and per the model package's #9 note, an omitempty'd column
// disappears entirely from DuckDB's read_json auto-detection.
func TestZeroValueKeySets(t *testing.T) {
	t.Run("Session", func(t *testing.T) {
		assertExactKeySet(t, Session{}, []string{
			"session_id", "vendor", "project_path", "project_name",
			"started_at", "ended_at", "vendor_version", "source_path",
		})
	})
	t.Run("Message", func(t *testing.T) {
		assertExactKeySet(t, Message{}, []string{
			"message_id", "session_id", "seq", "role", "created_at",
			"text", "raw",
		})
	})
	t.Run("ToolCall", func(t *testing.T) {
		assertExactKeySet(t, ToolCall{}, []string{
			"tool_call_id", "session_id", "message_id", "seq", "tool_name",
			"arguments", "status", "created_at",
		})
	})
}

// assertExactKeySet marshals v and fails the test unless its top-level JSON
// object's key set is exactly want.
func assertExactKeySet(t *testing.T, v any, want []string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(got, wantSorted) {
		t.Errorf("zero-value key set = %v, want %v (marshaled: %s)", got, wantSorted, b)
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
