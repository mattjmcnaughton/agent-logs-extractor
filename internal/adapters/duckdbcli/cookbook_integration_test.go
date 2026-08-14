//go:build integration

package duckdbcli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/duckdbcli"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// fixtureOldestRFC3339 is the earliest timestamp across the committed
// Claude fixtures (fixture-project's opening user message,
// internal/testing/logfixture/claude/projects/-tmp-.../94ba8eae-....jsonl).
// Verified directly against a real store built by `sync` over the
// fixtures (see the ticket's report for the cross-check).
const fixtureOldestRFC3339 = "2026-08-12T18:19:09.55Z"

// rebaseStoreTimestamps shifts every timestamp in every session doc under
// <storeRoot>/sessions/claude/ by one uniform delta (D11): DuckDB has no
// clock override, so the acceptance test shifts the data instead of the
// query. The 25-hour anchor (now - 25h vs. the oldest fixture timestamp)
// is deliberate: the fixture span is under 17h, so fixture-project lands
// outside the cookbook's `INTERVAL 1 DAY` window after rebasing while the
// other two sessions land inside it — the time predicate gets exercised in
// both directions, not just trivially satisfied or trivially empty. This
// touches only the temp store under storeRoot — never
// internal/testing/logfixture/, which must stay verbatim ground truth.
func rebaseStoreTimestamps(t *testing.T, storeRoot string) time.Duration {
	t.Helper()

	fixtureOldest, err := time.Parse(time.RFC3339, fixtureOldestRFC3339)
	if err != nil {
		t.Fatalf("parsing fixtureOldestRFC3339: %v", err)
	}
	delta := time.Now().Add(-25 * time.Hour).Sub(fixtureOldest)

	claudeDir := filepath.Join(storeRoot, "sessions", "claude")
	entries, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatalf("reading %s: %v", claudeDir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(claudeDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		var doc model.SessionDoc
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("unmarshaling %s: %v", path, err)
		}

		doc.Session.StartedAt = doc.Session.StartedAt.Add(delta)
		doc.Session.EndedAt = doc.Session.EndedAt.Add(delta)
		for i := range doc.Messages {
			doc.Messages[i].CreatedAt = doc.Messages[i].CreatedAt.Add(delta)
		}
		for i := range doc.ToolCalls {
			doc.ToolCalls[i].CreatedAt = doc.ToolCalls[i].CreatedAt.Add(delta)
		}

		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshaling rebased %s: %v", path, err)
		}
		out = append(out, '\n')
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatalf("writing rebased %s: %v", path, err)
		}
	}

	return delta
}

// parseDuckDBTime parses a timestamp as rendered by duckdb's -json output
// mode, trying every layout DuckDB's JSON writer is known to use across
// versions (see the TDD's C.4 version-sensitivity note) rather than
// assuming one.
func parseDuckDBTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected a string timestamp, got %T (%v)", v, v)
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	t.Fatalf("could not parse duckdb timestamp %q with any known layout", s)
	return time.Time{}
}

// assertRebasedTime asserts got equals wantBaseRFC3339 + delta, comparing
// as instants (time.Time.Equal after normalizing to UTC) so a difference
// in how duckdb renders a TIMESTAMPTZ's offset can never cause a false
// failure.
func assertRebasedTime(t *testing.T, got any, wantBaseRFC3339 string, delta time.Duration) {
	t.Helper()
	base, err := time.Parse(time.RFC3339, wantBaseRFC3339)
	if err != nil {
		t.Fatalf("parsing want base time %q: %v", wantBaseRFC3339, err)
	}
	want := base.Add(delta).UTC()
	gotTime := parseDuckDBTime(t, got).UTC()
	if !gotTime.Equal(want) {
		t.Errorf("timestamp = %v, want %v (base %s + delta %v)", gotTime, want, wantBaseRFC3339, delta)
	}
}

// assertArgumentsEqual compares a tool_calls.arguments cell against want as
// parsed JSON, not bytes (E.3) — tolerant of the arguments value coming
// back from duckdb's -json writer either as a nested JSON object/array
// directly, or as a JSON-encoded string, since which shape a given DuckDB
// version chooses for a JSON-typed column is exactly the kind of detail
// C.4 flags as version-sensitive.
func assertArgumentsEqual(t *testing.T, got any, want map[string]any) {
	t.Helper()
	if s, ok := got.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			t.Fatalf("arguments value %q is not valid JSON: %v", s, err)
		}
		got = parsed
	}
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshaling got arguments: %v", err)
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshaling want arguments: %v", err)
	}
	var gotNorm, wantNorm any
	if err := json.Unmarshal(gotBytes, &gotNorm); err != nil {
		t.Fatalf("normalizing got arguments: %v", err)
	}
	if err := json.Unmarshal(wantBytes, &wantNorm); err != nil {
		t.Fatalf("normalizing want arguments: %v", err)
	}
	if !reflect.DeepEqual(gotNorm, wantNorm) {
		t.Errorf("arguments = %s, want %s", gotBytes, wantBytes)
	}
}

// TestCookbookQueries is the ticket's literal "done when": the README's
// three cookbook queries, run against a real export of the (timestamp-
// rebased) fixtures, returning the rows the ticket's plan derived from a
// real store built by `sync` (see the report for the cross-check against
// that store's own contents). This test was never executed in the
// authoring environment (no duckdb binary, no working container daemon) —
// CI is its first real run; expect to iterate once it reports back.
//
// Each query that has a literal the fixtures don't match (Q1's
// project_name = 'fetch-context', Q2's arguments LIKE '%git push%') is
// asserted twice, per D11: once verbatim, proving the query binds and
// executes against the real schema (0 rows expected, but a schema bug
// would show up as a bind/type error, not a row-count mismatch); once with
// one documented substitution, proving the data and the join-free
// denormalization actually work. Q3 has neither a non-matching literal nor
// a time predicate, so it is asserted only once, verbatim.
func TestCookbookQueries(t *testing.T) {
	duckdbBin := requireDuckDB(t)

	storeRoot := t.TempDir()
	syncClaudeFixtures(t, storeRoot)
	delta := rebaseStoreTimestamps(t, storeRoot)

	out := filepath.Join(t.TempDir(), "logs.duckdb")
	if err := exportStore(t, storeRoot, out); err != nil {
		t.Fatalf("Export: %v", err)
	}

	t.Run("Q1 verbatim binds and returns 0 rows", func(t *testing.T) {
		rows := queryJSON(t, duckdbBin, out, duckdbcli.CookbookQuery1)
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0: %v", len(rows), rows)
		}
	})

	t.Run("Q1 substituted fixture-sidechain returns 2 rows in order", func(t *testing.T) {
		q := strings.Replace(duckdbcli.CookbookQuery1, "'fetch-context'", "'fixture-sidechain'", 1)
		rows := queryJSON(t, duckdbBin, out, q)
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
		}

		wantTimes := []string{"2026-08-13T11:16:42.55Z", "2026-08-13T11:16:45.684Z"}
		wantTexts := []string{
			"Use the Task tool to launch one general-purpose subagent whose prompt is: run the bash command 'echo hello from subagent' and report its output. After the subagent finishes, reply with exactly: done",
			"Run the bash command 'echo hello from subagent' and report its output.",
		}
		for i, row := range rows {
			assertRebasedTime(t, row["created_at"], wantTimes[i], delta)
			if got := fmt.Sprint(row["text"]); got != wantTexts[i] {
				t.Errorf("row %d text = %q, want %q", i, got, wantTexts[i])
			}
		}
	})

	t.Run("Q1 substituted fixture-project returns 0 rows (proves the time predicate filters)", func(t *testing.T) {
		q := strings.Replace(duckdbcli.CookbookQuery1, "'fetch-context'", "'fixture-project'", 1)
		rows := queryJSON(t, duckdbBin, out, q)
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0 (fixture-project's only user message rebases to exactly the INTERVAL 1 DAY boundary): %v", len(rows), rows)
		}
	})

	t.Run("Q2 verbatim binds and returns 0 rows", func(t *testing.T) {
		rows := queryJSON(t, duckdbBin, out, duckdbcli.CookbookQuery2)
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0: %v", len(rows), rows)
		}
	})

	t.Run("Q2 substituted echo-hello returns 2 rows, excluding the Agent call and the unrelated Bash call", func(t *testing.T) {
		q := strings.Replace(duckdbcli.CookbookQuery2, "'%git push%'", "'%echo hello%'", 1)
		rows := queryJSON(t, duckdbBin, out, q)
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
		}
		sort.Slice(rows, func(i, j int) bool {
			return fmt.Sprint(rows[i]["created_at"]) < fmt.Sprint(rows[j]["created_at"])
		})

		wantProjects := []string{"fixture-project", "fixture-sidechain"}
		wantTimes := []string{"2026-08-12T18:19:11.768Z", "2026-08-13T11:16:47.606Z"}
		wantArgs := []map[string]any{
			{"command": "echo hello fixture", "description": "Echo test string"},
			{"command": "echo hello from subagent", "description": "Echo test message"},
		}
		for i, row := range rows {
			if got := fmt.Sprint(row["vendor"]); got != "claude" {
				t.Errorf("row %d vendor = %q, want claude", i, got)
			}
			if got := fmt.Sprint(row["project_name"]); got != wantProjects[i] {
				t.Errorf("row %d project_name = %q, want %q", i, got, wantProjects[i])
			}
			assertRebasedTime(t, row["created_at"], wantTimes[i], delta)
			assertArgumentsEqual(t, row["arguments"], wantArgs[i])
		}
	})

	t.Run("Q3 verbatim returns 3 rows, one per project, all counts 1", func(t *testing.T) {
		rows := queryJSON(t, duckdbBin, out, duckdbcli.CookbookQuery3)
		if len(rows) != 3 {
			t.Fatalf("got %d rows, want 3: %v", len(rows), rows)
		}
		sort.Slice(rows, func(i, j int) bool {
			return fmt.Sprint(rows[i]["project_name"]) < fmt.Sprint(rows[j]["project_name"])
		})

		wantProjects := []string{"fixture-project", "fixture-sidechain", "fixture-tool-error"}
		wantLastActive := []string{"2026-08-12T18:19:12.755Z", "2026-08-13T11:16:49.806Z", "2026-08-13T11:17:29.81Z"}
		for i, row := range rows {
			if got := fmt.Sprint(row["project_name"]); got != wantProjects[i] {
				t.Errorf("row %d project_name = %q, want %q", i, got, wantProjects[i])
			}
			if got := fmt.Sprint(row["vendor"]); got != "claude" {
				t.Errorf("row %d vendor = %q, want claude", i, got)
			}
			if got := asInt(t, row["sessions"]); got != 1 {
				t.Errorf("row %d sessions = %d, want 1", i, got)
			}
			assertRebasedTime(t, row["last_active"], wantLastActive[i], delta)
		}
	})
}
