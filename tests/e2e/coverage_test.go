// TestACCoverage carries NO build tag deliberately (D5): it never spawns
// the binary, never touches duckdb, never needs a fixture — it only parses
// docs/acceptance.md and the Go source under tests/e2e/ and the rest of the
// repo, so it can run as part of the untagged `go test ./...` / `just gate`
// and prove the AC-ID <-> test mapping stays honest on every push, not just
// when someone remembers to run the opt-in e2e tier.
//
// It is the one file in this directory without "//go:build e2e" — every
// other file here carries that tag. Getting this backwards either drops
// the whole check from `just gate` (if this file also carried the tag) or
// breaks a plain `go build ./...` (if a tagged file lost its tag, since its
// symbols — alxbin, sandbox, result, run() — are undefined without it).
package e2e

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// acRow is one parsed row of docs/acceptance.md's §2 criteria index table.
type acRow struct {
	id   string
	test string // the row's Test column, verbatim (may hold >1 backtick span)
	tier string
}

// TestACCoverage enforces the D5 rules: the AC-ID set defined by §2's index
// table must exactly equal the set of "**AC-...**" headings in the rest of
// the document (a); every Tier=e2e row must have exactly one
// TestAC_<CAT>_<NN>_* test under tests/e2e/ (b), and every such test must
// map back to a Tier=e2e row (c); every Tier IN {unit, integration} row
// must name a test function that exists somewhere in the repo (d); and the
// Tier=container row must name a justfile recipe that exists (e).
func TestACCoverage(t *testing.T) {
	root := repoRootForCoverage()
	docPath := filepath.Join(root, "docs", "acceptance.md")

	rows, err := parseIndexTable(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows parsed from docs/acceptance.md's §2 index table")
	}

	headings, err := parseACHeadings(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(headings) == 0 {
		t.Fatal("no **AC-...** headings parsed from docs/acceptance.md")
	}

	indexIDs := make(map[string]bool, len(rows))
	for _, r := range rows {
		indexIDs[r.id] = true
	}

	// (a) heading set == index row set.
	for id := range headings {
		if !indexIDs[id] {
			t.Errorf("%s has a **%s — ...** heading but no row in the §2 index table", id, id)
		}
	}
	for id := range indexIDs {
		if !headings[id] {
			t.Errorf("%s has a §2 index row but no **%s — ...** heading", id, id)
		}
	}

	e2eTests, err := e2eTestIDs(filepath.Join(root, "tests", "e2e"))
	if err != nil {
		t.Fatal(err)
	}
	allTests, err := allTestFuncNames(root)
	if err != nil {
		t.Fatal(err)
	}
	justRecipes, err := justfileRecipes(filepath.Join(root, "justfile"))
	if err != nil {
		t.Fatal(err)
	}

	e2eRowsSeen := make(map[string]bool)
	for _, r := range rows {
		switch r.tier {
		case "e2e":
			e2eRowsSeen[r.id] = true
			names := e2eTests[r.id]
			switch len(names) {
			case 0:
				t.Errorf("%s (tier=e2e) has no TestAC_%s test under tests/e2e/", r.id, strings.ReplaceAll(r.id, "AC-", ""))
			case 1:
				// A2: the discovered TestAC_<CAT>_<NN>_* function name must
				// also be the one the row's own Test column names — without
				// this, renaming the Test cell to a nonexistent function (or
				// repointing it at some other test entirely) still passes,
				// since nothing here ever reads r.test for an e2e row.
				if !slices.Contains(testNamesIn(r.test), names[0]) {
					t.Errorf("%s (tier=e2e): Test column %q does not name %s, the TestAC_* function actually found for this AC ID", r.id, r.test, names[0])
				}
			default:
				t.Errorf("%s (tier=e2e) has %d TestAC_* tests, want exactly 1: %v", r.id, len(names), names)
			}
		case "unit", "integration":
			names := testNamesIn(r.test)
			if len(names) == 0 {
				t.Errorf("%s (tier=%s): could not find a `TestXxx` name in Test column %q", r.id, r.tier, r.test)
				continue
			}
			for _, n := range names {
				if !allTests[n] {
					t.Errorf("%s (tier=%s) names test %s, which does not exist anywhere in the repo", r.id, r.tier, n)
				}
			}
		case "container":
			recipe := recipeNameIn(r.test)
			if recipe == "" {
				t.Errorf("%s (tier=container): could not find a `just <recipe>` invocation in Test column %q", r.id, r.test)
				continue
			}
			if !justRecipes[recipe] {
				t.Errorf("%s (tier=container) names recipe %q, which is not defined in the justfile", r.id, recipe)
			}
		default:
			t.Errorf("%s: unrecognized Tier %q (want e2e, unit, integration, or container)", r.id, r.tier)
		}
	}

	// (c) every TestAC_* found in tests/e2e/ maps back to a Tier=e2e row.
	for id, names := range e2eTests {
		if !e2eRowsSeen[id] {
			t.Errorf("test(s) %v map to %s, which has no Tier=e2e row in docs/acceptance.md's §2 index", names, id)
		}
	}
}

// --- §2 index table parsing ------------------------------------------------

// indexRowRe matches one data row of the §2 table:
// | AC-CAT-NN | story | statement | test | tier |
var indexRowRe = regexp.MustCompile(`^\|\s*(AC-[A-Z]+-[0-9]+)\s*(?:†)?\s*\|(.*)\|(.*)\|(.*)\|\s*([a-z0-9]+)\s*\|\s*$`)

// parseIndexTable extracts every data row from the §2 "Criteria index"
// table: the table is bounded by "## 2." and the next "## " heading, so
// this never mistakes a later worked example's markdown table (§3 onward)
// for the index itself.
func parseIndexTable(path string) ([]acRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []acRow
	inSection := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## 2.") {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if !inSection {
			continue
		}
		m := indexRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows = append(rows, acRow{
			id:   m[1],
			test: strings.TrimSpace(m[4]),
			tier: strings.TrimSpace(m[5]),
		})
	}
	return rows, sc.Err()
}

// acHeadingRe matches an AC definition heading: "**AC-REPO-03 — ...**".
var acHeadingRe = regexp.MustCompile(`^\*\*(AC-[A-Z]+-[0-9]+)\b`)

// parseACHeadings parses the set of AC IDs defined as "**AC-...**" headings
// anywhere in the document (§3 onward).
func parseACHeadings(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ids := make(map[string]bool)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if m := acHeadingRe.FindStringSubmatch(sc.Text()); m != nil {
			ids[m[1]] = true
		}
	}
	return ids, sc.Err()
}

// testNameRe matches a backtick-quoted Go test identifier, e.g.
// `TestCookbookQueries`.
var testNameRe = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

// testNamesIn extracts every backtick-quoted TestXxx identifier from a Test
// column's cell text.
func testNamesIn(cell string) []string {
	matches := testNameRe.FindAllStringSubmatch(cell, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// recipeNameRe matches a backtick-quoted "just <recipe>" invocation.
var recipeNameRe = regexp.MustCompile("`just ([a-z0-9-]+)`")

func recipeNameIn(cell string) string {
	m := recipeNameRe.FindStringSubmatch(cell)
	if m == nil {
		return ""
	}
	return m[1]
}

// --- e2e test discovery ----------------------------------------------------

// acTestRe matches a conforming e2e test name: TestAC_CLI_01_ShortName.
var acTestRe = regexp.MustCompile(`^TestAC_([A-Z]+)_([0-9]+)_`)

// e2eTestIDs maps AC ID -> the test function names under dir (tests/e2e/)
// that encode it in their name. Parses every *_test.go file's syntax
// directly, ignoring build tags entirely (go/parser does not evaluate
// them) — deliberate, since the e2e-tagged files are exactly what this
// wants to discover from an untagged test run.
func e2eTestIDs(dir string) (map[string][]string, error) {
	fset := token.NewFileSet()
	byID := make(map[string][]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if m := acTestRe.FindStringSubmatch(fn.Name.Name); m != nil {
				id := fmt.Sprintf("AC-%s-%s", m[1], m[2])
				byID[id] = append(byID[id], fn.Name.Name)
			}
		}
	}
	return byID, nil
}

// allTestFuncNames walks the whole repo and returns the set of every
// top-level TestXxx function name declared in any *_test.go file,
// regardless of build tag (same rationale as e2eTestIDs) or directory —
// this is what backs rule (d): a unit/integration-tier AC's named test may
// live anywhere in the tree.
//
// T4: dot-directories (.git, a worktree's .worktrees, a sandcastle's
// .sandcastle/worktrees, ...) are skipped outright, and a _test.go file
// that fails to parse is skipped rather than aborting the whole walk — a
// stray or in-progress file under a worktree or scratch dir should never
// break `just gate` for a reason that has nothing to do with the AC-ID
// <-> test mapping this function backs. node_modules (the release
// pipeline's semantic-release install, gitignored) is skipped for the same
// reason: nothing vendored under it is ours, and a transitive dependency
// shipping a *_test.go would otherwise be parsed — and could fatal — here.
func allTestFuncNames(root string) (map[string]bool, error) {
	fset := token.NewFileSet()
	names := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "bin" || d.Name() == "node_modules" || (d.Name() != "." && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") {
				names[fn.Name.Name] = true
			}
		}
		return nil
	})
	return names, err
}

// --- justfile recipe discovery ---------------------------------------------

// recipeLineRe matches a justfile recipe header: "name:" or "name arg:" at
// column 0, not a comment or an indented recipe body line.
var recipeLineRe = regexp.MustCompile(`^([a-zA-Z0-9_-]+)\b.*:`)

func justfileRecipes(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	recipes := make(map[string]bool)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if m := recipeLineRe.FindStringSubmatch(line); m != nil {
			recipes[m[1]] = true
		}
	}
	return recipes, sc.Err()
}

// repoRootForCoverage walks up from the test's working directory to the
// directory containing go.mod. Named distinctly from the e2e-tagged
// main_test.go's repoRoot() (same idea, same body) because this file
// carries no build tag: with -tags=e2e both files compile into the same
// package, and two functions named identically would collide.
func repoRootForCoverage() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("go.mod not found above " + dir)
		}
		dir = parent
	}
}
