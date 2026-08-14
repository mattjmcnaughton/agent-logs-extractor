package duckdbcli

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// TestScriptPopulatedMatchesGolden pins Script(true) byte-for-byte against a
// committed golden file, so a change to the generated SQL is visible in the
// diff rather than only in a failing assertion message.
func TestScriptPopulatedMatchesGolden(t *testing.T) {
	assertMatchesGolden(t, "testdata/script_populated.golden.sql", Script(true))
}

// TestScriptEmptyMatchesGolden is TestScriptPopulatedMatchesGolden's
// counterpart for the empty-store branch (Script(false)).
func TestScriptEmptyMatchesGolden(t *testing.T) {
	assertMatchesGolden(t, "testdata/script_empty.golden.sql", Script(false))
}

func assertMatchesGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v", path, err)
	}
	if got != string(want) {
		t.Errorf("script does not match golden file %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// TestBothScriptFormsDeclareTheSameSchema pins C.2's contract: the
// populated and empty forms differ only in the header comment and the
// _docs declaration — every relation-building statement from "CREATE TABLE
// sessions AS" onward is byte-identical, so the two branches can never
// silently drift into declaring different columns.
func TestBothScriptFormsDeclareTheSameSchema(t *testing.T) {
	populated := Script(true)
	empty := Script(false)

	pIdx := strings.Index(populated, schemaBoundary)
	eIdx := strings.Index(empty, schemaBoundary)
	if pIdx < 0 {
		t.Fatalf("Script(true) does not contain %q", schemaBoundary)
	}
	if eIdx < 0 {
		t.Fatalf("Script(false) does not contain %q", schemaBoundary)
	}

	pTail := populated[pIdx:]
	eTail := empty[eIdx:]
	if pTail != eTail {
		t.Errorf("script tails diverge from %q onward:\n--- populated ---\n%s\n--- empty ---\n%s", schemaBoundary, pTail, eTail)
	}
}

// docsColumnKeys are the read_json columns={...} map's own keys, exactly as
// populatedHeader and emptyHeader hardcode them ('session', 'messages',
// 'tool_calls') — the single point of contact between model.SessionDoc's
// own top-level json tags and the generated SQL. Unlike sessionStructType
// et al., these three literals live inline in the header templates rather
// than as a named constant of their own, so this test string is kept in
// sync with script.go by hand.
const docsColumnKeys = "session messages tool_calls"

// TestScriptColumnsCoverEveryModelJSONTag reflects over the model structs
// that make up a SessionDoc — SessionDoc itself, plus each of its three
// fields' element types — and asserts every json tag name appears in the
// matching read_json column-type string. This catches "someone added a
// model field and forgot the export": read_json is handed an explicit
// columns={...} schema (not auto-detection, per model.go's #9 note), so a
// forgotten field would silently vanish from every export rather than
// erroring. The SessionDoc case specifically catches a fourth top-level
// field: read_json's columns={...} map silently drops any key it isn't
// told about, so a new SessionDoc field would otherwise vanish from every
// export with no test here failing.
func TestScriptColumnsCoverEveryModelJSONTag(t *testing.T) {
	cases := []struct {
		name       string
		model      any
		structType string
	}{
		{"SessionDoc", model.SessionDoc{}, docsColumnKeys},
		{"Session", model.Session{}, sessionStructType},
		{"Message", model.Message{}, messageStructType},
		{"ToolCall", model.ToolCall{}, toolCallStructType},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, tag := range jsonFieldNames(c.model) {
				if !strings.Contains(c.structType, tag) {
					t.Errorf("model.%s field %q has no matching column in the read_json struct type:\n%s", c.name, tag, c.structType)
				}
			}
		})
	}
}

// jsonFieldNames returns the json tag name of every exported field of v's
// type, stripping ",omitempty" and any other comma-separated options.
func jsonFieldNames(v any) []string {
	rt := reflect.TypeOf(v)
	names := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		names = append(names, name)
	}
	return names
}

// TestScriptEmbedsNoStoreOrOutPath asserts Script is a pure function of
// hasDocs with no path parameter at all (D2): the store root never appears
// in generated SQL, on any branch. Every non-comment line is checked for a
// '/' — the only one that may legitimately appear is inside the relative
// glob literal 'sessions/*/*.json' on the populated branch; the empty
// branch (no read_json call at all) must contain no '/' whatsoever outside
// comments.
func TestScriptEmbedsNoStoreOrOutPath(t *testing.T) {
	if !strings.Contains(Script(true), "'"+storeGlob+"'") {
		t.Fatalf("Script(true) missing the expected relative glob literal %q", storeGlob)
	}

	for _, hasDocs := range []bool{true, false} {
		script := Script(hasDocs)
		for _, line := range strings.Split(script, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "--") {
				continue
			}
			withoutGlob := strings.ReplaceAll(line, storeGlob, "")
			if strings.Contains(withoutGlob, "/") {
				t.Errorf("Script(%v) line contains an unexpected '/' outside the relative glob: %q", hasDocs, line)
			}
		}
	}
}
