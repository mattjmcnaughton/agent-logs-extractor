//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
)

// AC-QUERY-05 †
//
// AC-QUERY-01–04 are not tested anywhere — see docs/acceptance.md §8 and
// R5, where they are marked `manual`: their subject is README prose rather
// than the binary's behavior. This criterion is the binary-level part that
// is worth an automated test: does the real `export duckdb` invocation
// actually carry the denormalized columns those cookbook queries depend
// on.
func TestAC_QUERY_05_DenormalizedColumns(t *testing.T) {
	bin := requireDuckDB(t)
	s := syncFullFixture(t)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	res := s.run("export", "duckdb", "--out", out)
	wantCode(t, res, 0)

	rows := duckdbJSON(t, bin, out, "SELECT vendor, project_name, project_path FROM messages LIMIT 1")
	if len(rows) != 1 {
		t.Fatalf("SELECT ... FROM messages LIMIT 1 returned %d rows, want 1", len(rows))
	}
	for _, col := range []string{"vendor", "project_name", "project_path"} {
		v, _ := rows[0][col].(string)
		if v == "" {
			t.Errorf("messages.%s = %q, want a non-empty denormalized value", col, rows[0][col])
		}
	}
}
