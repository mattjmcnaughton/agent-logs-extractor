//go:build integration

package duckdbcli_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/duckdbcli"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/jsonlstore"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// These tests exercise the real duckdb CLI, so they only run when it is on
// PATH — requireDuckDB skips (or fails, under ALX_REQUIRE_DUCKDB=1) rather
// than faking or shimming the binary (see the ticket's rule 8: no fake
// duckdb ever goes on PATH). At authoring time this repository's sandbox
// has no duckdb and no working container daemon, so every test in this
// file was never executed locally — CI is their first real run.

// requireDuckDB resolves the duckdb binary on PATH, or skips the test — or,
// under ALX_REQUIRE_DUCKDB=1 (CI's native gate-expensive job), fails it
// instead of skipping, so a duckdb-install problem in that job is
// unmistakably a test failure rather than a silent skip.
func requireDuckDB(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("duckdb")
	if err == nil {
		return bin
	}
	if os.Getenv("ALX_REQUIRE_DUCKDB") != "" {
		t.Fatalf("duckdb not found on PATH and ALX_REQUIRE_DUCKDB is set: %v", err)
	}
	t.Skipf("duckdb not on PATH; skipping (set ALX_REQUIRE_DUCKDB=1 to make this fatal): %v", err)
	return ""
}

// syncSources builds a real canonical store at storeRoot from the given
// vendor sources, using the real claudesource and jsonlstore adapters (no
// fake anywhere in this file) — the use-case-level counterpart to
// sync_integration_test.go, feeding duckdbcli exactly what a real `sync`
// would have left on disk.
func syncSources(t *testing.T, storeRoot string, sources []sync.SourceRequest) sync.Summary {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	store := jsonlstore.New(afero.NewOsFs(), storeRoot, log)
	src := claudesource.New(log)
	s := sync.New([]ports.ConversationSource{src}, store, log)
	summary, err := s.Run(context.Background(), sync.Request{Sources: sources})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return summary
}

func syncClaudeFixtures(t *testing.T, storeRoot string) sync.Summary {
	t.Helper()
	return syncSources(t, storeRoot, []sync.SourceRequest{{Vendor: model.VendorClaude, Root: logfixture.ClaudeRoot()}})
}

// exportStore runs the real Adapter.Export against storeRoot, writing to
// out.
func exportStore(t *testing.T, storeRoot, out string) error {
	t.Helper()
	a := duckdbcli.New(slog.New(slog.DiscardHandler))
	return a.Export(context.Background(), ports.ExportRequest{StoreRoot: storeRoot, Out: out})
}

// queryJSON runs query against the snapshot at dbPath via the real duckdb
// CLI's -json output mode (E.3) and parses the result. TZ=UTC keeps output
// formatting deterministic across CI runners in different local zones — a
// TIMESTAMPTZ column is an absolute instant either way, so this affects
// only display, never which rows a WHERE clause selects. A command
// failure (non-zero exit — including "does not bind") fails the test
// immediately with the query and duckdb's own output attached, which is
// exactly the signal the "verbatim" cookbook assertions rely on (D11):
// their job is to bind and exit 0, not merely to be well-formed SQL.
func queryJSON(t *testing.T, duckdbBin, dbPath, query string) []map[string]any {
	t.Helper()
	cmd := exec.Command(duckdbBin, dbPath, "-json", "-c", query)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("duckdb query failed: %v\nquery:\n%s\noutput:\n%s", err, query, out)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		t.Fatalf("parsing duckdb -json output: %v\nquery:\n%s\noutput:\n%s", err, query, out)
	}
	return rows
}

// asInt coerces a duckdb -json numeric value (unmarshaled as float64 by
// encoding/json) to int, tolerating a couple of alternate shapes so this
// helper does not need to change if duckdb's JSON writer ever changes how
// it renders integers.
func asInt(t *testing.T, v any) int {
	t.Helper()
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, err := strconv.Atoi(x)
		if err != nil {
			t.Fatalf("asInt: parsing %q: %v", x, err)
		}
		return n
	default:
		t.Fatalf("asInt: unexpected type %T (%v)", v, v)
		return 0
	}
}

func countTable(t *testing.T, duckdbBin, dbPath, table string) int {
	t.Helper()
	rows := queryJSON(t, duckdbBin, dbPath, "SELECT count(*) AS n FROM "+table)
	if len(rows) != 1 {
		t.Fatalf("SELECT count(*) FROM %s returned %d rows, want 1", table, len(rows))
	}
	return asInt(t, rows[0]["n"])
}

// --- I1: empty / absent sessions/ ------------------------------------------

// TestExportEmptyStoreProducesEmptyRelations closes #7's flagged gap 1:
// read_json errors on a zero-match glob, so a present-but-empty
// "sessions/" must not make export fail — it must produce three present,
// empty relations instead.
func TestExportEmptyStoreProducesEmptyRelations(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storeRoot, "sessions"), 0o700); err != nil {
		t.Fatalf("seeding empty sessions dir: %v", err)
	}
	out := filepath.Join(t.TempDir(), "logs.duckdb")

	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export: %v", err)
	}

	for _, table := range []string{"sessions", "messages", "tool_calls"} {
		if got := countTable(t, duckdbBin, out, table); got != 0 {
			t.Errorf("%s count = %d, want 0", table, got)
		}
	}
}

// TestExportAbsentSessionsDirIsNotAnError is TestExportEmptyStoreProducesEmptyRelations's
// counterpart for a store root that exists but has no "sessions/" at all
// (as opposed to a present-but-empty one) — both are legitimate empty
// exports (A4), never ErrNoStore.
func TestExportAbsentSessionsDirIsNotAnError(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir() // "sessions/" deliberately never created
	out := filepath.Join(t.TempDir(), "logs.duckdb")

	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got := countTable(t, duckdbBin, out, "sessions"); got != 0 {
		t.Errorf("sessions count = %d, want 0", got)
	}
}

// --- I2: fixture store row counts ------------------------------------------

// TestExportFixtureStoreRowCounts pins the export's row counts against
// sync's own printed summary for the same fixtures ("claude: 3 sessions,
// 15 messages, 4 tool calls, 23 records skipped" —
// internal/adapters/cli/sync_integration_test.go), so the two can never
// silently drift apart.
func TestExportFixtureStoreRowCounts(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir()
	syncClaudeFixtures(t, storeRoot)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export: %v", err)
	}

	want := map[string]int{"sessions": 3, "messages": 15, "tool_calls": 4}
	for table, n := range want {
		if got := countTable(t, duckdbBin, out, table); got != n {
			t.Errorf("%s count = %d, want %d", table, got, n)
		}
	}
}

// --- I3: denormalization ----------------------------------------------------

// TestExportDenormalizesSessionFieldsOntoChildren pins TDD core decision 5:
// every messages/tool_calls row's denormalized vendor/project_name/
// project_path must agree exactly with its own session's row — the whole
// point of denormalizing is that the README cookbook queries never need a
// join to filter on these fields.
func TestExportDenormalizesSessionFieldsOntoChildren(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir()
	syncClaudeFixtures(t, storeRoot)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Guard against a vacuous pass: the mismatch-count assertions below
	// would trivially read 0 against empty tables too.
	if got := countTable(t, duckdbBin, out, "messages"); got == 0 {
		t.Fatal("messages table is empty; the denormalization check below would be vacuous")
	}
	if got := countTable(t, duckdbBin, out, "tool_calls"); got == 0 {
		t.Fatal("tool_calls table is empty; the denormalization check below would be vacuous")
	}

	rows := queryJSON(t, duckdbBin, out, `
SELECT count(*) AS n
FROM messages m JOIN sessions s USING (session_id)
WHERE m.vendor != s.vendor OR m.project_name != s.project_name OR m.project_path != s.project_path`)
	if got := asInt(t, rows[0]["n"]); got != 0 {
		t.Errorf("messages: %d rows have denormalized fields diverging from their session, want 0", got)
	}

	rows = queryJSON(t, duckdbBin, out, `
SELECT count(*) AS n
FROM tool_calls t JOIN sessions s USING (session_id)
WHERE t.vendor != s.vendor OR t.project_name != s.project_name OR t.project_path != s.project_path`)
	if got := asInt(t, rows[0]["n"]); got != 0 {
		t.Errorf("tool_calls: %d rows have denormalized fields diverging from their session, want 0", got)
	}
}

// --- I4: raw / arguments stay queryable JSON --------------------------------

// TestExportPreservesRawAndArgumentsAsQueryableJSON pins that messages.raw
// and tool_calls.arguments are read_json's own JSON type, not opaque text
// — so they can be queried with DuckDB's JSON functions, not just LIKE'd
// as strings.
func TestExportPreservesRawAndArgumentsAsQueryableJSON(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir()
	syncClaudeFixtures(t, storeRoot)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Every message's raw vendor record carries a "type" field
	// (docs/technical/tdd-mvp.md's Claude mapping notes).
	rows := queryJSON(t, duckdbBin, out, `SELECT count(*) AS n FROM messages WHERE json_extract_string(raw, '$.type') IS NULL`)
	if got := asInt(t, rows[0]["n"]); got != 0 {
		t.Errorf("%d messages rows have unparseable/absent raw.type, want 0", got)
	}

	// Every Bash tool call's arguments carries a "command" field.
	rows = queryJSON(t, duckdbBin, out, `SELECT count(*) AS n FROM tool_calls WHERE tool_name = 'Bash' AND json_extract_string(arguments, '$.command') IS NULL`)
	if got := asInt(t, rows[0]["n"]); got != 0 {
		t.Errorf("%d Bash tool_calls rows have unparseable/absent arguments.command, want 0", got)
	}
}

// --- I5: atomic overwrite ----------------------------------------------------

// TestExportOverwritesAPreviousSnapshotAtomically runs two exports to the
// same --out and asserts no ".export-*" temp directory and no "*.wal" file
// survive, and the second export's data is intact (D4).
func TestExportOverwritesAPreviousSnapshotAtomically(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir()
	syncClaudeFixtures(t, storeRoot)

	outDir := t.TempDir()
	out := filepath.Join(outDir, "logs.duckdb")

	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("first Export: %v", err)
	}
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("second Export: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("reading %s: %v", outDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".export-") {
			t.Errorf("leftover temp export directory %q after two clean exports", name)
		}
		if strings.HasSuffix(name, ".wal") {
			t.Errorf("leftover WAL file %q after two clean exports", name)
		}
	}

	if got := countTable(t, duckdbBin, out, "sessions"); got != 3 {
		t.Errorf("sessions count after second export = %d, want 3", got)
	}
}

// --- I6: failure leaves previous output intact ------------------------------

// TestExportFailsAndLeavesThePreviousOutputIntact seeds a malformed .json
// file directly into the store (bypassing jsonlstore, which would never
// write one) so read_json fails and -bail makes the whole script exit
// non-zero — proving both the non-zero-exit error plumbing and that a
// failed export never touches a previous successful one.
func TestExportFailsAndLeavesThePreviousOutputIntact(t *testing.T) {
	requireDuckDB(t)
	storeRoot := t.TempDir()
	syncClaudeFixtures(t, storeRoot)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("first (clean) Export: %v", err)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading first export: %v", err)
	}

	badPath := filepath.Join(storeRoot, "sessions", "claude", "zzz-malformed.json")
	if err := os.WriteFile(badPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seeding malformed doc: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(badPath) })

	if err := exportStore(t, storeRoot, out); err == nil {
		t.Fatal("Export over a store containing a malformed doc: want error, got nil")
	}

	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading export after the failed run: %v", err)
	}
	if string(after) != string(before) {
		t.Error("export output changed after a failed run; want it byte-identical to the previous successful export")
	}
}

// --- I7: pathological fixtures ----------------------------------------------

// TestExportHandlesThePathologicalFixtureStore syncs the pathological
// fixture tree (duplicate session ids, a malformed line, an unknown record
// type, a doc that yields nothing) and exports the resulting store — a
// store built from real, if adversarial, adapter output, as opposed to a
// hand-seeded fixture of duckdbcli's own.
func TestExportHandlesThePathologicalFixtureStore(t *testing.T) {
	duckdbBin := requireDuckDB(t)
	storeRoot := t.TempDir()
	summary := syncSources(t, storeRoot, []sync.SourceRequest{{Vendor: model.VendorClaude, Root: logfixture.PathologicalClaudeRoot()}})

	wantSessions := 0
	for _, v := range summary.Vendors {
		wantSessions += v.Sessions
	}
	if wantSessions == 0 {
		t.Fatalf("sync of the pathological fixtures reported 0 sessions; nothing for this test to verify")
	}

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export over the pathological fixture store: %v", err)
	}

	if got := countTable(t, duckdbBin, out, "sessions"); got != wantSessions {
		t.Errorf("sessions count = %d, want %d (matching sync's own summary over the pathological fixtures)", got, wantSessions)
	}
}
