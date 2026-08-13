package scrub_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture/scrub"
)

func topLevelKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestLineAppliesMapsAndPreservesShape(t *testing.T) {
	in := []byte(`{"type":"user","cwd":"/tmp/work/proj","sessionId":"abc"}`)
	out, err := scrub.Line(in, scrub.Options{
		Maps: []scrub.Rule{{Old: "/tmp/work/proj", New: "/home/user/fixture-project"}},
	})
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	if !strings.Contains(string(out), `"cwd":"/home/user/fixture-project"`) {
		t.Fatalf("map rewrite did not land: %s", out)
	}

	wantKeys := topLevelKeys(t, in)
	gotKeys := topLevelKeys(t, out)
	if strings.Join(wantKeys, ",") != strings.Join(gotKeys, ",") {
		t.Fatalf("key set changed: before=%v after=%v", wantKeys, gotKeys)
	}

	var before, after struct{ Type string }
	if err := json.Unmarshal(in, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	if before.Type != after.Type {
		t.Fatalf(`"type" changed: %q -> %q`, before.Type, after.Type)
	}
}

func TestLineIsByteIdenticalWhenNothingMatches(t *testing.T) {
	in := []byte(`{"type":"user","cwd":"/home/user/proj","sessionId":"abc","gitBranch":"main"}`)
	out, err := scrub.Line(in, scrub.Options{Branch: "main"})
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("expected byte-identical output when nothing matches:\nin:  %s\nout: %s", in, out)
	}
}

func TestLineRedactsEmailsAndTokens(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want string
	}{
		{"email", "someone@example.org", "user@example.com"},
		{"anthropic key", "sk-ant-abcdefgh12345678", "REDACTED"},
		{"generic sk key", "sk-" + strings.Repeat("a", 20), "REDACTED"},
		{"github pat classic", "ghp_" + strings.Repeat("a", 20), "REDACTED"},
		{"github pat fine-grained", "github_pat_" + strings.Repeat("a", 20), "REDACTED"},
		{"gitlab pat", "glpat-" + strings.Repeat("a", 16), "REDACTED"},
		{"aws key", "AKIA" + strings.Repeat("A", 16), "REDACTED"},
		{"slack token", "xoxb-" + strings.Repeat("a", 10), "REDACTED"},
		{"bearer token", "Bearer " + strings.Repeat("a", 20), "REDACTED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(`{"type":"user","text":"contact ` + tc.val + ` please"}`)
			out, err := scrub.Line(in, scrub.Options{})
			if err != nil {
				t.Fatalf("Line: %v", err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("expected %q in output, got %s", tc.want, out)
			}
			if strings.Contains(string(out), tc.val) {
				t.Fatalf("sensitive value %q still present in output %s", tc.val, out)
			}
			if _, err := decodeCheck(out); err != nil {
				t.Fatalf("output not valid JSON: %v", err)
			}
		})
	}
}

func decodeCheck(b []byte) (map[string]any, error) {
	var m map[string]any
	err := json.Unmarshal(b, &m)
	return m, err
}

func TestLineRewritesForeignHomeDirs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"linux foreign home", "/home/alice/project", "/home/user/project"},
		{"macos foreign home", "/Users/alice/project", "/home/user/project"},
		{"already home/user", "/home/user/project", "/home/user/project"},
		// /root is the generic root home -- identical on every machine, so
		// it identifies nobody and is left untouched rather than rewritten
		// (rewriting it would corrupt verbatim vendor paths, e.g. Codex's
		// real /root/.codex/skills/... listing).
		{"root left untouched", "/root/project", "/root/project"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(`{"type":"user","cwd":"` + tc.in + `"}`)
			out, err := scrub.Line(in, scrub.Options{})
			if err != nil {
				t.Fatalf("Line: %v", err)
			}
			var got struct{ Cwd string }
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("output not valid JSON: %v", err)
			}
			if got.Cwd != tc.want {
				t.Fatalf("cwd = %q, want %q", got.Cwd, tc.want)
			}
		})
	}
}

func TestLineRewritesGitBranch(t *testing.T) {
	t.Run("rewrites to Options.Branch", func(t *testing.T) {
		in := []byte(`{"type":"user","gitBranch":"feat/secret-thing"}`)
		out, err := scrub.Line(in, scrub.Options{Branch: "main"})
		if err != nil {
			t.Fatalf("Line: %v", err)
		}
		if !strings.Contains(string(out), `"gitBranch":"main"`) {
			t.Fatalf("expected gitBranch rewritten to main, got %s", out)
		}
	})

	t.Run("empty Branch keeps original", func(t *testing.T) {
		in := []byte(`{"type":"user","gitBranch":"feat/secret-thing"}`)
		out, err := scrub.Line(in, scrub.Options{Branch: ""})
		if err != nil {
			t.Fatalf("Line: %v", err)
		}
		if string(out) != string(in) {
			t.Fatalf("expected gitBranch untouched, got %s", out)
		}
	})

	t.Run("null gitBranch untouched", func(t *testing.T) {
		in := []byte(`{"type":"user","gitBranch":null}`)
		out, err := scrub.Line(in, scrub.Options{Branch: "main"})
		if err != nil {
			t.Fatalf("Line: %v", err)
		}
		if string(out) != string(in) {
			t.Fatalf("expected null gitBranch untouched, got %s", out)
		}
	})
}

func TestLineRejectsUnsafeReplacement(t *testing.T) {
	in := []byte(`{"type":"user","cwd":"/tmp/work"}`)

	t.Run("quote in Rule.New", func(t *testing.T) {
		_, err := scrub.Line(in, scrub.Options{Maps: []scrub.Rule{{Old: "/tmp/work", New: `bad"value`}}})
		if err == nil {
			t.Fatal("expected error for New containing a quote")
		}
	})

	t.Run("backslash in Rule.New", func(t *testing.T) {
		_, err := scrub.Line(in, scrub.Options{Maps: []scrub.Rule{{Old: "/tmp/work", New: `bad\value`}}})
		if err == nil {
			t.Fatal("expected error for New containing a backslash")
		}
	})

	t.Run("quote in Branch", func(t *testing.T) {
		_, err := scrub.Line(in, scrub.Options{Branch: `bad"branch`})
		if err == nil {
			t.Fatal("expected error for Branch containing a quote")
		}
	})
}

func TestLineRejectsNonObjectInput(t *testing.T) {
	cases := []string{
		`not json at all`,
		`["an", "array"]`,
		`"just a string"`,
		`42`,
	}
	for _, in := range cases {
		if _, err := scrub.Line([]byte(in), scrub.Options{}); err == nil {
			t.Fatalf("expected error for non-object input %q", in)
		}
	}
}

func TestTreeRewritesEncodedPaths(t *testing.T) {
	srcRoot := t.TempDir()
	projDir := filepath.Join(srcRoot, "-tmp-work-proj")
	subDir := filepath.Join(projDir, "session-uuid-1", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mainFile := filepath.Join(projDir, "session-uuid-1.jsonl")
	mainContent := `{"type":"user","cwd":"/tmp/work/proj","sessionId":"session-uuid-1"}` + "\n" +
		`{"type":"assistant","cwd":"/tmp/work/proj","sessionId":"session-uuid-1"}` + "\n"
	if err := os.WriteFile(mainFile, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}

	subFile := filepath.Join(subDir, "agent-abc123.jsonl")
	subContent := `{"type":"user","isSidechain":true,"cwd":"/tmp/work/proj","agentId":"abc123"}` + "\n"
	if err := os.WriteFile(subFile, []byte(subContent), 0o644); err != nil {
		t.Fatal(err)
	}

	dstRoot := t.TempDir()
	written, err := scrub.Tree(projDir, dstRoot, scrub.Options{
		Maps: []scrub.Rule{{Old: "/tmp/work/proj", New: "/home/user/fixture-project"}},
	})
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}

	wantProjDir := filepath.Join(dstRoot, "-home-user-fixture-project")
	wantMain := filepath.Join(wantProjDir, "session-uuid-1.jsonl")
	wantSub := filepath.Join(wantProjDir, "session-uuid-1", "subagents", "agent-abc123.jsonl")

	if len(written) != 2 {
		t.Fatalf("Tree returned %d written paths, want 2: %v", len(written), written)
	}
	wantWritten := map[string]bool{wantMain: true, wantSub: true}
	for _, w := range written {
		if !wantWritten[w] {
			t.Errorf("unexpected written path: %s", w)
		}
	}

	mainOut, err := os.ReadFile(wantMain)
	if err != nil {
		t.Fatalf("expected renamed main session file at %s: %v", wantMain, err)
	}
	subOut, err := os.ReadFile(wantSub)
	if err != nil {
		t.Fatalf("expected renamed subagent file at %s: %v", wantSub, err)
	}

	if got, want := len(strings.Split(strings.TrimRight(string(mainOut), "\n"), "\n")), 2; got != want {
		t.Fatalf("main file line count = %d, want %d", got, want)
	}
	if !strings.Contains(string(mainOut), `/home/user/fixture-project`) {
		t.Fatalf("main file cwd not rewritten: %s", mainOut)
	}
	if !strings.Contains(string(subOut), `/home/user/fixture-project`) {
		t.Fatalf("subagent file cwd not rewritten: %s", subOut)
	}
}

func TestTreePassesBlankLinesThroughVerbatim(t *testing.T) {
	srcRoot := t.TempDir()
	projDir := filepath.Join(srcRoot, "-tmp-work-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A blank line anywhere but EOF is not valid JSON on its own; pathological
	// vendor input is exactly where this is expected, and it must not abort
	// the whole file.
	mainFile := filepath.Join(projDir, "session-uuid-1.jsonl")
	mainContent := `{"type":"user","cwd":"/tmp/work/proj"}` + "\n" +
		"\n" +
		`{"type":"assistant","cwd":"/tmp/work/proj"}` + "\n"
	if err := os.WriteFile(mainFile, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}

	dstRoot := t.TempDir()
	written, err := scrub.Tree(projDir, dstRoot, scrub.Options{
		Maps: []scrub.Rule{{Old: "/tmp/work/proj", New: "/home/user/fixture-project"}},
	})
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("Tree returned %d written paths, want 1: %v", len(written), written)
	}

	out, err := os.ReadFile(written[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3 (blank line preserved): %q", len(lines), out)
	}
	if lines[1] != "" {
		t.Fatalf("expected blank line to pass through verbatim, got %q", lines[1])
	}
}

func TestFindingsCleanAndDirty(t *testing.T) {
	clean := []byte(`{"type":"user","cwd":"/home/user/project","text":"hello"}`)
	if f := scrub.Findings(clean); f != nil {
		t.Fatalf("expected no findings for clean line, got %v", f)
	}

	// /root identifies nobody -- it must never be a finding, or the guard
	// test would have to exclude Codex fixtures again (they legitimately
	// cite /root/.codex/skills/... paths).
	rootPath := []byte(`{"type":"user","text":"see /root/.codex/skills/foo.md"}`)
	if f := scrub.Findings(rootPath); f != nil {
		t.Fatalf("expected no findings for a /root path, got %v", f)
	}

	dirty := []byte(`{"type":"user","cwd":"/home/alice/project","text":"email me at alice@example.org"}`)
	f := scrub.Findings(dirty)
	if f == nil {
		t.Fatal("expected findings for dirty line, got none")
	}
	joined := strings.Join(f, " ")
	if !strings.Contains(joined, "/home/alice") {
		t.Fatalf("expected foreign home dir in findings, got %v", f)
	}
	if !strings.Contains(joined, "alice@example.org") {
		t.Fatalf("expected email in findings, got %v", f)
	}
}
