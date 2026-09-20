//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/testlogs"
)

// fixtureSummary is the exact stdout AC-SYNC-01/02/05 pin (docs/acceptance.md §1.3).
const claudeSummary = "claude: 3 sessions, 15 messages, 4 tool calls, 4 records skipped\n"
const codexZeroSummary = "codex: 0 sessions, 0 messages, 0 tool calls, 0 records skipped\n"
const fixtureSummary = claudeSummary + codexZeroSummary
const codexSummary = "codex: 3 sessions, 10 messages, 4 tool calls, 7 records skipped\n"

// AC-SYNC-01
func TestAC_SYNC_01_FixtureTreeSummary(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--claude-path", testlogs.ClaudeRoot(t))
	wantCode(t, res, 0)
	wantStdout(t, res, fixtureSummary)

	files := sessionFiles(t, s.storeRoot())
	if len(files) != 3 {
		t.Errorf("store holds %d session files, want 3: %v", len(files), files)
	}
}

// AC-SYNC-02
func TestAC_SYNC_02_BareSyncUsesSandboxHome(t *testing.T) {
	s := newSandbox(t)
	s.seedFullClaudeTree()

	res := s.run("sync")
	wantCode(t, res, 0)
	wantStdout(t, res, fixtureSummary)
}

// AC-SYNC-03
func TestAC_SYNC_03_RerunIsIdempotent(t *testing.T) {
	s := newSandbox(t)
	args := []string{"sync", "--claude-path", testlogs.ClaudeRoot(t), "--codex-path", testlogs.CodexRoot(t)}

	first := s.run(args...)
	wantCode(t, first, 0)
	hash1 := hashTree(t, s.storeRoot())

	second := s.run(args...)
	wantCode(t, second, 0)
	if second.stdout != first.stdout {
		t.Errorf("second run's stdout = %q, want it identical to the first's %q", second.stdout, first.stdout)
	}
	hash2 := hashTree(t, s.storeRoot())
	if hash1 != hash2 {
		t.Errorf("store tree hash changed across two identical syncs: %s -> %s", hash1, hash2)
	}
	noLeftovers(t, s.storeRoot())
}

// AC-SYNC-04
func TestAC_SYNC_04_RerunPicksUpNewSessions(t *testing.T) {
	s := newSandbox(t)
	s.seedClaude(testlogs.ClaudeToolErrorProject, claudeScratchpadProject)

	first := s.run("sync")
	wantCode(t, first, 0)
	wantStdout(t, first, "claude: 2 sessions, 8 messages, 2 tool calls, 2 records skipped\n"+codexZeroSummary)

	s.seedClaude(testlogs.ClaudeSidechainProject)
	second := s.run("sync")
	wantCode(t, second, 0)
	wantStdout(t, second, fixtureSummary)
}

// AC-SYNC-05
func TestAC_SYNC_05_SelectedVendorPreservesOthers(t *testing.T) {
	s := newSandbox(t)
	s.seedFullClaudeTree()
	s.seedCodex()
	wantCode(t, s.run("sync"), 0)
	claudeDir := filepath.Join(s.storeRoot(), "sessions", "claude")
	codexDir := filepath.Join(s.storeRoot(), "sessions", "codex")
	claudeBefore, codexBefore := hashTree(t, claudeDir), hashTree(t, codexDir)
	res := s.run("sync", "--vendor", "claude")
	wantCode(t, res, 0)
	wantStdout(t, res, claudeSummary)
	if hashTree(t, codexDir) != codexBefore {
		t.Fatal("Claude refresh changed Codex")
	}
	res = s.run("sync", "--vendor", "codex")
	wantCode(t, res, 0)
	wantStdout(t, res, codexSummary)
	if hashTree(t, claudeDir) != claudeBefore {
		t.Fatal("Codex refresh changed Claude")
	}
	if err := os.RemoveAll(filepath.Join(s.home, ".codex")); err != nil {
		t.Fatal(err)
	}
	res = s.run("sync", "--vendor", "codex")
	wantCode(t, res, 0)
	wantStdout(t, res, codexZeroSummary)
	if len(sessionFiles(t, s.storeRoot())) != 3 || hashTree(t, claudeDir) != claudeBefore {
		t.Fatal("empty Codex refresh lost Claude or kept stale Codex")
	}
}

// AC-SYNC-06
func TestAC_SYNC_06_CodexActiveAndArchived(t *testing.T) {
	for _, override := range []bool{false, true} {
		s := newSandbox(t)
		s.seedCodex()
		args := []string{"sync", "--vendor", "codex"}
		if override {
			args = append(args, "--codex-path", testlogs.CodexRoot(t))
		}
		res := s.run(args...)
		wantCode(t, res, 0)
		wantStdout(t, res, codexSummary)
		if len(sessionFiles(t, s.storeRoot())) != 3 {
			t.Fatal("active/archive sessions missing")
		}
	}
}

// Active copies take precedence during the short window where both locations
// contain the same thread. Exercise the source-to-store boundary through CLI.
func TestCodexActiveCopyWinsArchivedDuplicate(t *testing.T) {
	s := newSandbox(t)
	s.seedCodex()
	archived, err := filepath.Glob(filepath.Join(s.home, ".codex", "archived_sessions", "*.jsonl"))
	if err != nil || len(archived) != 1 {
		t.Fatal(archived, err)
	}
	data, err := os.ReadFile(archived[0])
	if err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(s.home, ".codex", "sessions", filepath.Base(archived[0]))
	if err := os.WriteFile(active, data, 0600); err != nil {
		t.Fatal(err)
	}
	res := s.run("sync", "--vendor", "codex")
	wantCode(t, res, 0)
	files := sessionFiles(t, s.storeRoot())
	if len(files) != 3 {
		t.Fatal("duplicate counted twice")
	}
	found := false
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Session struct {
				SourcePath string `json:"source_path"`
			} `json:"session"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Session.SourcePath == archived[0] {
			t.Fatal("archive overwrote active copy")
		}
		found = found || doc.Session.SourcePath == active
	}
	if !found {
		t.Fatal("active copy missing")
	}
}

// AC-SYNC-07
func TestAC_SYNC_07_UnknownVendor(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--vendor", "bogus")
	wantCode(t, res, 1)
	wantStderrContains(t, res, `unknown vendor "bogus"`)
	wantStderrContains(t, res, `"claude"`)
	wantStderrContains(t, res, `"codex"`)
}

// sessionFiles lists the regular files under storeRoot/sessions, recursively.
func sessionFiles(t *testing.T, storeRoot string) []string {
	t.Helper()
	dir := filepath.Join(storeRoot, "sessions")
	var out []string
	err := walkFiles(dir, func(path string) { out = append(out, path) })
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}
