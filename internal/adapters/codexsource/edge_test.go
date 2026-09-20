package codexsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/codexsource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/invariants"
)

func parseText(t *testing.T, text string) (model.SessionDoc, model.ParseStats) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-09-20T00-00-00-thread.jsonl")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	doc, stats, err := codexsource.New(nil).Parse(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if bad := invariants.Check(doc); len(bad) > 0 {
		t.Fatalf("invariants: %v", bad)
	}
	return doc, stats
}

func TestCallsWithoutCallIDDoNotClaimOtherResults(t *testing.T) {
	doc, stats := parseText(t, `{"type":"response_item","payload":{"type":"function_call","id":"same","name":"shell","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"same","output":"unrelated"}}`)
	if len(doc.ToolCalls) != 1 || doc.ToolCalls[0].Status != model.StatusPending || stats.Skipped["orphan_tool_result"] != 1 {
		t.Fatalf("calls=%+v stats=%+v", doc.ToolCalls, stats)
	}
}

func TestArgumentsResultsAndContext(t *testing.T) {
	doc, stats := parseText(t, `{"timestamp":"2026-09-20T00:00:00Z","type":"session_meta","payload":{"id":"own","cwd":"/project/demo","cli_version":"test","git":{"branch":"main"}}}
{"type":"turn_context","payload":{"model":"first"}}
{"type":"response_item","payload":{"type":"message","id":"dev","role":"developer","content":[{"type":"input_text","text":"setup"}]}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"json","output":[{"type":"input_text","text":"result"},{"type":"input_image","image_url":"data:image/png;base64,abc"}],"is_error":true}}
{"type":"response_item","payload":{"type":"function_call","id":"fc","call_id":"json","name":"read","arguments":"{\"path\":\"a\"}"}}
{"type":"turn_context","payload":{"model":"second"}}
{"type":"response_item","payload":{"type":"custom_tool_call","id":"cc","call_id":"custom","name":"exec","input":"not JSON"}}
{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"custom","output":"nested command error: exit 1"}}
{"type":"response_item","payload":{"type":"function_call","id":"bad","call_id":"bad","name":"read","arguments":"not JSON"}}
{"type":"response_item","payload":{"type":"message","id":"reply","role":"assistant","content":[{"type":"output_text","text":"one"},{"type":"output_text","text":"two"}]}}`)
	if len(doc.Messages) != 5 || len(doc.ToolCalls) != 3 {
		t.Fatalf("counts %d %d", len(doc.Messages), len(doc.ToolCalls))
	}
	if doc.Session.SessionID != "codex:own" || doc.Session.ProjectName != "demo" || doc.Session.GitBranch != "main" {
		t.Fatal(doc.Session)
	}
	if doc.Messages[0].Role != model.RoleSystem || doc.Messages[1].Model != "first" || doc.Messages[2].Model != "second" || doc.Messages[4].Text != "one\ntwo" {
		t.Fatal("role/model/text mapping")
	}
	calls := doc.ToolCalls
	if string(calls[0].Arguments) != `{"path":"a"}` || calls[0].Status != model.StatusError || !strings.Contains(calls[0].Output, "input_image") {
		t.Fatal(calls[0])
	}
	if string(calls[1].Arguments) != `"not JSON"` || calls[1].Status != model.StatusOK || calls[1].Output != "nested command error: exit 1" {
		t.Fatal(calls[1])
	}
	if string(calls[2].Arguments) != `"not JSON"` || calls[2].Status != model.StatusPending {
		t.Fatal(calls[2])
	}
	if stats.Skipped[model.SkipBookkeeping] != 3 {
		t.Fatal(stats)
	}
}

func TestMalformedUnknownDuplicatesAndLargeLines(t *testing.T) {
	big := strings.Repeat("x", 3*1024*1024)
	r := `{"type":"response_item","payload":{"type":"message","id":"m","role":"user","content":[{"type":"input_text","text":"` + big + `"}]}}`
	doc, stats := parseText(t, "bad\nnull\n"+r+"\n"+r+`
{"type":"future","payload":{}}
{"type":"response_item","payload":{"type":"future"}}
{"type":"event_msg","payload":{"type":"future"}}
{"type":"response_item","payload":{"type":"message","role":"future","content":[]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":3}}
{"truncated":`)
	if len(doc.Messages) != 1 || doc.Messages[0].Text != big || !bytes.Equal(doc.Messages[0].Raw, []byte(r)) {
		t.Fatal("valid row/raw lost")
	}
	if stats.Skipped[model.SkipMalformedLine] != 4 || stats.Skipped[model.SkipUnknownRecordType] != 4 || stats.Skipped["duplicate_record"] != 1 {
		t.Fatal(stats)
	}
	if doc.Session.SessionID != "codex:thread" {
		t.Fatal(doc.Session.SessionID)
	}
}

func TestInheritedHistoryUsesOwnerAndBoundary(t *testing.T) {
	text := `{"ordinal":0,"timestamp":"2026-09-20T00:00:00Z","type":"session_meta","payload":{"id":"child","cwd":"/child","subagent_history_start_ordinal":20}}
{"ordinal":1,"type":"session_meta","payload":{"id":"parent","cwd":"/parent"}}
{"ordinal":18,"type":"turn_context","payload":{"model":"inherited-model"}}
{"ordinal":19,"type":"response_item","payload":{"type":"message","id":"old","role":"user","content":[]}}
{"ordinal":20,"type":"response_item","payload":{"type":"message","id":"new","role":"assistant","content":[]}}`
	doc, stats := parseText(t, text)
	if doc.Session.SessionID != "codex:child" || doc.Session.ProjectPath != "/child" || len(doc.Messages) != 1 || doc.Messages[0].Model != "inherited-model" || stats.Skipped["inherited_history"] != 4 {
		t.Fatalf("doc=%+v stats=%+v", doc, stats)
	}
	for _, boundary := range []string{`"bad"`, `null`, `-1`} {
		d, _ := parseText(t, strings.Replace(text, `"subagent_history_start_ordinal":20`, `"subagent_history_start_ordinal":`+boundary, 1))
		if len(d.Messages) != 2 {
			t.Fatalf("boundary %s lost activity", boundary)
		}
	}
}

func TestDiscoveryAndCancellation(t *testing.T) {
	root := t.TempDir()
	src := codexsource.New(nil)
	ctx := context.Background()
	for _, rel := range []string{"sessions/2026/01/01/rollout-a.jsonl", "archived_sessions/rollout-a.jsonl", "sessions/unrelated.jsonl"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "archived_sessions"), filepath.Join(root, "sessions", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "archived_sessions/rollout-a.jsonl"), filepath.Join(root, "sessions/rollout-link.jsonl")); err != nil {
		t.Fatal(err)
	}
	ps, err := src.List(ctx, root)
	if err != nil || len(ps) != 2 || !strings.Contains(ps[0], "archived_sessions") {
		t.Fatalf("paths=%v err=%v", ps, err)
	}
	if ps, err := src.List(ctx, filepath.Join(root, "missing")); err != nil || len(ps) != 0 {
		t.Fatal(ps, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := src.List(canceled, root); err == nil {
		t.Fatal("List ignored cancellation")
	}
	if _, _, err := src.Parse(canceled, ps[0]); err == nil {
		t.Fatal("Parse ignored cancellation")
	}
	if _, _, err := src.Parse(ctx, filepath.Join(root, "absent")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestResultAndCallDuplicatesKeepFirst(t *testing.T) {
	doc, stats := parseText(t, `{"type":"response_item","payload":{"type":"function_call","id":"a","call_id":"x","name":"first","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call","id":"b","call_id":"x","name":"second","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"x","output":"first"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"x","output":"second"}}`)
	if len(doc.ToolCalls) != 1 || doc.ToolCalls[0].ToolName != "first" || doc.ToolCalls[0].Output != "first" || stats.Skipped["duplicate_record"] != 2 {
		t.Fatal(doc.ToolCalls, stats)
	}
	b, err := json.Marshal(doc)
	if err != nil || !json.Valid(b) {
		t.Fatal(err)
	}
}

func TestUnusableFileDoesNotInventSession(t *testing.T) {
	doc, stats := parseText(t, `{"type":"future","payload":{}}
{"type":"event_msg","payload":{"type":"token_count"}}`)
	if doc.Session.SessionID != "" || len(doc.Messages) != 0 || stats.Skipped[model.SkipUnknownRecordType] != 1 {
		t.Fatalf("doc=%+v stats=%+v", doc, stats)
	}
}
