package codexsource_test

import (
	"bytes"
	"context"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/codexsource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/invariants"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/testlogs"
	"os"
	"reflect"
	"testing"
)

func TestSyntheticScenarios(t *testing.T) {
	src := codexsource.New(nil)
	paths, err := src.List(context.Background(), testlogs.CodexRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{
		"codex:parent":   {4, 2},
		"codex:child":    {3, 1},
		"codex:archived": {3, 1},
	}
	if len(paths) != len(want) {
		t.Fatalf("files=%d want %d", len(paths), len(want))
	}
	for _, path := range paths {
		doc, _, err := src.Parse(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		again, _, err := src.Parse(context.Background(), path)
		if err != nil || !reflect.DeepEqual(doc, again) {
			t.Fatal("repeat parsing changed the document")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range doc.Messages {
			if len(m.Raw) == 0 || !bytes.Contains(raw, append(append([]byte{}, m.Raw...), '\n')) {
				t.Fatal("message lost original source bytes")
			}
		}
		counts, ok := want[doc.Session.SessionID]
		if !ok {
			t.Fatalf("unexpected session %q", doc.Session.SessionID)
		}
		delete(want, doc.Session.SessionID)
		if len(doc.Messages) != counts[0] || len(doc.ToolCalls) != counts[1] {
			t.Errorf("%s: messages=%d tools=%d want %v", doc.Session.SessionID, len(doc.Messages), len(doc.ToolCalls), counts)
		}
		if failures := invariants.Check(doc); len(failures) != 0 {
			t.Errorf("%s: %v", path, failures)
		}
		for _, call := range doc.ToolCalls {
			if call.Status != model.StatusOK {
				t.Errorf("synthetic completed call %s status=%s", call.ToolCallID, call.Status)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing sessions: %v", want)
	}
}
