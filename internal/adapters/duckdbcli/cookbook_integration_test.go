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

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// fixtureOldestRFC3339 is the earliest timestamp in the invented Claude examples.
const fixtureOldestRFC3339 = "2020-01-01T00:00:00Z"

// rebaseStoreTimestamps shifts every timestamp in every session doc under
// <storeRoot>/sessions/claude/ by one uniform delta (D11): DuckDB has no
// clock override, so the acceptance test shifts the data instead of the
// query. The 25-hour anchor (now - 25h vs. the oldest fixture timestamp)
// is deliberate: the fixture span is under 17h, so demo lands
// outside the cookbook's `INTERVAL 1 DAY` window after rebasing while the
// other two sessions land inside it — the time predicate gets exercised in
// both directions, not just trivially satisfied or trivially empty. This
// touches only the temp store under storeRoot — never
// the generated source records, which this test leaves unchanged.
func rebaseStoreTimestamps(t *testing.T, storeRoot string) time.Duration {
	t.Helper()

	fixtureOldest, err := time.Parse(time.RFC3339, fixtureOldestRFC3339)
	if err != nil {
		t.Fatalf("parsing fixtureOldestRFC3339: %v", err)
	}
	// Truncate the delta to whole microseconds. DuckDB's TIMESTAMPTZ is
	// microsecond-precision while Go's time.Time is nanosecond-precision, so
	// a delta carrying sub-microsecond digits produces store timestamps
	// DuckDB physically cannot round-trip: it truncates on the way in, and
	// every rebased assertion then fails by a few hundred nanoseconds. The
	// fixture base timestamps are millisecond-precision, so a microsecond
	// delta keeps base+delta exactly representable on both sides.
	delta := time.Now().Add(-25 * time.Hour).Sub(fixtureOldest).Truncate(time.Microsecond)

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
// mode. The actual layout list (and the shapes it covers) lives in
// parseDuckDBTimestamp (duckdbtime_test.go, no build tag) so it can be
// unit-tested without a duckdb binary; this wrapper just adapts that pure
// function to the *testing.T-based helpers the cookbook assertions use.
func parseDuckDBTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected a string timestamp, got %T (%v)", v, v)
	}
	ts, err := parseDuckDBTimestamp(s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
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
	// Compare at microsecond granularity: that is DuckDB's actual TIMESTAMPTZ
	// resolution, so asserting anything finer would be asserting a property
	// the storage layer does not have. rebaseStoreTimestamps already truncates
	// the delta to whole microseconds, so this is belt-and-braces rather than
	// the primary defence.
	want := base.Add(delta).UTC().Truncate(time.Microsecond)
	gotTime := parseDuckDBTime(t, got).UTC().Truncate(time.Microsecond)
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
		rows := queryJSON(t, duckdbBin, out, CookbookQuery1)
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0: %v", len(rows), rows)
		}
	})

	t.Run("Q1 substituted sidechain returns 2 rows in order", func(t *testing.T) {
		q := strings.Replace(CookbookQuery1, "'fetch-context'", "'sidechain'", 1)
		rows := queryJSON(t, duckdbBin, out, q)
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
		}

		wantTimes := []string{"2020-01-01T12:00:00Z", "2020-01-01T12:00:03Z"}
		wantTexts := []string{"parent prompt", "child prompt"}
		for i, row := range rows {
			assertRebasedTime(t, row["created_at"], wantTimes[i], delta)
			if got := fmt.Sprint(row["text"]); got != wantTexts[i] {
				t.Errorf("row %d text = %q, want %q", i, got, wantTexts[i])
			}
		}
	})

	t.Run("Q1 substituted demo returns 0 rows (proves the time predicate filters)", func(t *testing.T) {
		q := strings.Replace(CookbookQuery1, "'fetch-context'", "'demo'", 1)
		rows := queryJSON(t, duckdbBin, out, q)
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0 (demo's only user message rebases to 25h ago, an hour outside the INTERVAL 1 DAY window — deliberately not exactly on the boundary, so this stays robust rather than flaky): %v", len(rows), rows)
		}
	})

	t.Run("Q2 verbatim binds and returns 0 rows", func(t *testing.T) {
		rows := queryJSON(t, duckdbBin, out, CookbookQuery2)
		if len(rows) != 0 {
			t.Errorf("got %d rows, want 0: %v", len(rows), rows)
		}
	})

	t.Run("Q2 substituted echo-hello returns 2 rows, excluding the Agent call and the unrelated Bash call", func(t *testing.T) {
		q := strings.Replace(CookbookQuery2, "'%git push%'", "'%echo hello%'", 1)
		rows := queryJSON(t, duckdbBin, out, q)
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
		}
		sort.Slice(rows, func(i, j int) bool {
			return fmt.Sprint(rows[i]["created_at"]) < fmt.Sprint(rows[j]["created_at"])
		})

		wantProjects := []string{"demo", "sidechain"}
		wantTimes := []string{"2020-01-01T00:00:02Z", "2020-01-01T12:00:04Z"}
		wantArgs := []map[string]any{
			{"command": "echo hello fixture", "description": "Example command"},
			{"command": "echo hello from subagent", "description": "Example child command"},
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
		rows := queryJSON(t, duckdbBin, out, CookbookQuery3)
		if len(rows) != 3 {
			t.Fatalf("got %d rows, want 3: %v", len(rows), rows)
		}
		sort.Slice(rows, func(i, j int) bool {
			return fmt.Sprint(rows[i]["project_name"]) < fmt.Sprint(rows[j]["project_name"])
		})

		wantProjects := []string{"demo", "sidechain", "tool-error"}
		wantLastActive := []string{"2020-01-01T00:00:04Z", "2020-01-01T12:00:08Z", "2020-01-01T13:00:04Z"}
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
