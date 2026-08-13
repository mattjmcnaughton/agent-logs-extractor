package logfixture_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture/scrub"
)

// eachJSONLLine walks root and calls fn with the path and 1-based line
// number of every non-empty line of every *.jsonl file found.
func eachJSONLLine(t *testing.T, root string, fn func(path string, lineNum int, line []byte)) {
	t.Helper()

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}

		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			fn(p, lineNum, append([]byte(nil), line...))
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

func TestFixturesAreValidJSONL(t *testing.T) {
	// claude/ and codex/ alike -- every non-pathological fixture line must
	// be well-formed. pathological/ is intentionally excluded: its whole
	// purpose is malformed input (truncated lines, unknown types).
	eachJSONLLine(t, logfixture.ClaudeRoot(), func(path string, lineNum int, line []byte) {
		var v map[string]json.RawMessage
		if err := json.Unmarshal(line, &v); err != nil {
			t.Errorf("%s:%d: not a JSON object: %v", path, lineNum, err)
		}
	})
	eachJSONLLine(t, logfixture.CodexRoot(), func(path string, lineNum int, line []byte) {
		var v map[string]json.RawMessage
		if err := json.Unmarshal(line, &v); err != nil {
			t.Errorf("%s:%d: not a JSON object: %v", path, lineNum, err)
		}
	})
}

type toolUseBlock struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Caller json.RawMessage `json:"caller"`
}

type toolResultBlock struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeRecord struct {
	Type          string          `json:"type"`
	SessionID     string          `json:"sessionId"`
	IsSidechain   bool            `json:"isSidechain"`
	AgentID       string          `json:"agentId"`
	Message       *claudeMessage  `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

func decodeRecord(t *testing.T, path string, lineNum int, line []byte) claudeRecord {
	t.Helper()
	var r claudeRecord
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("%s:%d: not valid JSON: %v", path, lineNum, err)
	}
	return r
}

// toolUseBlocksOf returns the tool_use blocks in a record's message content,
// tolerating both string and content-block-array shapes.
func toolUseBlocksOf(content json.RawMessage) []toolUseBlock {
	var blocks []toolUseBlock
	_ = json.Unmarshal(content, &blocks)
	var out []toolUseBlock
	for _, b := range blocks {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

func toolResultBlocksOf(content json.RawMessage) []toolResultBlock {
	var blocks []toolResultBlock
	_ = json.Unmarshal(content, &blocks)
	var out []toolResultBlock
	for _, b := range blocks {
		if b.Type == "tool_result" {
			out = append(out, b)
		}
	}
	return out
}

// TestToolUseCallerIsAlwaysDirect pins the fixture-backed evidence behind
// the TDD's open-question-1 settlement (docs/technical/tdd-mvp.md): every
// observed tool_use.caller is {"type":"direct"}, in the parent files and
// inside the sidechain alike, so it carries no signal for the
// flatten-vs-split decision. If a future vendor version makes caller
// meaningful, this test -- not just prose -- should notice.
func TestToolUseCallerIsAlwaysDirect(t *testing.T) {
	found := 0
	eachJSONLLine(t, logfixture.ClaudeRoot(), func(path string, lineNum int, line []byte) {
		r := decodeRecord(t, path, lineNum, line)
		if r.Message == nil {
			return
		}
		for _, tu := range toolUseBlocksOf(r.Message.Content) {
			found++
			if string(tu.Caller) != `{"type":"direct"}` {
				t.Errorf("%s:%d: tool_use %q has caller %s, want {\"type\":\"direct\"}", path, lineNum, tu.ID, tu.Caller)
			}
		}
	})
	if found == 0 {
		t.Fatal("expected at least one tool_use block across the Claude fixture tree")
	}
}

func TestSidechainFixtureExercisesSubagents(t *testing.T) {
	project := logfixture.ClaudeSidechainProject

	sessionFile, err := logfixture.SessionFile(project)
	if err != nil {
		t.Fatalf("SessionFile(%q): %v", project, err)
	}

	subFiles, err := logfixture.SubagentFiles(project)
	if err != nil {
		t.Fatalf("SubagentFiles(%q): %v", project, err)
	}
	if len(subFiles) == 0 {
		t.Fatalf("expected at least one subagent file for project %q, found none", project)
	}

	// Parent file: find the Agent tool_use and its matching tool_result,
	// and pull sessionId + the join key (toolUseResult.agentId).
	var (
		parentSessionID string
		agentToolUseID  string
		joinAgentID     string
		foundAgentUse   bool
		foundResult     bool
	)
	eachJSONLLine(t, filepath.Dir(sessionFile), func(path string, lineNum int, line []byte) {
		if path != sessionFile {
			return
		}
		r := decodeRecord(t, path, lineNum, line)
		if r.SessionID != "" {
			parentSessionID = r.SessionID
		}
		if r.Message == nil {
			return
		}
		for _, tu := range toolUseBlocksOf(r.Message.Content) {
			if tu.Name == "Agent" {
				foundAgentUse = true
				agentToolUseID = tu.ID
			}
		}
		for _, tr := range toolResultBlocksOf(r.Message.Content) {
			if tr.ToolUseID == agentToolUseID && agentToolUseID != "" {
				foundResult = true
				var tur struct {
					AgentID string `json:"agentId"`
				}
				if err := json.Unmarshal(r.ToolUseResult, &tur); err != nil {
					t.Fatalf("%s:%d: toolUseResult for Agent tool_result is not an object: %v", path, lineNum, err)
				}
				joinAgentID = tur.AgentID
			}
		}
	})

	if !foundAgentUse {
		t.Fatalf("parent session %s: expected an assistant tool_use with name \"Agent\"", sessionFile)
	}
	if !foundResult {
		t.Fatalf("parent session %s: expected a tool_result matching the Agent tool_use id %q", sessionFile, agentToolUseID)
	}
	if joinAgentID == "" {
		t.Fatalf("parent session %s: Agent tool_result's toolUseResult.agentId is empty", sessionFile)
	}

	// Subagent file(s): every record isSidechain==true, non-empty agentId,
	// same sessionId as the parent, and the filename suffix matches the
	// join key.
	for _, sub := range subFiles {
		base := filepath.Base(sub)
		if !strings.HasPrefix(base, "agent-") || !strings.HasSuffix(base, ".jsonl") {
			t.Fatalf("unexpected subagent filename shape: %s", base)
		}
		fileAgentID := strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")

		if fileAgentID != joinAgentID {
			t.Errorf("subagent filename agentId %q does not match parent toolUseResult.agentId %q", fileAgentID, joinAgentID)
		}

		sawRecord := false
		sawSuccessfulToolResult := false
		eachJSONLLine(t, filepath.Dir(sub), func(path string, lineNum int, line []byte) {
			if path != sub {
				return
			}
			r := decodeRecord(t, path, lineNum, line)
			sawRecord = true
			if !r.IsSidechain {
				t.Errorf("%s:%d: expected isSidechain=true", path, lineNum)
			}
			if r.AgentID == "" {
				t.Errorf("%s:%d: expected non-empty agentId", path, lineNum)
			}
			if r.SessionID != parentSessionID {
				t.Errorf("%s:%d: sessionId %q does not match parent sessionId %q", path, lineNum, r.SessionID, parentSessionID)
			}
			if r.Message == nil {
				return
			}
			for _, tr := range toolResultBlocksOf(r.Message.Content) {
				if tr.IsError {
					continue
				}
				sawSuccessfulToolResult = true
				// The third toolUseResult shape (see docs/technical/tdd-mvp.md):
				// absent entirely on a successful call recorded inside a
				// sidechain transcript, not just object-vs-string.
				if r.ToolUseResult != nil {
					t.Errorf("%s:%d: expected toolUseResult to be absent for a successful sidechain tool_result, got %s", path, lineNum, r.ToolUseResult)
				}
			}
		})
		if !sawRecord {
			t.Errorf("subagent file %s has no records", sub)
		}
		if !sawSuccessfulToolResult {
			t.Errorf("subagent file %s: expected at least one successful (non-error) tool_result", sub)
		}

		// The .meta.json sidecar, if present, carries toolUseId as a direct
		// pointer back to the parent's Agent tool_use id -- the primary
		// join, with toolUseResult.agentId as the fallback exercised above.
		metaPath := strings.TrimSuffix(sub, ".jsonl") + ".meta.json"
		if b, err := os.ReadFile(metaPath); err == nil {
			var meta struct {
				ToolUseID string `json:"toolUseId"`
			}
			if err := json.Unmarshal(b, &meta); err != nil {
				t.Errorf("%s: not valid JSON: %v", metaPath, err)
			} else if meta.ToolUseID != agentToolUseID {
				t.Errorf("%s: toolUseId %q does not match parent Agent tool_use id %q", metaPath, meta.ToolUseID, agentToolUseID)
			}
		}
	}
}

func TestToolErrorFixtureExercisesFailedCall(t *testing.T) {
	project := logfixture.ClaudeToolErrorProject

	sessionFile, err := logfixture.SessionFile(project)
	if err != nil {
		t.Fatalf("SessionFile(%q): %v", project, err)
	}

	seenUseIDs := map[string]bool{}

	foundFailure := false
	eachJSONLLine(t, filepath.Dir(sessionFile), func(path string, lineNum int, line []byte) {
		if path != sessionFile {
			return
		}
		r := decodeRecord(t, path, lineNum, line)
		if r.Message == nil {
			return
		}
		for _, tu := range toolUseBlocksOf(r.Message.Content) {
			seenUseIDs[tu.ID] = true
		}
		for _, tr := range toolResultBlocksOf(r.Message.Content) {
			if !tr.IsError {
				continue
			}
			if !seenUseIDs[tr.ToolUseID] {
				t.Errorf("%s:%d: is_error tool_result's tool_use_id %q has no earlier matching tool_use", path, lineNum, tr.ToolUseID)
			}
			var s string
			if err := json.Unmarshal(r.ToolUseResult, &s); err != nil {
				t.Fatalf("%s:%d: expected toolUseResult to unmarshal as a JSON string for an errored tool_result, got: %v", path, lineNum, err)
			}
			if s == "" {
				t.Errorf("%s:%d: expected non-empty toolUseResult string", path, lineNum)
			}
			foundFailure = true
		}
	})

	if !foundFailure {
		t.Fatalf("session %s: expected at least one tool_result block with is_error=true", sessionFile)
	}
}

// eachFixtureDataFile walks logfixture.VendorRoots() (never the package's
// own .go/.md files) and calls fn with the path and full bytes of every
// non-.jsonl file found. Today that means subagent .meta.json sidecars,
// which scrub.Tree copies through verbatim rather than rewriting
// line-by-line.
func eachFixtureDataFile(t *testing.T, fn func(path string, data []byte)) {
	t.Helper()

	for _, root := range logfixture.VendorRoots() {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fn(p, b)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
}

func TestFixturesAreScrubbed(t *testing.T) {
	// Single root: covers claude/, codex/, and pathological/ alike. There
	// is no vendor-specific exclusion -- every committed *.jsonl line must
	// be clean, full stop. (Codex's real /root/.codex/skills/... paths are
	// not a finding: /root is the generic root home and scrub.Findings
	// does not flag it -- see TestLineRewritesForeignHomeDirs and
	// TestFindingsCleanAndDirty in scrub_test.go.)
	eachJSONLLine(t, logfixture.Dir(), func(path string, lineNum int, line []byte) {
		if findings := scrub.Findings(line); findings != nil {
			t.Errorf("%s:%d: sensitive content still present: %v", path, lineNum, findings)
		}
	})

	// Non-.jsonl fixture files (e.g. subagent .meta.json sidecars) are not
	// line-oriented, so scan each one whole rather than line-by-line.
	eachFixtureDataFile(t, func(path string, data []byte) {
		if findings := scrub.Findings(data); findings != nil {
			t.Errorf("%s: sensitive content still present: %v", path, findings)
		}
	})
}
