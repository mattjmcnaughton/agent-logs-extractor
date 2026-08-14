//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// syncFullFixture seeds and syncs the full Claude fixture tree into a fresh
// sandbox, failing the test immediately if the sync itself does not
// succeed — every export test below builds on it.
func syncFullFixture(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t)
	res := s.run("sync", "--claude-path", logfixture.ClaudeRoot())
	if res.code != 0 {
		t.Fatalf("seeding sync failed: code=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
	}
	return s
}

// AC-EXPORT-01 †
func TestAC_EXPORT_01_WritesTheFile(t *testing.T) {
	requireDuckDB(t)
	s := syncFullFixture(t)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	res := s.run("export", "duckdb", "--out", out)
	wantCode(t, res, 0)
	wantStdout(t, res, "wrote "+out+"\n")

	if _, err := os.Stat(out); err != nil {
		t.Fatalf("stat %s: %v", out, err)
	}
	if _, err := os.Stat(out + ".wal"); err == nil {
		t.Errorf("%s.wal survives beside the export; want it folded into the database file or removed", out)
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Dir(out), err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".export-") {
			t.Errorf("leftover temp export directory %s beside %s", e.Name(), out)
		}
	}
}

// AC-EXPORT-02 †
func TestAC_EXPORT_02_DefaultOutPath(t *testing.T) {
	requireDuckDB(t)
	s := syncFullFixture(t)

	res := s.run("export", "duckdb")
	wantCode(t, res, 0)

	if _, err := os.Stat(s.defaultExportPath()); err != nil {
		t.Fatalf("stat default export path %s: %v", s.defaultExportPath(), err)
	}
}

// AC-EXPORT-03 †
func TestAC_EXPORT_03_TablesMatchTheSyncSummary(t *testing.T) {
	bin := requireDuckDB(t)
	s := syncFullFixture(t)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	res := s.run("export", "duckdb", "--out", out)
	wantCode(t, res, 0)

	wantCounts := map[string]int{"sessions": 3, "messages": 15, "tool_calls": 4}
	for table, want := range wantCounts {
		rows := duckdbJSON(t, bin, out, "SELECT count(*) AS n FROM "+table)
		if len(rows) != 1 {
			t.Fatalf("SELECT count(*) FROM %s returned %d rows, want 1", table, len(rows))
		}
		if got := asInt(t, rows[0]["n"]); got != want {
			t.Errorf("%s row count = %d, want %d", table, got, want)
		}
	}
}

// AC-EXPORT-04
func TestAC_EXPORT_04_MissingBinaryHint(t *testing.T) {
	s := syncFullFixture(t)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	res := s.runWithEnv(map[string]string{"PATH": pathWithoutDuckDB()}, "export", "duckdb", "--out", out)
	wantCode(t, res, 1)
	wantStderrContains(t, res, "duckdb binary not found")
	wantStderrContains(t, res, "https://duckdb.org/docs/installation/")
	wantStderrContains(t, res, "sync")
}

// AC-EXPORT-05
func TestAC_EXPORT_05_NoStoreYet(t *testing.T) {
	s := newSandbox(t) // never synced

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	res := s.run("export", "duckdb", "--out", out)
	wantCode(t, res, 1)
	wantStderrContains(t, res, "no canonical store")
	wantStderrContains(t, res, s.storeRoot())
	wantStderrContains(t, res, "run `sync` first")
}

// AC-EXPORT-06 †
func TestAC_EXPORT_06_EmptyStoreExports(t *testing.T) {
	bin := requireDuckDB(t)
	s := newSandbox(t)

	syncRes := s.run("sync") // ~/.claude never seeded: an empty, but committed, store
	if syncRes.code != 0 {
		t.Fatalf("seeding sync failed: code=%d stdout=%q stderr=%q", syncRes.code, syncRes.stdout, syncRes.stderr)
	}

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	res := s.run("export", "duckdb", "--out", out)
	wantCode(t, res, 0)

	for _, table := range []string{"sessions", "messages", "tool_calls"} {
		rows := duckdbJSON(t, bin, out, "SELECT count(*) AS n FROM "+table)
		if len(rows) != 1 {
			t.Fatalf("SELECT count(*) FROM %s returned %d rows, want 1", table, len(rows))
		}
		if got := asInt(t, rows[0]["n"]); got != 0 {
			t.Errorf("%s row count = %d, want 0 (empty store)", table, got)
		}
	}
}
