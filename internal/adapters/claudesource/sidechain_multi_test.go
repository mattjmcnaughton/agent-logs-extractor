package claudesource_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// collectIDs calls remember with every uuid/parentUuid/agentId and
// tool_use/tool_result id string found in one JSONL line — the complete
// set of ids a duplicated subagent transcript needs remapped so it never
// collides with the original it was copied from.
func collectIDs(t *testing.T, line string, remember func(string)) {
	t.Helper()
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	for _, key := range []string{"uuid", "parentUuid", "agentId"} {
		if raw, ok := rec[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				remember(s)
			}
		}
	}
	for _, blk := range messageBlocks(rec) {
		for _, key := range []string{"id", "tool_use_id"} {
			if raw, ok := blk[key]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					remember(s)
				}
			}
		}
	}
}

// messageBlocks returns rec's message.content array decoded generically —
// each block as its own field map — or nil if rec has no message, or its
// content isn't a block array (e.g. a plain string, which carries no ids
// to remap).
func messageBlocks(rec map[string]json.RawMessage) []map[string]json.RawMessage {
	msgRaw, ok := rec["message"]
	if !ok {
		return nil
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return nil
	}
	contentRaw, ok := msg["content"]
	if !ok {
		return nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// remapIDs rewrites every id in idMap's domain, wherever collectIDs would
// have found it, to idMap's corresponding value; an id with no entry in
// idMap is left untouched.
func remapIDs(t *testing.T, line string, idMap map[string]string) string {
	t.Helper()
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}

	remapField := func(key string) {
		raw, ok := rec[key]
		if !ok {
			return
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return
		}
		newS, ok := idMap[s]
		if !ok {
			return
		}
		nb, err := json.Marshal(newS)
		if err != nil {
			t.Fatal(err)
		}
		rec[key] = nb
	}
	remapField("uuid")
	remapField("parentUuid")
	remapField("agentId")

	if blocks := messageBlocks(rec); blocks != nil {
		for _, blk := range blocks {
			for _, key := range []string{"id", "tool_use_id"} {
				raw, ok := blk[key]
				if !ok {
					continue
				}
				var s string
				if json.Unmarshal(raw, &s) != nil {
					continue
				}
				newS, ok := idMap[s]
				if !ok {
					continue
				}
				nb, err := json.Marshal(newS)
				if err != nil {
					t.Fatal(err)
				}
				blk[key] = nb
			}
		}
		newContent, err := json.Marshal(blocks)
		if err != nil {
			t.Fatal(err)
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(rec["message"], &msg); err != nil {
			t.Fatal(err)
		}
		msg["content"] = newContent
		newMsg, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		rec["message"] = newMsg
	}

	newLine, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(newLine)
}

// shiftTimestamp adds offset to line's top-level "timestamp" field (a
// no-op if the record has none or it doesn't parse), so a duplicated
// transcript's records land at a different point in the merge order than
// the original's rather than tying with them.
func shiftTimestamp(t *testing.T, line string, offset time.Duration) string {
	t.Helper()
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	raw, ok := rec["timestamp"]
	if !ok {
		return line
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return line
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return line
	}
	newS, err := json.Marshal(ts.Add(offset).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	rec["timestamp"] = newS
	newLine, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(newLine)
}

// duplicateSubagentTranscript copies srcJSONL into subDir under a new
// agent-<agentId>.jsonl filename, remapping every uuid/parentUuid/agentId
// and tool_use/tool_result id it finds (suffixed with suffix) so the
// duplicate never collides with the original's rows, and shifting every
// record's timestamp by offset so the two transcripts genuinely interleave
// in the merge rather than tie. It returns the new file's path and its
// (remapped) root uuid.
func duplicateSubagentTranscript(t *testing.T, subDir, srcJSONL, suffix string, offset time.Duration) (path, rootUUID string) {
	t.Helper()

	data, err := os.ReadFile(srcJSONL)
	if err != nil {
		t.Fatalf("reading %s: %v", srcJSONL, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	idMap := map[string]string{}
	remember := func(s string) {
		if s == "" {
			return
		}
		if _, ok := idMap[s]; !ok {
			idMap[s] = s + suffix
		}
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		collectIDs(t, line, remember)
	}

	var out []string
	var newAgentID string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		newLine := remapIDs(t, line, idMap)
		newLine = shiftTimestamp(t, newLine, offset)
		out = append(out, newLine)

		if newAgentID == "" {
			var probe struct {
				AgentID string `json:"agentId"`
			}
			if json.Unmarshal([]byte(newLine), &probe) == nil && probe.AgentID != "" {
				newAgentID = probe.AgentID
			}
		}
	}
	if newAgentID == "" {
		t.Fatal("duplicateSubagentTranscript: no agentId found to name the duplicate file after")
	}

	newPath := filepath.Join(subDir, "agent-"+newAgentID+".jsonl")
	if err := os.WriteFile(newPath, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", newPath, err)
	}

	origRootUUID := firstRecordUUID(t, srcJSONL)
	return newPath, idMap[origRootUUID]
}

// duplicateMetaSidecar writes dupJSONL's .meta.json sidecar as a byte-copy
// of origMeta's: the duplicate subagent resolves to the exact same parent
// Agent tool call as the original, by construction, which is what this
// test wants to prove both roots can do independently.
func duplicateMetaSidecar(t *testing.T, origMeta, dupJSONL string) {
	t.Helper()
	data, err := os.ReadFile(origMeta)
	if err != nil {
		t.Fatalf("reading %s: %v", origMeta, err)
	}
	dupMeta := strings.TrimSuffix(dupJSONL, ".jsonl") + ".meta.json"
	if err := os.WriteFile(dupMeta, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", dupMeta, err)
	}
}

// TestMultipleSubagentTranscriptsInterleave covers the N>1 subagent path
// that addRecord's per-fileRank links/rootSeen bookkeeping exists
// specifically to support (§C.4's stated motivation: a run_in_background
// agent's records interleaving with everything else, which only shows up
// with more than one flattened transcript). It duplicates the sidechain
// fixture's single subagent transcript under a second agentId with its own
// sidecar, and asserts both roots resolve to the parent's Agent tool call
// independently and the merged seq order still tracks CreatedAt across all
// three files.
func TestMultipleSubagentTranscriptsInterleave(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	sessionPath, subDir := copySidechainFixture(t)

	origJSONL, err := filepath.Glob(filepath.Join(subDir, "*.jsonl"))
	if err != nil || len(origJSONL) != 1 {
		t.Fatalf("expected exactly one subagent transcript, got %v (err %v)", origJSONL, err)
	}
	origMeta, err := filepath.Glob(filepath.Join(subDir, "*.meta.json"))
	if err != nil || len(origMeta) != 1 {
		t.Fatalf("expected exactly one .meta.json sidecar, got %v (err %v)", origMeta, err)
	}
	origRootUUID := firstRecordUUID(t, origJSONL[0])

	dupJSONL, dupRootUUID := duplicateSubagentTranscript(t, subDir, origJSONL[0], "-dup", 2*time.Second)
	duplicateMetaSidecar(t, origMeta[0], dupJSONL)

	doc, _, err := src.Parse(ctx, sessionPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
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

	messageByID := func(uuid string) *model.Message {
		id := "claude:" + uuid
		for i := range doc.Messages {
			if doc.Messages[i].MessageID == id {
				return &doc.Messages[i]
			}
		}
		return nil
	}
	origRoot := messageByID(origRootUUID)
	dupRoot := messageByID(dupRootUUID)
	if origRoot == nil || dupRoot == nil {
		t.Fatalf("expected both subagent roots to resolve to messages (orig=%v dup=%v)", origRoot, dupRoot)
	}
	if origRoot.ParentMessageID != agentToolCall.MessageID {
		t.Errorf("original root ParentMessageID = %q, want %q", origRoot.ParentMessageID, agentToolCall.MessageID)
	}
	if dupRoot.ParentMessageID != agentToolCall.MessageID {
		t.Errorf("duplicate root ParentMessageID = %q, want %q", dupRoot.ParentMessageID, agentToolCall.MessageID)
	}
	if origRoot.Seq == dupRoot.Seq {
		t.Errorf("both roots share Seq %d, want distinct rows", origRoot.Seq)
	}

	// True k-way chronological merge: seq must track CreatedAt
	// monotonically across all three transcripts (parent + two
	// subagents), never grouped by file.
	for i := 1; i < len(doc.Messages); i++ {
		if doc.Messages[i].CreatedAt.Before(doc.Messages[i-1].CreatedAt) {
			t.Errorf("message %d created_at %s sorts before message %d's %s", i, doc.Messages[i].CreatedAt, i-1, doc.Messages[i-1].CreatedAt)
		}
	}

	var bashCalls []model.ToolCall
	for _, tc := range doc.ToolCalls {
		if tc.ToolName == "Bash" {
			bashCalls = append(bashCalls, tc)
		}
	}
	if len(bashCalls) != 2 {
		t.Fatalf("expected 2 Bash tool calls (one per subagent), got %d", len(bashCalls))
	}
	for _, tc := range bashCalls {
		if tc.Status != model.StatusOK {
			t.Errorf("bash tool call %s: status = %q, want ok", tc.ToolCallID, tc.Status)
		}
	}
}
