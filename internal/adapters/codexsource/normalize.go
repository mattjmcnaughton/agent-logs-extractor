package codexsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

type payload struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Role      string `json:"role"`
	CWD       string `json:"cwd"`
	Version   string `json:"cli_version"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	Git       struct {
		Branch string `json:"branch"`
	} `json:"git"`
	Boundary  json.RawMessage `json:"subagent_history_start_ordinal"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Status    string          `json:"status"`
	IsError   bool            `json:"is_error"`
}

func normalize(ctx context.Context, filename string, records []record, stats model.ParseStats) (model.SessionDoc, model.ParseStats, error) {
	var doc model.SessionDoc
	if len(records) == 0 {
		return doc, stats, nil
	}
	var owner payload
	var boundary *int64
	for _, r := range records {
		if r.Type != "session_meta" {
			continue
		}
		var p payload
		if json.Unmarshal(r.Payload, &p) != nil {
			continue
		}
		owner = p
		var n int64
		if len(p.Boundary) > 0 && string(p.Boundary) != "null" && json.Unmarshal(p.Boundary, &n) == nil && n >= 0 {
			boundary = &n
		}
		break
	}
	thread := owner.ID
	if thread == "" {
		thread = strings.TrimSuffix(filepath.Base(filename), ".jsonl")
		// Rollout timestamps are fixed-width; keep the complete suffix, not a
		// guessed UUID assembled by splitting its hyphens.
		if strings.HasPrefix(thread, "rollout-") && len(thread) > 28 {
			thread = thread[28:]
		}
	}
	doc.Session = model.Session{SessionID: "codex:" + thread, Vendor: model.VendorCodex, ProjectPath: owner.CWD, VendorVersion: owner.Version, GitBranch: owner.Git.Branch, SourcePath: filename, StartedAt: stamp(owner.Timestamp)}
	if owner.CWD != "" {
		doc.Session.ProjectName = path.Base(owner.CWD)
	}
	prefix := "codex:" + url.QueryEscape(thread)
	seen := map[string]bool{}
	calls := map[string]int{}
	outputs := map[string]payload{}
	currentModel := ""
	skip := func(reason model.SkipReason) { stats.Skipped[reason]++ }
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return model.SessionDoc{}, stats, err
		}
		var p payload
		if json.Unmarshal(r.Payload, &p) != nil {
			skip(model.SkipMalformedLine)
			continue
		}
		if r.Type == "turn_context" && p.Model != "" {
			currentModel = p.Model
		}
		if boundary != nil && r.Ordinal != nil && *r.Ordinal < *boundary {
			skip("inherited_history")
			continue
		}
		ts := stamp(r.Timestamp)
		if !ts.IsZero() {
			if doc.Session.StartedAt.IsZero() {
				doc.Session.StartedAt = ts
			}
			if ts.After(doc.Session.EndedAt) {
				doc.Session.EndedAt = ts
			}
		}
		if r.Type != "response_item" {
			switch r.Type {
			case "session_meta", "turn_context", "world_state", "token_usage_record", "inter_agent_communication_metadata", "compacted":
				skip(model.SkipBookkeeping)
			case "event_msg":
				switch p.Type {
				case "item_completed", "token_count", "task_started", "task_complete", "thread_settings_applied", "user_message", "agent_message", "turn_aborted":
					skip(model.SkipBookkeeping)
				default:
					skip(model.SkipUnknownRecordType)
				}
			default:
				skip(model.SkipUnknownRecordType)
			}
			continue
		}
		key := "id:" + url.QueryEscape(p.ID)
		if p.ID == "" {
			key = fmt.Sprintf("line:%d", r.line)
		}
		if seen[key] {
			skip("duplicate_record")
			continue
		}
		switch p.Type {
		case "message", "agent_message":
			role := model.Role(p.Role)
			if p.Type == "agent_message" || role == "developer" {
				role = model.RoleSystem
			}
			if role != model.RoleUser && role != model.RoleAssistant && role != model.RoleSystem {
				skip(model.SkipUnknownRecordType)
				continue
			}
			text, ok := messageText(p.Content)
			if !ok {
				skip(model.SkipMalformedLine)
				continue
			}
			m := model.Message{MessageID: prefix + ":message:" + key, SessionID: doc.Session.SessionID, Seq: len(doc.Messages), Role: role, CreatedAt: ts, Text: text, Raw: r.raw}
			if role == model.RoleAssistant {
				m.Model = currentModel
			}
			doc.Messages = append(doc.Messages, m)
			seen[key] = true
		case "function_call", "custom_tool_call":
			if p.Name == "" {
				skip(model.SkipMalformedLine)
				continue
			}
			callKey := p.CallID
			if callKey == "" {
				callKey = "unmatched:" + key
			} else {
				callKey = "id:" + url.QueryEscape(callKey)
			}
			if _, exists := calls[callKey]; exists {
				skip("duplicate_record")
				continue
			}
			args := p.Arguments
			if p.Type == "custom_tool_call" {
				args = p.Input
			} else {
				var s string
				if json.Unmarshal(args, &s) == nil && json.Valid([]byte(s)) {
					args = json.RawMessage(s)
				}
			}
			if len(args) == 0 {
				args = nil
			}
			mid := prefix + ":message:" + key
			doc.Messages = append(doc.Messages, model.Message{MessageID: mid, SessionID: doc.Session.SessionID, Seq: len(doc.Messages), Role: model.RoleAssistant, CreatedAt: ts, Model: currentModel, Raw: r.raw})
			calls[callKey] = len(doc.ToolCalls)
			doc.ToolCalls = append(doc.ToolCalls, model.ToolCall{ToolCallID: prefix + ":tool:" + callKey, SessionID: doc.Session.SessionID, MessageID: mid, Seq: len(doc.ToolCalls), ToolName: p.Name, Arguments: args, Status: model.StatusPending, CreatedAt: ts})
			seen[key] = true
		case "function_call_output", "custom_tool_call_output":
			if p.CallID == "" || len(p.Output) == 0 || string(p.Output) == "null" {
				skip(model.SkipMalformedLine)
				continue
			}
			callKey := "id:" + url.QueryEscape(p.CallID)
			if _, exists := outputs[callKey]; exists {
				skip("duplicate_record")
				continue
			}
			outputs[callKey] = p
			seen[key] = true
		case "reasoning", "compaction":
			skip(model.SkipBookkeeping)
		default:
			skip(model.SkipUnknownRecordType)
		}
	}
	for key, p := range outputs {
		index, ok := calls[key]
		if !ok {
			skip("orphan_tool_result")
			continue
		}
		call := &doc.ToolCalls[index]
		if json.Unmarshal(p.Output, &call.Output) != nil {
			call.Output = string(p.Output)
		}
		call.Status = model.StatusOK
		if p.IsError || p.Status == "failed" || p.Status == "error" {
			call.Status = model.StatusError
		}
	}
	if owner.ID == "" && owner.CWD == "" && owner.Version == "" && len(doc.Messages) == 0 {
		return model.SessionDoc{}, stats, nil
	}
	if doc.Session.EndedAt.IsZero() {
		doc.Session.EndedAt = doc.Session.StartedAt
	}
	return doc, stats, nil
}
func stamp(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func messageText(raw json.RawMessage) (string, bool) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "input_text" || b.Type == "output_text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), true
}
