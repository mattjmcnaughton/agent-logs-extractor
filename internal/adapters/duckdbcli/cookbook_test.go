package duckdbcli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readmeSQLFence matches a fenced ```sql ... ``` block, capturing its body.
var readmeSQLFence = regexp.MustCompile("(?s)```sql\n(.*?)```")

// TestCookbookQueriesMatchTheREADME parses every ```sql fence out of
// README.md's "Querying" section and fails if it diverges from the
// CookbookQuery1/2/3 constants this package's integration acceptance test
// (TestCookbookQueries) actually runs against a real export. This is the
// anti-drift guard D11 calls for: the README's cookbook is user-facing
// documentation, never edited to make a test pass (see D11's note on why
// each query is asserted twice, verbatim and substituted, rather than
// changing the README) — so the queries the acceptance test proves must
// instead be kept byte-identical to what a reader would actually copy out
// of the README, mechanically, here.
func TestCookbookQueriesMatchTheREADME(t *testing.T) {
	readme, err := os.ReadFile(readmePath(t))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}

	matches := readmeSQLFence.FindAllStringSubmatch(string(readme), -1)
	got := make([]string, 0, len(matches))
	for _, m := range matches {
		got = append(got, strings.TrimSpace(m[1]))
	}

	want := []string{CookbookQuery1, CookbookQuery2, CookbookQuery3}

	if len(got) != len(want) {
		t.Fatalf("README.md has %d ```sql fences, want %d (CookbookQuery1..3):\n%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("README.md ```sql fence #%d does not match CookbookQuery%d:\n--- README ---\n%s\n--- constant ---\n%s", i+1, i+1, got[i], want[i])
		}
	}
}

// readmePath resolves README.md at the repository root, relative to this
// package's own source file — the same runtime.Caller pattern
// internal/testing/logfixture.Dir() uses, so this test works regardless of
// the caller's own working directory.
func readmePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	// internal/adapters/duckdbcli -> repo root is three directories up.
	return filepath.Join(dir, "..", "..", "..", "README.md")
}
