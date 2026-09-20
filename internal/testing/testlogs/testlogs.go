// Package testlogs writes small, invented vendor records into test-owned
// temporary directories. It contains no captured logs or private source data.
// These examples specify parser behavior; only local opt-in contract checks
// provide evidence that live vendors still follow those assumptions.
package testlogs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ClaudeSidechainProject = "-home-user-sidechain"
const ClaudeToolErrorProject = "-home-user-tool-error"
const ClaudeSingleProject = "-home-user-demo"

func write(t testing.TB, path string, records ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	b := strings.Join(records, "\n")
	if len(records) > 0 {
		b += "\n"
	}
	if err := os.WriteFile(path, []byte(b), 0600); err != nil {
		t.Fatal(err)
	}
}

func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func claude(session, id, parent, stamp, role string, content any, extra map[string]any) string {
	project := map[string]string{"single": "demo", "parent": "sidechain", "failure": "tool-error"}[session]
	var parentID any
	if parent != "" {
		parentID = parent
	}
	r := map[string]any{"type": role, "uuid": id, "parentUuid": parentID, "sessionId": session, "cwd": "/home/user/" + project, "gitBranch": "main", "version": "test", "timestamp": "2020-01-01T" + stamp + "Z", "message": map[string]any{"role": role, "model": "test-model", "content": content}}
	for k, v := range extra {
		r[k] = v
	}
	return encode(r)
}

func tool(id, name, command, description string) any {
	return []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{"command": command, "description": description}, "caller": map[string]any{"type": "direct"}}}
}
func result(id, output string, failed bool) any {
	return []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": output, "is_error": failed}}
}

const bookkeeping = `{"type":"queue-operation","operation":"enqueue"}`

func single(session, clock, command, output string, failed bool) []string {
	id := session + "-tool"
	return []string{
		bookkeeping,
		claude(session, session+"-user", "", clock+":00", "user", "run the example", nil),
		claude(session, session+"-thinking", session+"-user", clock+":01", "assistant", []any{map[string]any{"type": "thinking", "thinking": "example"}}, nil),
		claude(session, session+"-call", session+"-thinking", clock+":02", "assistant", tool(id, "Bash", command, "Example command"), nil),
		claude(session, session+"-result", session+"-call", clock+":03", "user", result(id, output, failed), nil),
		claude(session, session+"-done", session+"-result", clock+":04", "assistant", "done", nil),
	}
}

// Dir creates one isolated corpus. Reuse the returned root when a test needs
// to mutate it or compare multiple reads of the exact same source paths.
func Dir(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "claude", "projects")
	one := single("single", "00:00", "echo hello fixture", "hello fixture", false)
	write(t, filepath.Join(base, ClaudeSingleProject, "single.jsonl"), one...)
	write(t, filepath.Join(base, ClaudeToolErrorProject, "failure.jsonl"), single("failure", "13:00", "cat /nonexistent-example", "missing file", true)...)
	parent := []string{
		bookkeeping,
		claude("parent", "parent-user", "", "12:00:00", "user", "parent prompt", nil),
		claude("parent", "parent-thinking", "parent-user", "12:00:01", "assistant", []any{map[string]any{"type": "thinking", "thinking": "example"}}, nil),
		claude("parent", "parent-call", "parent-thinking", "12:00:02", "assistant", tool("spawn-tool", "Agent", "delegate", "Example delegation"), nil),
		claude("parent", "parent-result", "parent-call", "12:00:07", "user", result("spawn-tool", "child complete", false), map[string]any{"toolUseResult": map[string]any{"agentId": "child-agent"}}),
		claude("parent", "parent-done", "parent-result", "12:00:08", "assistant", "done", nil),
	}
	write(t, filepath.Join(base, ClaudeSidechainProject, "parent.jsonl"), parent...)
	extra := map[string]any{"isSidechain": true, "agentId": "child-agent"}
	write(t, filepath.Join(base, ClaudeSidechainProject, "parent", "subagents", "agent-child-agent.jsonl"),
		claude("parent", "child-user", "", "12:00:03", "user", "child prompt", extra),
		bookkeeping,
		claude("parent", "child-call", "child-user", "12:00:04", "assistant", tool("child-tool", "Bash", "echo hello from subagent", "Example child command"), extra),
		claude("parent", "child-result", "child-call", "12:00:05", "user", result("child-tool", "hello from subagent", false), extra),
		claude("parent", "child-done", "child-result", "12:00:06", "assistant", "done", extra))
	write(t, filepath.Join(base, ClaudeSidechainProject, "parent", "subagents", "agent-child-agent.meta.json"), `{"agentType":"general-purpose","toolUseId":"spawn-tool"}`)
	bad := filepath.Join(root, "pathological", "claude", "projects", "-home-user-demo")
	write(t, filepath.Join(bad, "01-truncated.jsonl"), bookkeeping, one[1], `{"type":`)
	write(t, filepath.Join(bad, "02-unknown.jsonl"), bookkeeping, one[1], `{"type":"future-record-type"}`)
	pending := append(append([]string{}, one[:4]...), one[5])
	write(t, filepath.Join(bad, "03-pending.jsonl"), pending...)
	write(t, filepath.Join(bad, "04-empty.jsonl"))
	codex(t, filepath.Join(root, "codex"))
	return root
}

func ClaudeRoot(t testing.TB) string        { return filepath.Join(Dir(t), "claude") }
func CodexRoot(t testing.TB) string         { return filepath.Join(Dir(t), "codex") }
func ClaudeProjectsDir(t testing.TB) string { return filepath.Join(ClaudeRoot(t), "projects") }
func PathologicalClaudeRoot(t testing.TB) string {
	return filepath.Join(Dir(t), "pathological", "claude")
}
func SessionFile(t testing.TB, project string) (string, error) {
	files, err := filepath.Glob(filepath.Join(ClaudeProjectsDir(t), project, "*.jsonl"))
	if err != nil {
		return "", err
	}
	if len(files) != 1 {
		return "", fmt.Errorf("expected one synthetic session, got %d", len(files))
	}
	return files[0], nil
}
func SubagentFiles(t testing.TB, project string) ([]string, error) {
	return filepath.Glob(filepath.Join(ClaudeProjectsDir(t), project, "*", "subagents", "*.jsonl"))
}

func codex(t testing.TB, root string) {
	meta := func(id string) string {
		return `{"type":"session_meta","timestamp":"2020-01-01T00:00:00Z","payload":{"id":"` + id + `","cwd":"/home/user/demo","cli_version":"test"}}`
	}
	user := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"example prompt"}]}}`
	final := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`
	call := `{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"custom","input":"echo example"}}`
	output := `{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"custom","output":"example"}}`
	write(t, filepath.Join(root, "sessions", "rollout-parent.jsonl"), meta("parent"),
		`{"type":"turn_context","payload":{"model":"test-model"}}`, user,
		`{"type":"event_msg","payload":{"type":"user_message","message":"example prompt"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"function","arguments":"{\"cmd\":\"echo example\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"function","output":"example"}}`, call, output, final)
	write(t, filepath.Join(root, "sessions", "rollout-child.jsonl"),
		`{"type":"session_meta","payload":{"id":"child","cwd":"/home/user/demo","cli_version":"test","subagent_history_start_ordinal":2}}`,
		`{"ordinal":0,"type":"session_meta","payload":{"id":"parent"}}`,
		`{"ordinal":1,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inherited"}]}}`,
		user, call, output, final)
	write(t, filepath.Join(root, "archived_sessions", "rollout-archived.jsonl"), meta("archived"), user, call, output, final)
}
