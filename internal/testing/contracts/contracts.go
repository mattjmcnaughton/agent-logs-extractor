// Package contracts checks live source assumptions without retaining or reporting
// source content. Ordinary tests exercise this checker with invented records.
package contracts

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/invariants"
)

// Report contains only counts and checker-defined labels, never source values.
// Observed counts are coverage, not a claim that unobserved shapes are valid.
type Report struct {
	Files, Sessions, Messages, Tools int
	Observed, Drift, Failures        map[string]int
}

func Check(ctx context.Context, src ports.ConversationSource, root string) (r Report) {
	r.Observed = map[string]int{}
	r.Drift = map[string]int{}
	r.Failures = map[string]int{}
	// A vendor panic may embed private input. Never format the recovered value.
	defer func() {
		if recover() != nil {
			r.Failures["source_panic"]++
		}
	}()
	if vendor := src.Vendor(); vendor != model.VendorClaude && vendor != model.VendorCodex {
		r.Failures["unsupported_vendor"]++
		return
	}
	paths, err := src.List(ctx, root)
	if err != nil {
		r.Failures["list_error"]++
		return
	}
	r.Files = len(paths)
	docs := map[string]model.SessionDoc{}
	for _, p := range paths {
		d, s, err := src.Parse(ctx, p)
		if err != nil {
			r.Failures["parse_error"]++
			continue
		}
		for _, v := range invariants.Check(d) {
			r.Failures[v.Kind]++
		}
		for _, o := range invariants.Observe(d) {
			r.Drift[o.Kind] += o.Count
		}
		for reason, n := range s.Skipped {
			switch reason {
			case model.SkipBookkeeping: // Expected non-conversation records.
			case "inherited_history":
				r.Observed["inherited_history"] += n
			default:
				r.Drift["skipped_records_requiring_review"] += n
			}
		}
		files := []string{p}
		if src.Vendor() == model.VendorClaude {
			children, e := filepath.Glob(filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents", "*.jsonl"))
			if e != nil {
				r.Failures["child_discovery_error"]++
			}
			files = append(files, children...)
			r.Observed["sidechain_files"] += len(children)
		}
		inspect(files, d, src.Vendor(), &r)
		if d.Session.SessionID != "" {
			if _, ok := docs[d.Session.SessionID]; ok {
				r.Drift["duplicate_session_id"]++
			}
			docs[d.Session.SessionID] = d
		}
		if strings.Contains(filepath.ToSlash(p), "/archived_sessions/") {
			r.Observed["archive_files"]++
		}
	}
	seen := map[string]bool{}
	for _, d := range docs {
		r.Sessions++
		r.Messages += len(d.Messages)
		r.Tools += len(d.ToolCalls)
		for _, m := range d.Messages {
			key := "message:" + m.MessageID
			if seen[key] {
				r.Failures["cross_session_message_id"]++
			}
			seen[key] = true
		}
		for _, c := range d.ToolCalls {
			key := "tool:" + c.ToolCallID
			if seen[key] {
				r.Failures["cross_session_tool_id"]++
			}
			seen[key] = true
		}
	}
	if r.Files > 0 && (r.Sessions == 0 || r.Messages == 0) {
		r.Failures["total_ingestion_loss"]++
	}
	return
}

// RunLocal is the opt-in test entrypoint. Even an explicitly configured path is
// ignored in CI. Source constructors must use a discard logger.
func RunLocal(t *testing.T, src ports.ConversationSource, env string) {
	t.Helper()
	if _, ci := os.LookupEnv("CI"); ci {
		t.Skip("live contracts are local-only; CI is set")
	}
	root := os.Getenv(env)
	if root == "" {
		t.Skip("explicit contract path is unset; no live data read")
	}
	r := Check(context.Background(), src, root)
	t.Logf("files=%d sessions=%d messages=%d tool_calls=%d", r.Files, r.Sessions, r.Messages, r.Tools)
	labels := []string{"session_metadata", "user_message", "assistant_message", "system_message", "tool_call", "tool_result_join"}
	if src.Vendor() == model.VendorClaude {
		labels = append(labels, "sidechain_files", "error_result")
	} else if src.Vendor() == model.VendorCodex {
		labels = append(labels, "function_call", "custom_tool_call", "mirrored_event", "inherited_history", "archive_files")
	}
	for _, label := range labels {
		if r.Observed[label] == 0 {
			t.Logf("NOT_OBSERVED %s", label)
		} else {
			t.Logf("OBSERVED %s=%d", label, r.Observed[label])
		}
	}
	logCounts := func(prefix string, counts map[string]int) {
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("%s %s=%d", prefix, k, counts[k])
		}
	}
	logCounts("REVIEW", r.Drift)
	logCounts("FAIL", r.Failures)
	if len(r.Failures) > 0 {
		t.Fatal("contract assumptions violated; diagnostics contain counts only")
	}
	if r.Files == 0 {
		t.Skip("no source files; compatibility remains unverified")
	}
	if len(r.Drift) > 0 {
		t.Log("PARTIAL: review drift locally before claiming compatibility")
	}
}

// Inspect independently recognizes the documented record envelopes and checks
// that source message bytes survive normalization and paired results resolve.
// It deliberately does not reproduce every normalization rule.
type record struct {
	Type, UUID, SessionID string
	Ordinal               *int64
	Payload               json.RawMessage
	Message               *struct{ Content json.RawMessage }
	Content               json.RawMessage
}
type payload struct {
	Type, ID, Role, Status string
	CallID                 string `json:"call_id"`
	Boundary               *int64 `json:"subagent_history_start_ordinal"`
	Content                json.RawMessage
	Output                 json.RawMessage
	IsError                bool `json:"is_error"`
}

type inspection struct {
	report         *Report
	rawMessages    map[string]model.Message
	toolsByMessage map[string][]model.ToolCall
	calls          map[string]model.ToolCall
	results        map[string]bool
	seen           map[string]bool
	boundary       *int64
	ownerSeen      bool
}

func inspect(paths []string, doc model.SessionDoc, vendor model.Vendor, r *Report) {
	in := inspection{report: r, rawMessages: map[string]model.Message{}, toolsByMessage: map[string][]model.ToolCall{}, calls: map[string]model.ToolCall{}, results: map[string]bool{}, seen: map[string]bool{}}
	for _, m := range doc.Messages {
		in.rawMessages[string(m.Raw)] = m
	}
	for _, c := range doc.ToolCalls {
		in.toolsByMessage[c.MessageID] = append(in.toolsByMessage[c.MessageID], c)
	}
	for _, path := range paths {
		in.read(path, vendor)
	}
	for id, failed := range in.results {
		c, ok := in.calls[id]
		if !ok {
			r.Drift["unmatched_result"]++
			continue
		}
		want := model.StatusOK
		if failed {
			want = model.StatusError
			r.Observed["error_result"]++
		}
		if c.Status != want {
			r.Failures["tool_result_join"]++
		} else {
			r.Observed["tool_result_join"]++
		}
	}
}

func (in *inspection) read(path string, vendor model.Vendor) {
	f, err := os.Open(path)
	if err != nil {
		in.report.Failures["source_read_error"]++
		return
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	for {
		line, e := reader.ReadBytes('\n')
		raw := strings.TrimRight(string(line), "\r\n")
		if strings.TrimSpace(raw) != "" {
			var rec record
			if json.Unmarshal([]byte(raw), &rec) != nil {
				in.report.Drift["malformed_source_record"]++
			} else if vendor == model.VendorCodex {
				in.codex(rec, raw)
			} else {
				in.claude(rec, raw)
			}
		}
		if e != nil {
			if e != io.EOF {
				in.report.Failures["source_read_error"]++
			}
			return
		}
	}
}

func (in *inspection) codex(rec record, raw string) {
	r := in.report
	var p payload
	if json.Unmarshal(rec.Payload, &p) != nil {
		r.Drift["malformed_payload"]++
		return
	}
	if rec.Type == "session_meta" && !in.ownerSeen {
		in.ownerSeen = true
		in.boundary = p.Boundary
		if p.ID != "" {
			r.Observed["session_metadata"]++
		}
	}
	if in.boundary != nil && rec.Ordinal != nil && *rec.Ordinal < *in.boundary {
		if _, ok := in.rawMessages[raw]; ok {
			r.Failures["inherited_message_emitted"]++
		}
		return
	}
	if rec.Type == "event_msg" && (p.Type == "user_message" || p.Type == "agent_message") {
		r.Observed["mirrored_event"]++
		if _, ok := in.rawMessages[raw]; ok {
			r.Failures["mirrored_event_emitted"]++
		}
		return
	}
	if rec.Type != "response_item" {
		return
	}
	// Only usable outputs participate in first-wins deduplication and joins.
	if p.Type == "function_call_output" || p.Type == "custom_tool_call_output" {
		if p.CallID == "" || len(p.Output) == 0 || string(p.Output) == "null" {
			r.Drift["unusable_tool_result"]++
			return
		}
	}
	if p.ID != "" {
		if in.seen[p.ID] {
			return
		}
		in.seen[p.ID] = true
	}
	switch p.Type {
	case "message", "agent_message":
		role := p.Role
		if role == "developer" || p.Type == "agent_message" {
			role = "system"
		}
		r.expectMessage(raw, role, in.rawMessages)
		checkBlocks(p.Content, []string{"input_text", "output_text"}, r)
	case "function_call", "custom_tool_call":
		r.Observed[p.Type]++
		if _, duplicate := in.calls[p.CallID]; p.CallID != "" && duplicate {
			return
		}
		m, ok := r.expectMessage(raw, "assistant", in.rawMessages)
		if ok && len(in.toolsByMessage[m.MessageID]) == 1 {
			in.calls[p.CallID] = in.toolsByMessage[m.MessageID][0]
			r.Observed["tool_call"]++
		} else {
			r.Failures["tool_call_missing"]++
		}
	case "function_call_output", "custom_tool_call_output":
		if _, duplicate := in.results[p.CallID]; duplicate {
			return
		}
		in.results[p.CallID] = p.IsError || p.Status == "error" || p.Status == "failed"
	}
}

func (in *inspection) claude(rec record, raw string) {
	if rec.Type != "user" && rec.Type != "assistant" && rec.Type != "system" {
		return
	}
	r := in.report
	if rec.SessionID != "" {
		r.Observed["session_metadata"]++
	}
	if rec.UUID == "" {
		r.Drift["conversation_without_id"]++
		return
	}
	if rec.Message == nil && len(rec.Content) == 0 {
		r.Drift["conversation_without_content"]++
		return
	}
	content := rec.Content
	if rec.Message != nil {
		content = rec.Message.Content
	}
	var blocks []struct {
		Type, ID  string
		ToolUseID string `json:"tool_use_id"`
		IsError   bool   `json:"is_error"`
	}
	_ = json.Unmarshal(content, &blocks)
	isResult := false
	for _, b := range blocks {
		if b.Type == "tool_result" {
			isResult = true
			in.results[b.ToolUseID] = b.IsError
		}
	}
	if isResult {
		for _, b := range blocks {
			if b.Type != "tool_result" {
				r.Drift["mixed_result_content"]++
			}
		}
		return
	}
	if in.seen[rec.UUID] {
		return
	}
	in.seen[rec.UUID] = true
	m, ok := r.expectMessage(raw, rec.Type, in.rawMessages)
	if ok {
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			found := false
			for _, c := range in.toolsByMessage[m.MessageID] {
				if c.ToolCallID == "claude:"+b.ID {
					in.calls[b.ID] = c
					found = true
					r.Observed["tool_call"]++
				}
			}
			if !found {
				r.Failures["tool_call_missing"]++
			}
		}
	}
	if len(blocks) > 0 {
		checkBlocks(content, []string{"text", "thinking", "redacted_thinking", "tool_use", "image", "document"}, r)
	}
}
func (r *Report) expectMessage(raw, role string, messages map[string]model.Message) (model.Message, bool) {
	m, ok := messages[raw]
	if !ok {
		r.Failures["source_message_missing"]++
		return m, false
	}
	if string(m.Role) != role {
		r.Failures["message_role"]++
	}
	switch role {
	case "user", "assistant", "system":
		r.Observed[role+"_message"]++
	}
	return m, true
}
func checkBlocks(raw json.RawMessage, known []string, r *Report) {
	var blocks []struct{ Type string }
	if json.Unmarshal(raw, &blocks) != nil {
		r.Drift["content_shape"]++
		return
	}
	for _, b := range blocks {
		ok := false
		for _, k := range known {
			if b.Type == k {
				ok = true
			}
		}
		if !ok {
			r.Drift["unknown_content_block"]++
		}
	}
}
