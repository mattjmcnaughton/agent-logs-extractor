package invariants_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/invariants"
)

// baseDoc returns a minimal, fully conforming SessionDoc: one message, one
// tool call, everything joined correctly. Every Check test below starts
// from a clone of this and breaks exactly one thing, so a test failure
// isolates to the invariant it names.
func baseDoc() model.SessionDoc {
	return model.SessionDoc{
		Session: model.Session{
			SessionID:     "claude:sess-1",
			Vendor:        model.VendorClaude,
			ProjectPath:   "/tmp/proj",
			VendorVersion: "1.0.0",
			StartedAt:     time.Unix(0, 0),
			EndedAt:       time.Unix(10, 0),
		},
		Messages: []model.Message{
			{
				MessageID: "claude:m1",
				SessionID: "claude:sess-1",
				Seq:       0,
				Role:      model.RoleAssistant,
				CreatedAt: time.Unix(1, 0),
				Raw:       json.RawMessage(`{"cwd":"/tmp/proj"}`),
			},
		},
		ToolCalls: []model.ToolCall{
			{
				ToolCallID: "claude:tc1",
				SessionID:  "claude:sess-1",
				MessageID:  "claude:m1",
				Seq:        0,
				ToolName:   "Bash",
				Status:     model.StatusOK,
				CreatedAt:  time.Unix(2, 0),
			},
		},
	}
}

func kinds(vs []invariants.Violation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Kind
	}
	return out
}

func hasKind(vs []invariants.Violation, kind string) bool {
	for _, v := range vs {
		if v.Kind == kind {
			return true
		}
	}
	return false
}

func TestCheckAcceptsAConformingDoc(t *testing.T) {
	if got := invariants.Check(baseDoc()); len(got) != 0 {
		t.Fatalf("Check(conforming doc) = %v, want none", got)
	}
}

func TestCheckMessageSeqGap(t *testing.T) {
	doc := baseDoc()
	doc.Messages[0].Seq = 1
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindSeqGap) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindSeqGap)
	}
}

func TestCheckToolCallSeqGap(t *testing.T) {
	doc := baseDoc()
	doc.ToolCalls[0].Seq = 5
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindSeqGap) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindSeqGap)
	}
}

func TestCheckMessageSessionIDMismatch(t *testing.T) {
	doc := baseDoc()
	doc.Messages[0].SessionID = "claude:other"
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindSessionIDMismatch) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindSessionIDMismatch)
	}
}

func TestCheckToolCallSessionIDMismatch(t *testing.T) {
	doc := baseDoc()
	doc.ToolCalls[0].SessionID = "claude:other"
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindSessionIDMismatch) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindSessionIDMismatch)
	}
}

func TestCheckDuplicateMessageID(t *testing.T) {
	doc := baseDoc()
	dup := doc.Messages[0]
	dup.Seq = 1
	doc.Messages = append(doc.Messages, dup)
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindDuplicateMessageID) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindDuplicateMessageID)
	}
}

func TestCheckDuplicateToolCallID(t *testing.T) {
	doc := baseDoc()
	dup := doc.ToolCalls[0]
	dup.Seq = 1
	doc.ToolCalls = append(doc.ToolCalls, dup)
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindDuplicateToolCallID) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindDuplicateToolCallID)
	}
}

func TestCheckDanglingToolCallMessageID(t *testing.T) {
	doc := baseDoc()
	doc.ToolCalls[0].MessageID = "claude:does-not-exist"
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindDanglingToolCallMessageID) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindDanglingToolCallMessageID)
	}
}

func TestCheckInvalidSessionDocWithContent(t *testing.T) {
	doc := baseDoc()
	doc.Session.SessionID = "" // ports.ValidSessionDoc now false, but doc still has rows
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindInvalidSessionDoc) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindInvalidSessionDoc)
	}
}

func TestCheckInvalidSessionDocIsFineWhenDocIsEmpty(t *testing.T) {
	// A doc with no messages/tool_calls and an invalid session id is
	// exactly the "pure bookkeeping" case sync.go's own ingestVendor
	// treats as normal, not a violation (docs/technical/tdd-mvp.md).
	doc := model.SessionDoc{}
	if got := invariants.Check(doc); len(got) != 0 {
		t.Fatalf("Check(empty doc) = %v, want none", got)
	}
}

func TestCheckInvalidRole(t *testing.T) {
	doc := baseDoc()
	doc.Messages[0].Role = "bogus"
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindInvalidRole) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindInvalidRole)
	}
}

func TestCheckInvalidStatus(t *testing.T) {
	doc := baseDoc()
	doc.ToolCalls[0].Status = "bogus"
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindInvalidStatus) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindInvalidStatus)
	}
}

func TestCheckRawJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool // true means valid (no violation)
	}{
		{"nil", nil, true},
		{"valid object", json.RawMessage(`{"a":1}`), true},
		{"valid null literal", json.RawMessage(`null`), true},
		{"non-nil empty", json.RawMessage{}, false},
		{"malformed", json.RawMessage(`{`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := baseDoc()
			doc.Messages[0].Raw = tt.raw
			got := invariants.Check(doc)
			gotValid := !hasKind(got, invariants.KindInvalidRawJSON)
			if gotValid != tt.want {
				t.Errorf("Raw=%q: valid=%v, want %v (violations: %v)", tt.raw, gotValid, tt.want, kinds(got))
			}
		})
	}
}

func TestCheckArgumentsJSON(t *testing.T) {
	doc := baseDoc()
	doc.ToolCalls[0].Arguments = json.RawMessage{}
	got := invariants.Check(doc)
	if !hasKind(got, invariants.KindInvalidArgumentsJSON) {
		t.Fatalf("Check() = %v, want a %s violation", kinds(got), invariants.KindInvalidArgumentsJSON)
	}
}

// --- Observe -----------------------------------------------------------

func obsFor(t *testing.T, obs []invariants.Observation, kind string) (invariants.Observation, bool) {
	t.Helper()
	for _, o := range obs {
		if o.Kind == kind {
			return o, true
		}
	}
	return invariants.Observation{}, false
}

func TestObserveConformingDocYieldsNothing(t *testing.T) {
	if got := invariants.Observe(baseDoc()); len(got) != 0 {
		t.Fatalf("Observe(conforming doc) = %v, want none", got)
	}
}

func TestObserveDanglingParentMessageID(t *testing.T) {
	doc := baseDoc()
	doc.Messages[0].ParentMessageID = "claude:ghost"
	got := invariants.Observe(doc)
	o, ok := obsFor(t, got, invariants.KindDanglingParentMessageID)
	if !ok || o.Count != 1 {
		t.Fatalf("Observe() = %v, want one %s", got, invariants.KindDanglingParentMessageID)
	}
}

func TestObserveZeroTimestamp(t *testing.T) {
	doc := baseDoc()
	doc.Messages[0].CreatedAt = time.Time{}
	got := invariants.Observe(doc)
	if o, ok := obsFor(t, got, invariants.KindZeroTimestamp); !ok || o.Count != 1 {
		t.Fatalf("Observe() = %v, want one %s", got, invariants.KindZeroTimestamp)
	}
}

func TestObserveCreatedAtNonMonotonic(t *testing.T) {
	doc := baseDoc()
	doc.Messages = append(doc.Messages, model.Message{
		MessageID: "claude:m2",
		SessionID: "claude:sess-1",
		Seq:       1,
		Role:      model.RoleUser,
		CreatedAt: time.Unix(0, 0), // earlier than m1's Unix(1,0)
	})
	got := invariants.Observe(doc)
	if o, ok := obsFor(t, got, invariants.KindCreatedAtNonMonotonic); !ok || o.Count != 1 {
		t.Fatalf("Observe() = %v, want one %s", got, invariants.KindCreatedAtNonMonotonic)
	}
}

func TestObserveEmptyProjectPath(t *testing.T) {
	doc := baseDoc()
	doc.Session.ProjectPath = ""
	got := invariants.Observe(doc)
	if _, ok := obsFor(t, got, invariants.KindEmptyProjectPath); !ok {
		t.Fatalf("Observe() = %v, want %s", got, invariants.KindEmptyProjectPath)
	}
}

func TestObserveEmptyVendorVersion(t *testing.T) {
	doc := baseDoc()
	doc.Session.VendorVersion = ""
	got := invariants.Observe(doc)
	if _, ok := obsFor(t, got, invariants.KindEmptyVendorVersion); !ok {
		t.Fatalf("Observe() = %v, want %s", got, invariants.KindEmptyVendorVersion)
	}
}

func TestObserveCwdDrift(t *testing.T) {
	doc := baseDoc()
	doc.Messages = append(doc.Messages, model.Message{
		MessageID: "claude:m2",
		SessionID: "claude:sess-1",
		Seq:       1,
		Role:      model.RoleUser,
		CreatedAt: time.Unix(3, 0),
		Raw:       json.RawMessage(`{"cwd":"/tmp/other"}`),
	})
	got := invariants.Observe(doc)
	o, ok := obsFor(t, got, invariants.KindCwdDrift)
	if !ok || o.Count != 2 {
		t.Fatalf("Observe() = %v, want %s with count 2", got, invariants.KindCwdDrift)
	}
	want := []string{"/tmp/other", "/tmp/proj"} // sorted
	if !reflect.DeepEqual(o.Examples, want) {
		t.Errorf("Examples = %v, want %v", o.Examples, want)
	}
}

func TestObserveNoCwdDriftWhenCwdIsStable(t *testing.T) {
	doc := baseDoc()
	doc.Messages = append(doc.Messages, model.Message{
		MessageID: "claude:m2",
		SessionID: "claude:sess-1",
		Seq:       1,
		Role:      model.RoleUser,
		CreatedAt: time.Unix(3, 0),
		Raw:       json.RawMessage(`{"cwd":"/tmp/proj"}`),
	})
	got := invariants.Observe(doc)
	if _, ok := obsFor(t, got, invariants.KindCwdDrift); ok {
		t.Fatalf("Observe() = %v, want no %s (both messages share one cwd)", got, invariants.KindCwdDrift)
	}
}

// --- MergeObservations ---------------------------------------------------

func TestMergeObservationsSumsCountsAndCapsExamples(t *testing.T) {
	a := []invariants.Observation{{Kind: "x", Count: 1, Examples: []string{"a1"}}}
	b := []invariants.Observation{{Kind: "x", Count: 1, Examples: []string{"a2"}}}
	c := []invariants.Observation{{Kind: "x", Count: 1, Examples: []string{"a3"}}}
	d := []invariants.Observation{{Kind: "x", Count: 1, Examples: []string{"a4"}}}

	got := invariants.MergeObservations(a, b, c, d)
	if len(got) != 1 {
		t.Fatalf("MergeObservations() = %v, want exactly one Kind", got)
	}
	if got[0].Count != 4 {
		t.Errorf("Count = %d, want 4 (summed across every input set)", got[0].Count)
	}
	if len(got[0].Examples) != 3 {
		t.Errorf("Examples = %v, want exactly 3 (capped)", got[0].Examples)
	}
	want := []string{"a1", "a2", "a3"}
	if !reflect.DeepEqual(got[0].Examples, want) {
		t.Errorf("Examples = %v, want %v (first-seen order across inputs)", got[0].Examples, want)
	}
}

func TestMergeObservationsSortsByKind(t *testing.T) {
	a := []invariants.Observation{{Kind: "zebra", Count: 1}}
	b := []invariants.Observation{{Kind: "aardvark", Count: 1}}

	got := invariants.MergeObservations(a, b)
	if len(got) != 2 || got[0].Kind != "aardvark" || got[1].Kind != "zebra" {
		t.Fatalf("MergeObservations() = %v, want [aardvark, zebra]", got)
	}
}

func TestMergeObservationsOfEmptyIsEmpty(t *testing.T) {
	if got := invariants.MergeObservations(); len(got) != 0 {
		t.Fatalf("MergeObservations() = %v, want none", got)
	}
}
