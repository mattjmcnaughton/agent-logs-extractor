//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// AC-SANDBOX-01 †. The store half needs no duckdb; the export half does
// (a real `export duckdb` run), so requireDuckDB is checked right before
// that step — this sandbox has no duckdb installed, so the store
// assertions below still run and pass, and the test then reports SKIP
// rather than silently omitting the export half.
func TestAC_SANDBOX_01_HomeRedirectsStoreAndExport(t *testing.T) {
	s := newSandbox(t)
	s.seedFullClaudeTree()

	syncRes := s.run("sync")
	wantCode(t, syncRes, 0)
	if _, err := os.Stat(s.storeRoot()); err != nil {
		t.Fatalf("stat store root %s: %v", s.storeRoot(), err)
	}
	wantPrefix := filepath.Join(s.home, ".local", "share", "agent-logs-extractor")
	if !isUnder(s.storeRoot(), wantPrefix) {
		t.Errorf("store root %s is not under %s", s.storeRoot(), wantPrefix)
	}

	requireDuckDB(t)
	exportRes := s.run("export", "duckdb")
	wantCode(t, exportRes, 0)
	if _, err := os.Stat(s.defaultExportPath()); err != nil {
		t.Fatalf("stat default export path %s: %v", s.defaultExportPath(), err)
	}
	if !isUnder(s.defaultExportPath(), wantPrefix) {
		t.Errorf("export path %s is not under %s", s.defaultExportPath(), wantPrefix)
	}
}

// AC-SANDBOX-02
func TestAC_SANDBOX_02_HomeBeatsXDG(t *testing.T) {
	s := newSandbox(t)
	s.seedFullClaudeTree()
	xdg := t.TempDir()
	s.setEnv("XDG_DATA_HOME", xdg)

	res := s.run("sync")
	wantCode(t, res, 0)

	if _, err := os.Stat(s.storeRoot()); err != nil {
		t.Fatalf("stat store root %s: %v", s.storeRoot(), err)
	}
	// storeRoot(), given AGENT_LOGS_EXTRACTOR_HOME still set on this
	// sandbox, already resolves under <home>/.local/share — assert
	// directly that it is NOT under xdg, the failure mode this criterion
	// guards against.
	if isUnder(s.storeRoot(), xdg) {
		t.Errorf("store root %s landed under XDG_DATA_HOME %s; AGENT_LOGS_EXTRACTOR_HOME must win outright", s.storeRoot(), xdg)
	}
	entries, err := os.ReadDir(xdg)
	if err != nil {
		t.Fatalf("reading %s: %v", xdg, err)
	}
	if len(entries) != 0 {
		t.Errorf("XDG_DATA_HOME %s received %d entries, want none: %v", xdg, len(entries), entries)
	}
}

// AC-SANDBOX-03
func TestAC_SANDBOX_03_XDGGovernsDataDirOnly(t *testing.T) {
	s := newSandbox(t)
	s.seedFullClaudeTree()
	s.unsetAgentLogsExtractorHome()
	xdg := t.TempDir()
	s.setEnv("XDG_DATA_HOME", xdg)

	res := s.run("sync")
	wantCode(t, res, 0)
	wantStdout(t, res, fixtureSummary) // vendor root (~/.claude) still comes from $HOME

	wantStoreRoot := filepath.Join(xdg, "agent-logs-extractor", "store")
	if s.storeRoot() != wantStoreRoot {
		t.Fatalf("test bug: s.storeRoot() = %s, want %s", s.storeRoot(), wantStoreRoot)
	}
	if _, err := os.Stat(wantStoreRoot); err != nil {
		t.Errorf("stat %s: %v (want the store under XDG_DATA_HOME since AGENT_LOGS_EXTRACTOR_HOME is unset)", wantStoreRoot, err)
	}

	defaultDataDir := filepath.Join(s.home, ".local", "share", "agent-logs-extractor")
	if _, err := os.Stat(defaultDataDir); err == nil {
		t.Errorf("%s exists; want no data written under the default <home>/.local/share location once XDG_DATA_HOME is set", defaultDataDir)
	}
}
