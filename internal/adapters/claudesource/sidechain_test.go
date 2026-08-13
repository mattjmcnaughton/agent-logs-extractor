package claudesource_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// firstRecordUUID returns the uuid of the first record in path that has
// one — used to identify a fixture's sidechain-root message by its actual
// uuid rather than a hardcoded literal, so the test stays correct across a
// fixture regeneration (which changes every uuid).
func firstRecordUUID(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var probe struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		if probe.UUID != "" {
			return probe.UUID
		}
	}
	t.Fatalf("%s: no record with a uuid found", path)
	return ""
}

// TestSidechainOrderAndParentLink pins the merged seq order the sidechain
// fixture must produce (§C.4 of the ticket plan: chronological merge,
// parent and subagent records interleaved by timestamp, never grouped by
// file) and the sidechain root's parent_message_id (§C.5: it hangs off the
// Agent tool call's message, not off any parentUuid found inline).
func TestSidechainOrderAndParentLink(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	sessionPath, err := logfixture.SessionFile(logfixture.ClaudeSidechainProject)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := src.Parse(ctx, sessionPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Messages) != 7 {
		t.Fatalf("got %d messages, want 7", len(doc.Messages))
	}

	wantRoles := []model.Role{
		model.RoleUser,      // 0: parent user prompt
		model.RoleAssistant, // 1: parent thinking-only turn
		model.RoleAssistant, // 2: parent's Agent tool_use
		model.RoleUser,      // 3: sidechain root (flattened)
		model.RoleAssistant, // 4: sidechain's Bash tool_use
		model.RoleAssistant, // 5: sidechain's closing text
		model.RoleAssistant, // 6: parent's "done"
	}
	for i, m := range doc.Messages {
		if m.Seq != i {
			t.Errorf("message %d: Seq = %d, want %d", i, m.Seq, i)
		}
		if m.Role != wantRoles[i] {
			t.Errorf("message %d: Role = %s, want %s", i, m.Role, wantRoles[i])
		}
	}
	for i := 1; i < len(doc.Messages); i++ {
		if doc.Messages[i].CreatedAt.Before(doc.Messages[i-1].CreatedAt) {
			t.Errorf("message %d created_at %s sorts before message %d's %s", i, doc.Messages[i].CreatedAt, i-1, doc.Messages[i-1].CreatedAt)
		}
	}

	var agentToolCall *model.ToolCall
	for i := range doc.ToolCalls {
		if doc.ToolCalls[i].ToolName == "Agent" {
			agentToolCall = &doc.ToolCalls[i]
		}
	}
	if agentToolCall == nil {
		t.Fatal("expected an Agent tool call")
	}

	subFiles, err := logfixture.SubagentFiles(logfixture.ClaudeSidechainProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(subFiles) != 1 {
		t.Fatalf("expected exactly one subagent file, got %d", len(subFiles))
	}
	rootMsgID := "claude:" + firstRecordUUID(t, subFiles[0])

	var root *model.Message
	for i := range doc.Messages {
		if doc.Messages[i].MessageID == rootMsgID {
			root = &doc.Messages[i]
		}
	}
	if root == nil {
		t.Fatalf("sidechain root message %s not found", rootMsgID)
	}
	if root.Seq != 3 {
		t.Errorf("sidechain root Seq = %d, want 3", root.Seq)
	}
	if root.ParentMessageID != agentToolCall.MessageID {
		t.Errorf("sidechain root ParentMessageID = %q, want %q (the Agent tool call's message id)", root.ParentMessageID, agentToolCall.MessageID)
	}
}

// copySidechainFixture copies the sidechain fixture's whole project
// directory (parent transcript, subagents/ transcript, and .meta.json
// sidecar) into a fresh temp directory, so a test can mutate the copy —
// removing the sidecar, breaking the agentId join, adding a second
// subagent transcript — without ever touching the committed fixture. It
// returns the copied session file's path and its subagents/ directory.
func copySidechainFixture(t *testing.T) (sessionPath, subagentDir string) {
	t.Helper()

	srcSession, err := logfixture.SessionFile(logfixture.ClaudeSidechainProject)
	if err != nil {
		t.Fatal(err)
	}
	srcProjDir := filepath.Dir(srcSession)
	dstProjDir := filepath.Join(t.TempDir(), filepath.Base(srcProjDir))

	err = filepath.WalkDir(srcProjDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcProjDir, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstProjDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying sidechain fixture tree: %v", err)
	}

	sessionPath = filepath.Join(dstProjDir, filepath.Base(srcSession))
	subagentDir = filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
	return sessionPath, subagentDir
}

// breakAgentIDJoin rewrites every toolUseResult.agentId in parentPath to a
// value that cannot match any real subagent, so the agentIDToolUseID
// fallback join is guaranteed to miss. Each record is decoded and
// re-encoded as a generic map, which changes byte formatting but not
// meaning — acceptable here since this helper is only ever used against a
// throwaway copy, never compared byte-for-byte against anything.
func breakAgentIDJoin(t *testing.T, parentPath string) {
	t.Helper()

	data, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatalf("reading %s: %v", parentPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("%s:%d: not a JSON object: %v", parentPath, i+1, err)
		}
		turRaw, ok := rec["toolUseResult"]
		if !ok {
			continue
		}
		var tur map[string]json.RawMessage
		if err := json.Unmarshal(turRaw, &tur); err != nil {
			// String or absent shapes: nothing to break here.
			continue
		}
		if _, ok := tur["agentId"]; !ok {
			continue
		}
		tur["agentId"] = json.RawMessage(`"no-such-agent"`)
		newTur, err := json.Marshal(tur)
		if err != nil {
			t.Fatal(err)
		}
		rec["toolUseResult"] = newTur
		newLine, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = string(newLine)
	}

	if err := os.WriteFile(parentPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", parentPath, err)
	}
}

// TestSidechainLinkFallbacks exercises the three-tier link chain (§C.5,
// §D4): with the .meta.json sidecar removed, resolution falls back to the
// toolUseResult.agentId join and still finds the same parent message; with
// both links broken, parent_message_id is simply empty and Parse still
// succeeds.
func TestSidechainLinkFallbacks(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	agentToolCallMessageID := func(doc model.SessionDoc) string {
		for _, tc := range doc.ToolCalls {
			if tc.ToolName == "Agent" {
				return tc.MessageID
			}
		}
		return ""
	}

	rootMessage := func(t *testing.T, doc model.SessionDoc, rootMsgID string) *model.Message {
		t.Helper()
		for i := range doc.Messages {
			if doc.Messages[i].MessageID == rootMsgID {
				return &doc.Messages[i]
			}
		}
		t.Fatalf("sidechain root message %s not found", rootMsgID)
		return nil
	}

	t.Run("meta.json missing falls back to agentId join", func(t *testing.T) {
		sessionPath, subDir := copySidechainFixture(t)
		metas, err := filepath.Glob(filepath.Join(subDir, "*.meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(metas) == 0 {
			t.Fatal("expected at least one .meta.json sidecar to remove")
		}
		for _, m := range metas {
			if err := os.Remove(m); err != nil {
				t.Fatal(err)
			}
		}
		subFiles, err := filepath.Glob(filepath.Join(subDir, "*.jsonl"))
		if err != nil || len(subFiles) != 1 {
			t.Fatalf("expected exactly one subagent transcript, got %v (err %v)", subFiles, err)
		}
		rootMsgID := "claude:" + firstRecordUUID(t, subFiles[0])

		doc, _, err := src.Parse(ctx, sessionPath)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}

		agentMsgID := agentToolCallMessageID(doc)
		if agentMsgID == "" {
			t.Fatal("expected an Agent tool call")
		}
		root := rootMessage(t, doc, rootMsgID)
		if root.ParentMessageID != agentMsgID {
			t.Errorf("ParentMessageID = %q, want %q (agentId-join fallback)", root.ParentMessageID, agentMsgID)
		}
	})

	t.Run("both links unresolved leaves parent_message_id empty", func(t *testing.T) {
		sessionPath, subDir := copySidechainFixture(t)
		metas, err := filepath.Glob(filepath.Join(subDir, "*.meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range metas {
			if err := os.Remove(m); err != nil {
				t.Fatal(err)
			}
		}
		subFiles, err := filepath.Glob(filepath.Join(subDir, "*.jsonl"))
		if err != nil || len(subFiles) != 1 {
			t.Fatalf("expected exactly one subagent transcript, got %v (err %v)", subFiles, err)
		}
		rootMsgID := "claude:" + firstRecordUUID(t, subFiles[0])

		breakAgentIDJoin(t, sessionPath)

		doc, _, err := src.Parse(ctx, sessionPath)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}

		root := rootMessage(t, doc, rootMsgID)
		if root.ParentMessageID != "" {
			t.Errorf("ParentMessageID = %q, want empty", root.ParentMessageID)
		}
	})
}
