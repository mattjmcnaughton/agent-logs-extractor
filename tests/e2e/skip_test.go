//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// AC-SKIP-01
func TestAC_SKIP_01_PathologicalTreeNeverFails(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--claude-path", logfixture.PathologicalClaudeRoot())
	wantCode(t, res, 0)
	wantStdout(t, res, "claude: 1 session, 4 messages, 1 tool call, 14 records skipped\n")
}

// AC-SKIP-02
func TestAC_SKIP_02_DebugPrintsSkipBreakdown(t *testing.T) {
	debug := newSandbox(t)
	debugRes := debug.run("sync", "--claude-path", logfixture.ClaudeRoot(), "--log-level", "debug")
	wantCode(t, debugRes, 0)
	wantStderrContains(t, debugRes, `msg="sync: skipped records"`)
	wantStderrContains(t, debugRes, "reason=bookkeeping_record")
	wantStderrContains(t, debugRes, "count=23")

	quiet := newSandbox(t)
	quietRes := quiet.run("sync", "--claude-path", logfixture.ClaudeRoot())
	wantCode(t, quietRes, 0)
	if strings.Contains(quietRes.stderr, "reason=") {
		t.Errorf("default-level stderr contains %q, want no by-reason skip breakdown at all: %q", "reason=", quietRes.stderr)
	}
}

// AC-SKIP-03
func TestAC_SKIP_03_UnreadableFileIsCounted(t *testing.T) {
	s := newSandbox(t)
	src := t.TempDir()
	copyTree(t, logfixture.ClaudeRoot(), src)

	// A broken symlink, deliberately not chmod 000 (the container runs as
	// root, where mode bits are ignored, and that variant would silently
	// pass there — docs/acceptance.md §5).
	toolErrorDir := filepath.Join(src, "projects", logfixture.ClaudeToolErrorProject)
	broken := filepath.Join(toolErrorDir, "broken.jsonl")
	if err := os.Symlink("/nonexistent/nope.jsonl", broken); err != nil {
		t.Fatalf("creating broken symlink: %v", err)
	}

	res := s.run("sync", "--claude-path", src)
	wantCode(t, res, 0)
	wantStdout(t, res, "claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped, 1 file unreadable\n")
}
