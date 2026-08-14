//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// fixtureSummary is the exact stdout AC-SYNC-01/02/05 pin (docs/acceptance.md §1.3).
const fixtureSummary = "claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped\n"

// AC-SYNC-01
func TestAC_SYNC_01_FixtureTreeSummary(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--claude-path", logfixture.ClaudeRoot())
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
	args := []string{"sync", "--claude-path", logfixture.ClaudeRoot()}

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
	s.seedClaude(logfixture.ClaudeToolErrorProject, claudeScratchpadProject)

	first := s.run("sync")
	wantCode(t, first, 0)
	wantStdout(t, first, "claude: 2 sessions, 8 messages, 2 tool calls, 14 records skipped\n")

	s.seedClaude(logfixture.ClaudeSidechainProject)
	second := s.run("sync")
	wantCode(t, second, 0)
	wantStdout(t, second, fixtureSummary)
}

// AC-SYNC-05
func TestAC_SYNC_05_VendorClaudeMatchesBareSync(t *testing.T) {
	bare := newSandbox(t)
	bare.seedFullClaudeTree()
	bareRes := bare.run("sync")
	wantCode(t, bareRes, 0)

	vendored := newSandbox(t)
	vendored.seedFullClaudeTree()
	vendoredRes := vendored.run("sync", "--vendor", "claude")
	wantCode(t, vendoredRes, 0)
	// A6: pin vendoredRes's own content — without this, two runs that both
	// printed "" would still satisfy the equality check below.
	wantStdout(t, vendoredRes, fixtureSummary)

	if vendoredRes.stdout != bareRes.stdout {
		t.Errorf("sync --vendor claude stdout = %q, want it identical to bare sync's %q", vendoredRes.stdout, bareRes.stdout)
	}
}

// AC-SYNC-06
func TestAC_SYNC_06_VendorCodexNotYetAvailable(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--vendor", "codex")
	wantCode(t, res, 1)
	wantStderrContains(t, res, `vendor "codex" is not supported by this build yet; available: [claude]`)
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
