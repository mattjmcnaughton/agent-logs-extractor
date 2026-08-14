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
//
// "nothing is written outside the sandbox's home directory" (the second
// clause of this criterion's Then) is asserted by snapshotOutsideHome
// before/after each half: the os.Stat calls above only prove something
// was written *inside* s.home, never that nothing landed outside it. A1:
// this replaces two tautological isUnder(derived-path, derived-prefix)
// calls that could never fail (both operands were built from s.home by
// the same code path, with no XDG override active) with a check that
// actually exercises the claim — verified by reintroducing a stray
// os.TempDir() write and confirming this test then fails (see the FIX
// commit's message for the mutation result).
func TestAC_SANDBOX_01_HomeRedirectsStoreAndExport(t *testing.T) {
	s := newSandbox(t)
	s.seedFullClaudeTree()

	before := snapshotOutsideHome(t, s)

	syncRes := s.run("sync")
	wantCode(t, syncRes, 0)
	if _, err := os.Stat(s.storeRoot()); err != nil {
		t.Fatalf("stat store root %s: %v", s.storeRoot(), err)
	}

	afterSync := snapshotOutsideHome(t, s)
	assertNoNewOutsideEntries(t, s, before, afterSync)

	requireDuckDB(t)
	exportRes := s.run("export", "duckdb")
	wantCode(t, exportRes, 0)
	if _, err := os.Stat(s.defaultExportPath()); err != nil {
		t.Fatalf("stat default export path %s: %v", s.defaultExportPath(), err)
	}

	afterExport := snapshotOutsideHome(t, s)
	assertNoNewOutsideEntries(t, s, afterSync, afterExport)
}

// outsideSnapshot captures, at one point in time, every place a stray
// write outside a sandbox's home directory could land without being lost
// in a huge, foreign, possibly-permission-restricted directory tree:
//   - tmpTop: the entry names directly under os.TempDir() (the shared OS
//     temp root every sandbox's own t.TempDir() home is created under) — a
//     top-level listing, not a recursive walk, since a full walk would
//     descend into unrelated processes' and other tests' directories this
//     one has no business reading.
//   - homeFiles: every file anywhere under s.home's own parent directory
//     (t.TempDir()'s private per-test root, of which s.home is the sole
//     pre-existing child), excluding s.home's own subtree — small and
//     entirely owned by this test, so walking it fully is cheap and safe.
type outsideSnapshot struct {
	tmpTop    map[string]bool
	homeFiles map[string]bool
}

// snapshotOutsideHome builds an outsideSnapshot for s at the current
// moment.
func snapshotOutsideHome(t *testing.T, s *sandbox) outsideSnapshot {
	t.Helper()
	tmpTop := map[string]bool{}
	if entries, err := os.ReadDir(os.TempDir()); err == nil {
		for _, e := range entries {
			tmpTop[e.Name()] = true
		}
	}
	homeFiles := map[string]bool{}
	_ = walkFiles(filepath.Dir(s.home), func(path string) {
		if !isUnder(path, s.home) {
			homeFiles[path] = true
		}
	})
	return outsideSnapshot{tmpTop: tmpTop, homeFiles: homeFiles}
}

// assertNoNewOutsideEntries fails the test for every entry present in
// after but absent from before — the load-bearing half of AC-SANDBOX-01's
// "nothing is written outside the sandbox's home directory" clause.
func assertNoNewOutsideEntries(t *testing.T, s *sandbox, before, after outsideSnapshot) {
	t.Helper()
	for name := range after.tmpTop {
		if !before.tmpTop[name] {
			t.Errorf("new entry %q appeared directly under %s; nothing may be written outside the sandbox home %s", name, os.TempDir(), s.home)
		}
	}
	for path := range after.homeFiles {
		if !before.homeFiles[path] {
			t.Errorf("new file %s appeared outside the sandbox home %s; nothing may be written outside it", path, s.home)
		}
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
