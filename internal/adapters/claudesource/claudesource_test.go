package claudesource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

var update = flag.Bool("update", false, "update golden files in testdata/")

// mustList calls src.List and fails the test on error — every test here
// treats a broken List over the committed fixture tree as a setup bug, not
// a thing under test.
func mustList(t *testing.T, src *claudesource.Source, root string) []string {
	t.Helper()
	paths, err := src.List(context.Background(), root)
	if err != nil {
		t.Fatalf("List(%s): %v", root, err)
	}
	return paths
}

// goldenName maps a session file path to its testdata/*.golden.json stem:
// the project directory name with its leading "-" trimmed, plus
// "-<file-stem>" only when the project holds more than one top-level
// *.jsonl (as the four pathological files, sharing one synthetic project
// directory, do). Real fixture projects hold exactly one (logfixture.
// SessionFile's contract), so they get stable names untouched by this
// suffix.
func goldenName(t *testing.T, path string) string {
	t.Helper()
	projDir := filepath.Dir(path)
	proj := strings.TrimPrefix(filepath.Base(projDir), "-")

	entries, err := os.ReadDir(projDir)
	if err != nil {
		t.Fatalf("reading %s: %v", projDir, err)
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			count++
		}
	}
	if count <= 1 {
		return proj
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return proj + "-" + stem
}

// golden is the shape written to testdata/*.golden.json: everything Parse
// returns for one fixture, besides the error (which every fixture in this
// suite is asserted to be nil for separately).
type golden struct {
	Stats model.ParseStats `json:"stats"`
	Doc   model.SessionDoc `json:"doc"`
}

// normalizeSourcePath rewrites doc.Session.SourcePath to be relative to
// logfixture.Dir(), so a golden file compares equal on any machine and in
// CI, not just on the one it was generated on (§F5 of the ticket plan).
func normalizeSourcePath(doc model.SessionDoc) model.SessionDoc {
	doc.Session.SourcePath = strings.TrimPrefix(doc.Session.SourcePath, logfixture.Dir()+string(filepath.Separator))
	return doc
}

// TestGoldenParse parses every session file List finds under the real and
// pathological Claude fixture roots and compares the result — stats plus
// the full SessionDoc — against a committed golden file. Run with -update
// to write/refresh the goldens after reviewing the diff.
func TestGoldenParse(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	var paths []string
	paths = append(paths, mustList(t, src, logfixture.ClaudeRoot())...)
	paths = append(paths, mustList(t, src, logfixture.PathologicalClaudeRoot())...)

	for _, p := range paths {
		name := goldenName(t, p)
		t.Run(name, func(t *testing.T) {
			doc, stats, err := src.Parse(ctx, p)
			if err != nil {
				t.Fatalf("Parse(%s): %v", p, err)
			}
			doc = normalizeSourcePath(doc)

			gotBytes, err := json.MarshalIndent(golden{Stats: stats, Doc: doc}, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			gotBytes = append(gotBytes, '\n')

			goldenPath := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(goldenPath, gotBytes, 0o644); err != nil {
					t.Fatalf("writing %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("reading %s: %v (run `go test ./internal/adapters/claudesource -run TestGoldenParse -update` to create it)", goldenPath, err)
			}
			if !bytes.Equal(gotBytes, want) {
				t.Errorf("golden mismatch for %s (-update to refresh)\n got:\n%s\nwant:\n%s", p, gotBytes, want)
			}
		})
	}
}

// TestGoldenSetMatchesFixtures is the anti-rot guard for TestGoldenParse: a
// fixture with no golden file fails here rather than being silently
// unverified, and a golden file with no matching fixture (left behind by a
// fixture removal or rename) fails here rather than rotting forever.
func TestGoldenSetMatchesFixtures(t *testing.T) {
	src := claudesource.New(nil)

	want := map[string]bool{}
	for _, root := range []string{logfixture.ClaudeRoot(), logfixture.PathologicalClaudeRoot()} {
		for _, p := range mustList(t, src, root) {
			want[goldenName(t, p)+".golden.json"] = true
		}
	}

	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("reading testdata: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".golden.json") {
			got[e.Name()] = true
		}
	}

	for name := range want {
		if !got[name] {
			t.Errorf("missing golden file for a fixture List(...) reported: testdata/%s", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("stale golden file with no matching fixture: testdata/%s", name)
		}
	}
}

// rawLinesByUUID reads path's non-empty lines and indexes them by the
// record's uuid field, so a test can look up the exact source bytes a
// message's Raw ought to byte-equal. A line with no (or unparseable) uuid
// is skipped: malformed lines never become messages, so they never need to
// satisfy this check.
func rawLinesByUUID(t *testing.T, path string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	out := make(map[string][]byte)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		var probe struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil || probe.UUID == "" {
			continue
		}
		out[probe.UUID] = []byte(trimmed)
	}
	return out
}

// subagentFilesOf returns the *.jsonl files under path's own subagents/
// directory (mirroring what Source.Parse itself looks for), or nil if
// there is none.
func subagentFilesOf(t *testing.T, path string) []string {
	t.Helper()
	subDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			out = append(out, filepath.Join(subDir, e.Name()))
		}
	}
	return out
}

// TestRawIsVerbatim proves losslessness independently of the golden files:
// every emitted message's Raw must byte-equal the exact source line (from
// the parent transcript or a flattened subagent transcript) it came from.
func TestRawIsVerbatim(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	var paths []string
	paths = append(paths, mustList(t, src, logfixture.ClaudeRoot())...)
	paths = append(paths, mustList(t, src, logfixture.PathologicalClaudeRoot())...)

	for _, p := range paths {
		t.Run(goldenName(t, p), func(t *testing.T) {
			doc, _, err := src.Parse(ctx, p)
			if err != nil {
				t.Fatalf("Parse(%s): %v", p, err)
			}

			raws := rawLinesByUUID(t, p)
			for _, sub := range subagentFilesOf(t, p) {
				maps.Copy(raws, rawLinesByUUID(t, sub))
			}

			for _, m := range doc.Messages {
				uuid := strings.TrimPrefix(m.MessageID, "claude:")
				want, ok := raws[uuid]
				if !ok {
					t.Errorf("message %s: no source line found for uuid %q", m.MessageID, uuid)
					continue
				}
				if !bytes.Equal(m.Raw, want) {
					t.Errorf("message %s: Raw does not byte-equal its source line\n got:  %s\nwant: %s", m.MessageID, m.Raw, want)
				}
			}
		})
	}
}

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
// removing the sidecar, breaking the agentId join — without ever touching
// the committed fixture. It returns the copied session file's path and its
// subagents/ directory.
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

// TestPathologicalNeverErrors is the ticket's central done-when clause for
// the malformed fixtures: Parse must never return an error for any of
// them, however badly formed their content.
func TestPathologicalNeverErrors(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	paths := mustList(t, src, logfixture.PathologicalClaudeRoot())
	if len(paths) != 4 {
		t.Fatalf("expected 4 pathological Claude fixtures, got %d", len(paths))
	}
	for _, p := range paths {
		if _, _, err := src.Parse(ctx, p); err != nil {
			t.Errorf("Parse(%s) returned an error: %v", p, err)
		}
	}
}

// TestNoPanicOnAnyFixture parses every *.jsonl under the whole fixture
// tree directly, including subagent transcripts read as if they were
// top-level sessions and the Codex fixtures — inputs this adapter was
// never designed for — asserting only that it never panics.
func TestNoPanicOnAnyFixture(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	found := 0
	err := filepath.WalkDir(logfixture.Dir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		found++
		rel := strings.TrimPrefix(p, logfixture.Dir()+string(filepath.Separator))
		t.Run(rel, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse(%s) panicked: %v", p, r)
				}
			}()
			_, _, _ = src.Parse(ctx, p)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", logfixture.Dir(), err)
	}
	if found == 0 {
		t.Fatal("expected at least one .jsonl file under the fixture tree")
	}
}

// TestListSkipsSubagentTranscripts pins the ticket's enumeration contract:
// List returns only top-level session files, never a subagent transcript.
func TestListSkipsSubagentTranscripts(t *testing.T) {
	src := claudesource.New(nil)
	paths := mustList(t, src, logfixture.ClaudeRoot())
	if len(paths) != 3 {
		t.Fatalf("List(ClaudeRoot()) returned %d paths, want 3: %v", len(paths), paths)
	}
	marker := string(filepath.Separator) + "subagents" + string(filepath.Separator)
	for _, p := range paths {
		if strings.Contains(p, marker) {
			t.Errorf("List returned a subagent transcript: %s", p)
		}
	}
}

// TestListMissingRootIsNotAnError pins US-7: a vendor root (or its
// projects/ directory) that does not exist yields no paths and no error,
// same as one that exists but is empty.
func TestListMissingRootIsNotAnError(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	paths, err := src.List(ctx, filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if paths != nil {
		t.Errorf("List = %v, want nil", paths)
	}

	emptyRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(emptyRoot, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err = src.List(ctx, emptyRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if paths != nil {
		t.Errorf("List = %v, want nil", paths)
	}
}

// TestFixtureShape pins the exact message/tool_call/skip counts (and a
// handful of load-bearing field values) each committed fixture must
// produce. This is what stops a blind `-update` after a fixture
// regeneration from silently accepting a behaviour change: id/timestamp
// churn is expected, but a changed count here is not.
func TestFixtureShape(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	sidechainPath, err := logfixture.SessionFile(logfixture.ClaudeSidechainProject)
	if err != nil {
		t.Fatal(err)
	}
	toolErrorPath, err := logfixture.SessionFile(logfixture.ClaudeToolErrorProject)
	if err != nil {
		t.Fatal(err)
	}

	singleTurnDir := filepath.Join(logfixture.ClaudeProjectsDir(), "-tmp-claude-0--home-user-94ba8eae-3476-51cf-a4d4-b0b5339db735-scratchpad-fixture-project")
	singleTurnEntries, err := os.ReadDir(singleTurnDir)
	if err != nil {
		t.Fatal(err)
	}
	var singleTurnPath string
	for _, e := range singleTurnEntries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			singleTurnPath = filepath.Join(singleTurnDir, e.Name())
		}
	}
	if singleTurnPath == "" {
		t.Fatal("single-turn fixture session file not found")
	}

	pathDemoDir := filepath.Join(logfixture.PathologicalClaudeRoot(), "projects", "-home-user-demo")
	truncatedPath := filepath.Join(pathDemoDir, "11111111-1111-4111-8111-111111111111.jsonl")
	unknownTypePath := filepath.Join(pathDemoDir, "22222222-2222-4222-8222-222222222222.jsonl")
	missingResultPath := filepath.Join(pathDemoDir, "33333333-3333-4333-8333-333333333333.jsonl")
	emptyPath := filepath.Join(pathDemoDir, "44444444-4444-4444-8444-444444444444.jsonl")

	type want struct {
		messages  int
		toolCalls int
		skips     model.SkipCounts
	}
	tests := []struct {
		name string
		path string
		want want
	}{
		{"sidechain", sidechainPath, want{7, 2, model.SkipCounts{model.SkipBookkeeping: 9}}},
		{"tool-error", toolErrorPath, want{4, 1, model.SkipCounts{model.SkipBookkeeping: 7}}},
		{"single-turn", singleTurnPath, want{4, 1, model.SkipCounts{model.SkipBookkeeping: 7}}},
		{"pathological/truncated", truncatedPath, want{1, 0, model.SkipCounts{model.SkipBookkeeping: 3, model.SkipMalformedLine: 1}}},
		{"pathological/unknown-type", unknownTypePath, want{1, 0, model.SkipCounts{model.SkipBookkeeping: 2, model.SkipUnknownRecordType: 1}}},
		{"pathological/missing-tool-result", missingResultPath, want{4, 1, model.SkipCounts{model.SkipBookkeeping: 7}}},
		{"pathological/empty", emptyPath, want{0, 0, nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, stats, err := src.Parse(ctx, tt.path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(doc.Messages) != tt.want.messages {
				t.Errorf("messages = %d, want %d", len(doc.Messages), tt.want.messages)
			}
			if len(doc.ToolCalls) != tt.want.toolCalls {
				t.Errorf("tool_calls = %d, want %d", len(doc.ToolCalls), tt.want.toolCalls)
			}
			if !maps.Equal(stats.Skipped, tt.want.skips) {
				t.Errorf("skips = %v, want %v", stats.Skipped, tt.want.skips)
			}
		})
	}

	t.Run("sidechain fields", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, sidechainPath)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Session.SessionID != "claude:40def079-df87-46bd-ac03-5ffbf1a74ca2" {
			t.Errorf("session_id = %q", doc.Session.SessionID)
		}
		if doc.Session.ProjectPath != "/home/user/fixture-sidechain" {
			t.Errorf("project_path = %q", doc.Session.ProjectPath)
		}
		if doc.Session.ProjectName != "fixture-sidechain" {
			t.Errorf("project_name = %q", doc.Session.ProjectName)
		}
		if doc.Session.GitBranch != "main" {
			t.Errorf("git_branch = %q", doc.Session.GitBranch)
		}
		if doc.Session.VendorVersion != "2.1.231" {
			t.Errorf("vendor_version = %q", doc.Session.VendorVersion)
		}
		wantStart := time.Date(2026, 8, 13, 11, 16, 42, 550_000_000, time.UTC)
		wantEnd := time.Date(2026, 8, 13, 11, 16, 49, 806_000_000, time.UTC)
		if !doc.Session.StartedAt.Equal(wantStart) {
			t.Errorf("started_at = %s, want %s", doc.Session.StartedAt, wantStart)
		}
		if !doc.Session.EndedAt.Equal(wantEnd) {
			t.Errorf("ended_at = %s, want %s", doc.Session.EndedAt, wantEnd)
		}

		var agentTC, bashTC *model.ToolCall
		for i := range doc.ToolCalls {
			switch doc.ToolCalls[i].ToolName {
			case "Agent":
				agentTC = &doc.ToolCalls[i]
			case "Bash":
				bashTC = &doc.ToolCalls[i]
			}
		}
		if agentTC == nil || bashTC == nil {
			t.Fatal("expected both an Agent and a Bash tool call")
		}
		if agentTC.ToolCallID != "claude:toolu_01BDUbYNjgU13kKTWciYEUEm" {
			t.Errorf("agent tool_call_id = %q", agentTC.ToolCallID)
		}
		if agentTC.Seq != 0 {
			t.Errorf("agent seq = %d, want 0", agentTC.Seq)
		}
		if agentTC.Status != model.StatusOK {
			t.Errorf("agent status = %q, want ok", agentTC.Status)
		}
		wantAgentOutput := "The command output: `hello from subagent`\n" +
			"agentId: a0fb4161db4e2c307 (use SendMessage with to: 'a0fb4161db4e2c307', summary: '<5-10 word recap>' to continue this agent)\n" +
			"<usage>subagent_tokens: 14066\ntool_uses: 1\nduration_ms: 3280</usage>"
		if agentTC.Output != wantAgentOutput {
			t.Errorf("agent output = %q, want %q", agentTC.Output, wantAgentOutput)
		}

		if bashTC.ToolCallID != "claude:toolu_01LT6HE1Vb4do18ShHMyCNJ8" {
			t.Errorf("bash tool_call_id = %q", bashTC.ToolCallID)
		}
		if bashTC.Seq != 1 {
			t.Errorf("bash seq = %d, want 1", bashTC.Seq)
		}
		if bashTC.Status != model.StatusOK {
			t.Errorf("bash status = %q, want ok", bashTC.Status)
		}
		if bashTC.Output != "hello from subagent" {
			t.Errorf("bash output = %q, want %q", bashTC.Output, "hello from subagent")
		}
	})

	t.Run("tool-error fields", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, toolErrorPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatal("expected exactly one tool call")
		}
		tc := doc.ToolCalls[0]
		if tc.ToolCallID != "claude:toolu_01FUATKH4b5XsMviFXq4VNcj" {
			t.Errorf("tool_call_id = %q", tc.ToolCallID)
		}
		if tc.ToolName != "Bash" {
			t.Errorf("tool_name = %q", tc.ToolName)
		}
		if tc.Status != model.StatusError {
			t.Errorf("status = %q, want error", tc.Status)
		}
		wantOutput := "Exit code 1\ncat: /nonexistent-fixture-file: No such file or directory"
		if tc.Output != wantOutput {
			t.Errorf("output = %q, want %q", tc.Output, wantOutput)
		}
		if doc.Session.SessionID != "claude:8f1900e4-5b83-46c2-a244-2811996f87c3" {
			t.Errorf("session_id = %q", doc.Session.SessionID)
		}
		if doc.Session.ProjectPath != "/home/user/fixture-tool-error" {
			t.Errorf("project_path = %q", doc.Session.ProjectPath)
		}
		if doc.Session.GitBranch != "main" {
			t.Errorf("git_branch = %q", doc.Session.GitBranch)
		}
		if doc.Session.VendorVersion != "2.1.231" {
			t.Errorf("vendor_version = %q", doc.Session.VendorVersion)
		}
	})

	t.Run("single-turn fields", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, singleTurnPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatal("expected exactly one tool call")
		}
		tc := doc.ToolCalls[0]
		if tc.ToolCallID != "claude:toolu_01FrTAL9gbc7Fp3VgKpvw6VH" {
			t.Errorf("tool_call_id = %q", tc.ToolCallID)
		}
		if tc.Status != model.StatusOK {
			t.Errorf("status = %q, want ok", tc.Status)
		}
		if tc.Output != "hello fixture" {
			t.Errorf("output = %q, want %q", tc.Output, "hello fixture")
		}
		if doc.Session.ProjectName != "fixture-project" {
			t.Errorf("project_name = %q", doc.Session.ProjectName)
		}
		if doc.Session.GitBranch != "HEAD" {
			t.Errorf("git_branch = %q, want HEAD (passed through unchanged)", doc.Session.GitBranch)
		}
		if doc.Session.VendorVersion != "2.1.228" {
			t.Errorf("vendor_version = %q", doc.Session.VendorVersion)
		}
	})

	t.Run("pathological/missing-tool-result status pending", func(t *testing.T) {
		doc, _, err := src.Parse(ctx, missingResultPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.ToolCalls) != 1 {
			t.Fatal("expected exactly one tool call")
		}
		tc := doc.ToolCalls[0]
		if tc.Status != model.StatusPending {
			t.Errorf("status = %q, want pending", tc.Status)
		}
		if tc.Output != "" {
			t.Errorf("output = %q, want empty", tc.Output)
		}

		var lastMsg *model.Message
		for i := range doc.Messages {
			if doc.Messages[i].Text == "done" {
				lastMsg = &doc.Messages[i]
			}
		}
		if lastMsg == nil {
			t.Fatal(`expected a "done" message`)
		}
		// The dangling parentUuid (pointing at a uuid that appears nowhere
		// else in the file, because the tool_result record that carried
		// it was deleted) is preserved verbatim, not repaired or dropped
		// — it is what the vendor recorded.
		wantParent := "claude:75ca9980-2b60-471f-98a9-c508c157a032"
		if lastMsg.ParentMessageID != wantParent {
			t.Errorf("ParentMessageID = %q, want %q (dangling reference preserved verbatim)", lastMsg.ParentMessageID, wantParent)
		}
	})

	t.Run("pathological fixtures share the single-turn fixture's inner session_id/cwd", func(t *testing.T) {
		// Known artifact of how these fixtures were derived
		// (internal/testing/logfixture/README.md): despite their
		// synthetic filenames, all four carry the single-turn fixture's
		// real sessionId/cwd. Orchestrator adjudication 6: not "fixed" by
		// keying off the filename — pinned here so a future change to
		// that call is a deliberate, visible diff.
		want := "claude:94ba8eae-3476-51cf-a4d4-b0b5339db735"
		wantPath := "/tmp/claude-0/-home-user/94ba8eae-3476-51cf-a4d4-b0b5339db735/scratchpad/fixture-project"
		for _, p := range []string{truncatedPath, unknownTypePath, missingResultPath} {
			doc, _, err := src.Parse(ctx, p)
			if err != nil {
				t.Fatalf("Parse(%s): %v", p, err)
			}
			if doc.Session.SessionID != want {
				t.Errorf("%s: session_id = %q, want %q", p, doc.Session.SessionID, want)
			}
			if doc.Session.ProjectPath != wantPath {
				t.Errorf("%s: project_path = %q, want %q", p, doc.Session.ProjectPath, wantPath)
			}
		}
	})
}

// writeSyntheticSession writes lines (each already a complete JSON record)
// as a session file in a fresh temp directory and returns its path.
func writeSyntheticSession(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

const syntheticUserLine = `{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`

func syntheticToolUseLine(uuid, parentUUID, toolUseID string) string {
	return fmt.Sprintf(`{"parentUuid":%q,"isSidechain":false,"type":"assistant","message":{"model":"m","role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"x"}}]},"uuid":%q,"timestamp":"2026-01-01T00:00:01.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
		parentUUID, toolUseID, uuid)
}

func syntheticToolResultLine(uuid, parentUUID, toolUseID string, content json.RawMessage, omitContent, isError bool) string {
	var block string
	if omitContent {
		block = fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"is_error":%v}`, toolUseID, isError)
	} else {
		block = fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"is_error":%v,"content":%s}`, toolUseID, isError, content)
	}
	return fmt.Sprintf(`{"parentUuid":%q,"isSidechain":false,"type":"user","message":{"role":"user","content":[%s]},"uuid":%q,"timestamp":"2026-01-01T00:00:02.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`,
		parentUUID, block, uuid)
}

// TestToolResultShapes is a pure, table-driven, in-code exercise of every
// tool_result content shape and the orphan case, built from synthetic
// records rather than fixtures — the same coverage §D5/§C.6 of the ticket
// plan describes for resultOutput and applyToolResults, driven entirely
// through the public Source.Parse API.
func TestToolResultShapes(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	tests := []struct {
		name        string
		content     json.RawMessage
		omitContent bool
		isError     bool
		toolUseID   string
		wantOrphan  bool
		wantStatus  model.ToolCallStatus
		wantOutput  string
	}{
		{
			name:       "string content, success",
			content:    json.RawMessage(`"hello"`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "hello",
		},
		{
			name:       "string content, error",
			content:    json.RawMessage(`"boom"`),
			isError:    true,
			toolUseID:  "toolu_1",
			wantStatus: model.StatusError,
			wantOutput: "boom",
		},
		{
			name:       "block array content, text blocks join with newline",
			content:    json.RawMessage(`[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "line one\nline two",
		},
		{
			name:       "block array content, non-text blocks contribute nothing",
			content:    json.RawMessage(`[{"type":"text","text":"kept"},{"type":"tool_use","id":"x","name":"y"}]`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "kept",
		},
		{
			name:        "absent content",
			omitContent: true,
			toolUseID:   "toolu_1",
			wantStatus:  model.StatusOK,
			wantOutput:  "",
		},
		{
			name:       "null content",
			content:    json.RawMessage(`null`),
			toolUseID:  "toolu_1",
			wantStatus: model.StatusOK,
			wantOutput: "",
		},
		{
			name:       "orphan tool_use_id",
			content:    json.RawMessage(`"whatever"`),
			toolUseID:  "toolu_does_not_exist",
			wantOrphan: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := []string{
				syntheticUserLine,
				syntheticToolUseLine("u2", "u1", "toolu_1"),
				syntheticToolResultLine("u3", "u2", tt.toolUseID, tt.content, tt.omitContent, tt.isError),
			}
			path := writeSyntheticSession(t, lines)
			doc, stats, err := src.Parse(ctx, path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			if len(doc.ToolCalls) != 1 {
				t.Fatalf("got %d tool calls, want 1", len(doc.ToolCalls))
			}
			tc := doc.ToolCalls[0]

			if tt.wantOrphan {
				if got := stats.Skipped[claudesource.SkipOrphanToolResult]; got != 1 {
					t.Errorf("SkipOrphanToolResult count = %d, want 1", got)
				}
				if tc.Status != model.StatusPending {
					t.Errorf("status = %q, want pending (the result never matched)", tc.Status)
				}
				return
			}

			if tc.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", tc.Status, tt.wantStatus)
			}
			if tc.Output != tt.wantOutput {
				t.Errorf("output = %q, want %q", tc.Output, tt.wantOutput)
			}
		})
	}
}

// TestTextOf is a pure, table-driven, in-code exercise of message text
// extraction (record.go's textOf), driven entirely through the public
// Source.Parse API: string content, a block array mixing text/thinking/
// tool_use blocks, an empty array, and null.
func TestTextOf(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		content string // raw JSON for message.content
		want    string
	}{
		{"string content", `"hello world"`, "hello world"},
		{
			"block array: thinking contributes nothing, text blocks join with newline, tool_use ignored",
			`[{"type":"thinking","thinking":"secret"},{"type":"text","text":"first"},{"type":"tool_use","id":"t1","name":"Bash","input":{}},{"type":"text","text":"second"}]`,
			"first\nsecond",
		},
		{"empty array", `[]`, ""},
		{"null", `null`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"type":"assistant","message":{"model":"m","role":"assistant","content":%s},"uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp/proj","sessionId":"sess-1","version":"1.0.0","gitBranch":"main"}`, tt.content)
			path := writeSyntheticSession(t, []string{line})

			doc, _, err := src.Parse(ctx, path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(doc.Messages) != 1 {
				t.Fatalf("got %d messages, want 1", len(doc.Messages))
			}
			if got := doc.Messages[0].Text; got != tt.want {
				t.Errorf("Text = %q, want %q", got, tt.want)
			}
		})
	}
}
