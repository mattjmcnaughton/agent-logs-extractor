package duckdbcli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// duckdbTimeLayouts lists every layout DuckDB's `-json` output mode is
// known to render a TIMESTAMPTZ as, across versions and observed CI runs
// (see the TDD's C.4 version-sensitivity note). parseDuckDBTimestamp tries
// each in turn rather than assuming one.
//
// "2006-01-02 15:04:05.999999-07" was added after CI's first real run
// against a native duckdb CLI: duckdb -json renders a TIMESTAMPTZ with a
// space separator (not "T"), microsecond precision, and a two-digit UTC
// offset with no colon (e.g. "2026-08-14 03:00:21.947824+00") — a shape
// none of the pre-existing layouts (all "T"-separated, or space-separated
// with a colon/Z offset) matched. The trailing ".999999" (nines, not
// zeros) makes the fractional-seconds portion optional, so this same
// layout also parses a whole-second value with no fractional part at all.
var duckdbTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05.999999-07",
}

// parseDuckDBTimestamp parses a timestamp string as rendered by duckdb's
// -json output mode, trying every known layout. It is the pure core behind
// the integration test's parseDuckDBTime helper (cookbook_integration_test.go),
// split out here so it can be unit-tested without a duckdb binary or the
// integration build tag. On failure the returned error names the exact
// input string, which is what made the space-separated/microsecond/
// two-digit-offset shape diagnosable from a single CI run.
func parseDuckDBTimestamp(s string) (time.Time, error) {
	for _, layout := range duckdbTimeLayouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse duckdb timestamp %q with any known layout", s)
}

// TestParseDuckDBTimestamp is the unit test that would have caught the bug
// this package's first real CI run hit: parseDuckDBTime (via
// parseDuckDBTimestamp) failed on every timestamp duckdb -json actually
// emitted for a TIMESTAMPTZ, because no layout matched its space-separated,
// microsecond-precision, two-digit-offset shape. Table-driven and duckdb-
// free so it runs under plain `go test ./...`.
func TestParseDuckDBTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:  "space-separated microseconds two-digit offset (CI log Q1)",
			input: "2026-08-14 03:00:21.947824+00",
			want:  time.Date(2026, 8, 14, 3, 0, 21, 947824000, time.UTC),
		},
		{
			name:  "space-separated microseconds two-digit offset (CI log Q2)",
			input: "2026-08-13 10:02:51.165824+00",
			want:  time.Date(2026, 8, 13, 10, 2, 51, 165824000, time.UTC),
		},
		{
			name:  "space-separated microseconds two-digit offset (CI log Q3)",
			input: "2026-08-13 10:02:52.152824+00",
			want:  time.Date(2026, 8, 13, 10, 2, 52, 152824000, time.UTC),
		},
		{
			name:  "RFC3339 Z form",
			input: "2026-08-14T03:00:21.947824Z",
			want:  time.Date(2026, 8, 14, 3, 0, 21, 947824000, time.UTC),
		},
		{
			name:  "space-separated microseconds with colon offset (+00:00)",
			input: "2026-08-14 03:00:21.947824+00:00",
			want:  time.Date(2026, 8, 14, 3, 0, 21, 947824000, time.UTC),
		},
		{
			name:  "space-separated whole seconds, no fractional part, two-digit offset",
			input: "2026-08-14 03:00:21+00",
			want:  time.Date(2026, 8, 14, 3, 0, 21, 0, time.UTC),
		},
		{
			name:    "garbage input errors",
			input:   "not-a-timestamp",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuckDBTimestamp(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDuckDBTimestamp(%q) = %v, nil; want an error", tt.input, got)
				}
				if got := err.Error(); got == "" {
					t.Fatalf("parseDuckDBTimestamp(%q) returned an empty error message", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDuckDBTimestamp(%q) unexpected error: %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseDuckDBTimestamp(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseDuckDBTimestampErrorNamesTheInput pins that a failure's error
// message includes the exact, unmodified input string — the property that
// made the original CI failure diagnosable in one round rather than
// requiring another push-and-wait cycle.
func TestParseDuckDBTimestampErrorNamesTheInput(t *testing.T) {
	input := "definitely not a duckdb timestamp"
	_, err := parseDuckDBTimestamp(input)
	if err == nil {
		t.Fatalf("parseDuckDBTimestamp(%q): want error, got nil", input)
	}
	if got := err.Error(); !strings.Contains(got, input) {
		t.Errorf("error %q does not contain the exact input %q", got, input)
	}
}
