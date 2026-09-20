package contracts_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/codexsource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/contracts"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/testlogs"
)

func TestInventedExamplesSatisfyContract(t *testing.T) {
	for _, tc := range []struct {
		src  ports.ConversationSource
		root string
	}{
		{claudesource.New(nil), testlogs.ClaudeRoot(t)},
		{codexsource.New(nil), testlogs.CodexRoot(t)},
	} {
		r := contracts.Check(context.Background(), tc.src, tc.root)
		if len(r.Failures) != 0 || r.Messages == 0 || r.Observed["tool_result_join"] == 0 {
			t.Fatalf("%s: %+v", tc.src.Vendor(), r)
		}
	}
}

type brokenSource struct {
	ports.ConversationSource
	mode string
}

func (b brokenSource) Parse(ctx context.Context, p string) (model.SessionDoc, model.ParseStats, error) {
	d, s, e := b.ConversationSource.Parse(ctx, p)
	switch b.mode {
	case "drop":
		d.Messages = nil
		d.ToolCalls = nil
	case "join":
		for i := range d.ToolCalls {
			d.ToolCalls[i].Status = model.StatusPending
			d.ToolCalls[i].Output = ""
		}
	case "error":
		return d, s, errors.New("PRIVATE_SENTINEL path prompt credential")
	case "panic":
		panic("PRIVATE_SENTINEL")
	}
	return d, s, e
}
func TestDetectsLossAndSuppressesPrivateDiagnostics(t *testing.T) {
	for _, mode := range []string{"drop", "join", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			r := contracts.Check(context.Background(), brokenSource{codexsource.New(nil), mode}, testlogs.CodexRoot(t))
			if len(r.Failures) == 0 {
				t.Fatal("broken adapter passed")
			}
			if strings.Contains(fmt.Sprint(r), "PRIVATE_SENTINEL") {
				t.Fatal("private diagnostic leaked")
			}
		})
	}
}
func TestEmptyInputIsNotEvidence(t *testing.T) {
	r := contracts.Check(context.Background(), codexsource.New(nil), t.TempDir())
	if r.Files != 0 || len(r.Observed) != 0 {
		t.Fatalf("empty source claimed evidence: %+v", r)
	}
}

type unsupportedSource struct{ ports.ConversationSource }

func (unsupportedSource) Vendor() model.Vendor { return "PRIVATE_SENTINEL" }

func TestUnsupportedVendorFailsWithoutReadingSource(t *testing.T) {
	// The embedded nil source panics if the checker tries to discover or parse it.
	r := contracts.Check(context.Background(), unsupportedSource{}, t.TempDir())
	if len(r.Failures) != 1 || r.Failures["unsupported_vendor"] != 1 || r.Files != 0 || len(r.Observed) != 0 {
		t.Fatalf("unsupported vendor was not rejected: %+v", r)
	}
	if strings.Contains(fmt.Sprint(r), "PRIVATE_SENTINEL") {
		t.Fatal("vendor label leaked")
	}
}

func TestCodexDuplicateCallsAndResultsKeepFirst(t *testing.T) {
	for _, family := range []string{"function_call", "custom_tool_call"} {
		t.Run(family, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "sessions"), 0700); err != nil {
				t.Fatal(err)
			}
			text := fmt.Sprintf(`{"type":"response_item","payload":{"type":%q,"id":"first","call_id":"call","name":"shell"}}
{"type":"response_item","payload":{"type":%q,"id":"second","call_id":"call","name":"other"}}
{"type":"response_item","payload":{"type":%q,"id":"result","call_id":"call","output":null,"is_error":true}}
{"type":"response_item","payload":{"type":%q,"id":"result","call_id":"call","output":"first","is_error":false}}
{"type":"response_item","payload":{"type":%q,"call_id":"call","output":"second","is_error":true}}
`, family, family, family+"_output", family+"_output", family+"_output")
			if err := os.WriteFile(filepath.Join(root, "sessions", "rollout-duplicates.jsonl"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			r := contracts.Check(context.Background(), codexsource.New(nil), root)
			if len(r.Failures) != 0 || r.Messages != 1 || r.Tools != 1 || r.Observed["tool_result_join"] != 1 {
				t.Fatalf("first-wins normalization rejected: %+v", r)
			}
			if r.Drift["skipped_records_requiring_review"] != 3 {
				t.Fatalf("duplicates and malformed output must remain visible for review: %+v", r)
			}
		})
	}
}

func TestUnextractableSystemEventRequiresReview(t *testing.T) {
	root := testlogs.ClaudeRoot(t)
	path := filepath.Join(root, "projects", testlogs.ClaudeSingleProject, "single.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"system","uuid":"system-event","subtype":"PRIVATE_SENTINEL"}` + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	r := contracts.Check(context.Background(), claudesource.New(nil), root)
	if len(r.Failures) != 0 || r.Drift["conversation_without_content"] != 1 {
		t.Fatalf("unextractable event misreported: %+v", r)
	}
	if strings.Contains(fmt.Sprint(r), "PRIVATE_SENTINEL") {
		t.Fatal("source label leaked")
	}
}
