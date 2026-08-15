// Package docs is a mechanical drift check between the prose in the repo's
// top-level docs and the tree those docs describe. It carries NO build tag
// deliberately (mirroring tests/e2e/coverage_test.go's D5 and
// internal/core/arch_test.go's TestCorePurity): it does no I/O beyond
// reading local files, needs no subprocess, no binary, and no duckdb, so it
// runs as part of the untagged `go test ./...` / `just gate` and catches
// doc drift on every push, not just when someone remembers to check by
// hand.
//
// It covers exactly three claim families — the ones the ticket's plan
// measured as reliably checkable — deliberately NOT a generic
// backticked-path checker: a general "every backtick-quoted path must
// resolve" check was tried and measured a 100% false-positive rate (it
// flags package-qualified symbols like internal/testing/invariants.Check
// and placeholders like <name>.go), so it is not reproduced here.
package docs

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the directory
// containing go.mod. Duplicated from tests/e2e/coverage_test.go's
// repoRootForCoverage (same idea, same body) rather than shared, per the
// ticket's decision to accept ~20 lines of duplication here instead of
// widening tests/e2e/coverage_test.go's charter to cover doc/tree drift
// beyond the AC-ID mapping it already owns.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// recipeLineRe matches a justfile recipe header: "name:" or "name arg:" at
// column 0, not a comment or an indented recipe body line. Same pattern
// tests/e2e/coverage_test.go's justfileRecipes uses.
var recipeLineRe = regexp.MustCompile(`^([a-zA-Z0-9_-]+)\b.*:`)

// justfileRecipes returns the set of recipe names defined in the justfile
// at path.
func justfileRecipes(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
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
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning %s: %v", path, err)
	}
	return recipes
}

// docPaths is the set of top-level docs a fresh contributor is expected to
// navigate the codebase from — the ticket's "Done when" criterion. Every
// subtest below reads some subset of these.
func docPaths(t *testing.T, root string) []string {
	t.Helper()
	return []string{
		filepath.Join(root, "CLAUDE.md"),
		filepath.Join(root, "README.md"),
		filepath.Join(root, "docs", "architecture.md"),
		filepath.Join(root, "docs", "testing.md"),
		filepath.Join(root, "docs", "development.md"),
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// justRecipeRefRe matches a backtick-quoted "just <recipe>" invocation
// anywhere in prose, e.g. "`just test-contract`" or "`just gate`". The
// recipe name must start with a letter, so a version reference like
// "`just 1.21.0`" (the just *tool's* pinned version, not a recipe
// invocation) is never mistaken for one — every real recipe name in the
// justfile starts with a letter. The gap between "just" and the recipe
// name is \s+, not a literal space: the docs hard-wrap at ~72 columns, so
// a backtick-quoted "just <recipe>" span sometimes wraps across a newline
// inside its own backticks, and a literal-space regex would silently
// never see those (the recipe name it needs is on the next source line).
var justRecipeRefRe = regexp.MustCompile("`just\\s+([a-zA-Z][a-zA-Z0-9_-]*)")

// TestJustRecipesReferencedInDocsExist asserts that every `just <recipe>`
// invocation named in the docs is a recipe the justfile actually defines.
// This is the mechanical form of the plan's claim H: README.md's `just
// install` was a live failure sitting in main before this ticket; this
// test is what stops that class of drift from recurring silently.
func TestJustRecipesReferencedInDocsExist(t *testing.T) {
	root := repoRoot(t)
	recipes := justfileRecipes(t, filepath.Join(root, "justfile"))

	seen := map[string]bool{}
	for _, path := range docPaths(t, root) {
		content := readFile(t, path)
		for _, m := range justRecipeRefRe.FindAllStringSubmatch(content, -1) {
			seen[m[1]] = true
		}
	}

	// Anti-vacuity guard: a check that silently passes because it found
	// nothing to check is as dangerous as a bug in the check itself
	// (mirrors tests/e2e/coverage_test.go:51,58 and
	// internal/core/arch_test.go:70).
	if len(seen) == 0 {
		t.Fatal("found zero `just <recipe>` references across the docs; the scan (docPaths/justRecipeRefRe) is almost certainly broken")
	}

	for recipe := range seen {
		if !recipes[recipe] {
			t.Errorf("docs reference `just %s`, which is not a recipe defined in justfile", recipe)
		}
	}
}

// parseMarkdownTable extracts the first-column cell of every data row (not
// the header, not the "---" separator) from the first markdown table found
// under headingPrefix (e.g. "## Ports") in content, stopping at the next
// "## " heading. A table row is any line starting with "|"; the header row
// and the separator row (whose cells are all "-"/":" runs) are skipped.
func parseMarkdownTable(content, headingPrefix string) []string {
	lines := strings.Split(content, "\n")
	inSection := false
	inTable := false
	var firstCells []string
	headerSeen := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, headingPrefix) {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			break // next section; the table (if any) is finished
		}
		if !strings.HasPrefix(trimmed, "|") {
			if inTable {
				break // table ended before hitting the next heading
			}
			continue
		}
		inTable = true
		cells := strings.Split(trimmed, "|")
		if len(cells) < 2 {
			continue
		}
		first := strings.TrimSpace(cells[1])
		if !headerSeen {
			headerSeen = true
			continue // this is the header row itself
		}
		if isSeparatorCell(first) {
			continue // the "|---|---|" row
		}
		firstCells = append(firstCells, first)
	}
	return firstCells
}

// isSeparatorCell reports whether s is a markdown table separator cell
// (only made of '-' and ':').
func isSeparatorCell(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '-' && r != ':' {
			return false
		}
	}
	return true
}

// backtickNameRe matches the first backtick-quoted identifier in a cell.
var backtickNameRe = regexp.MustCompile("`([A-Za-z0-9_]+)`")

// namesFromCells extracts the first backtick-quoted identifier from each
// cell. A cell with no backtick-quoted identifier is a table-authoring
// error for the two tables this backs (every row names one Go symbol) —
// silently skipping it would let a whole row go unchecked (e.g. a row
// rewritten as "| ExporterTYPO |" with no backticks), so it is reported as
// a test failure via t.Errorf rather than dropped.
func namesFromCells(t *testing.T, tableDesc string, cells []string) []string {
	t.Helper()
	names := make([]string, 0, len(cells))
	for _, c := range cells {
		m := backtickNameRe.FindStringSubmatch(c)
		if m == nil {
			t.Errorf("%s: a data row's first cell %q has no backtick-quoted identifier", tableDesc, c)
			continue
		}
		names = append(names, m[1])
	}
	return names
}

// portInterfaceRe matches a top-level interface declaration.
var portInterfaceRe = regexp.MustCompile(`^type ([A-Za-z0-9_]+) interface`)

// realPortNames returns the set of interface names declared directly under
// internal/ports/ (not _test.go files), by scanning source text rather
// than using go/ast — this package intentionally stays dependency-light,
// matching the other checks in this file.
func realPortNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "internal", "ports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		content := readFile(t, filepath.Join(dir, e.Name()))
		for _, line := range strings.Split(content, "\n") {
			if m := portInterfaceRe.FindStringSubmatch(line); m != nil {
				names[m[1]] = true
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("found zero interface declarations under %s; the scan is almost certainly broken", dir)
	}
	return names
}

// TestPortNamesInArchitectureTableExist asserts that docs/architecture.md's
// "## Ports" table's first column and the set of interfaces declared under
// internal/ports/ name exactly the same set — bidirectionally. This is the
// mechanical form of the plan's claim C: StoreRebuild is a fourth exported
// interface older docs never mentioned, and this test is what stops a
// Ports table from silently going stale (a mistyped/dropped table entry,
// checked table->tree) or from silently missing a new port (checked
// tree->table, the direction that would otherwise let #10's codexsource
// land with a Ports table nobody updated).
func TestPortNamesInArchitectureTableExist(t *testing.T) {
	root := repoRoot(t)
	content := readFile(t, filepath.Join(root, "docs", "architecture.md"))
	const tableDesc = `docs/architecture.md's "## Ports" table`

	cells := parseMarkdownTable(content, "## Ports")
	if len(cells) == 0 {
		t.Fatalf("parsed zero rows from %s; parseMarkdownTable or the doc's table shape is almost certainly broken", tableDesc)
	}
	names := namesFromCells(t, tableDesc, cells)
	if len(names) == 0 {
		t.Fatalf("parsed zero backtick-quoted port names from %s", tableDesc)
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}

	real := realPortNames(t, root)
	for _, n := range names {
		if !real[n] {
			t.Errorf("%s names %q, which is not an interface declared in internal/ports/", tableDesc, n)
		}
	}
	for n := range real {
		if !nameSet[n] {
			t.Errorf("internal/ports/ declares interface %q, which %s does not list", n, tableDesc)
		}
	}
}

// realAdapterPackages returns the set of directory names directly under
// internal/adapters/.
func realAdapterPackages(t *testing.T, root string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "internal", "adapters")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() {
			names[e.Name()] = true
		}
	}
	if len(names) == 0 {
		t.Fatalf("found zero directories under %s; the scan is almost certainly broken", dir)
	}
	return names
}

// TestAdapterPackageNamesInArchitectureTableExist asserts that
// docs/architecture.md's "## Adapters" table's first column and the set of
// package directories under internal/adapters/ name exactly the same set —
// bidirectionally, for the same reason TestPortNamesInArchitectureTableExist
// checks the Ports table both ways: deleting the whole duckdbcli row would
// pass a table->tree-only check, and a future internal/adapters/codexsource/
// (#10) would go undocumented forever under a tree->table-only check.
func TestAdapterPackageNamesInArchitectureTableExist(t *testing.T) {
	root := repoRoot(t)
	content := readFile(t, filepath.Join(root, "docs", "architecture.md"))
	const tableDesc = `docs/architecture.md's "## Adapters" table`

	cells := parseMarkdownTable(content, "## Adapters")
	if len(cells) == 0 {
		t.Fatalf("parsed zero rows from %s; parseMarkdownTable or the doc's table shape is almost certainly broken", tableDesc)
	}
	names := namesFromCells(t, tableDesc, cells)
	if len(names) == 0 {
		t.Fatalf("parsed zero backtick-quoted adapter package names from %s", tableDesc)
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}

	real := realAdapterPackages(t, root)
	for _, n := range names {
		if !real[n] {
			t.Errorf("%s names %q, which is not a package directory under internal/adapters/", tableDesc, n)
		}
	}
	for n := range real {
		if !nameSet[n] {
			t.Errorf("internal/adapters/%s/ exists, but %s does not list it", n, tableDesc)
		}
	}
}

// --- cited test-name existence (N3 / A15) -----------------------------------

// docTestNameRe matches a backtick-quoted TestXxx identifier anywhere in
// prose, e.g. "`TestScriptPopulatedMatchesGolden`".
var docTestNameRe = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

// testNamePlaceholder is the one backtick-quoted "Test..." span that is
// documented shorthand, not a citation of a real function — both
// docs/testing.md and docs/development.md use `TestXxx` to mean "a test
// function", generically, when describing rule (d) of the AC-ID mapping.
const testNamePlaceholder = "TestXxx"

// testNameDocPaths is docPaths' set plus the two other docs known to cite
// real Test... function names by their bare identifier:
// docs/technical/tdd-mvp.md (where A5's dead citation to
// TestBothScriptFormsDeclareTheSameSchema lived until b37940d fixed it by
// hand) and docs/acceptance.md (whose §2 index table cites TestAC_* and
// TestCookbookQueries*/TestMain names directly in prose, not only inside
// table cells rule (d)/(e) already check).
func testNameDocPaths(t *testing.T, root string) []string {
	t.Helper()
	paths := docPaths(t, root)
	paths = append(paths,
		filepath.Join(root, "docs", "technical", "tdd-mvp.md"),
		filepath.Join(root, "docs", "acceptance.md"),
	)
	return paths
}

// allTestFuncNames walks the whole repo and returns the set of every
// top-level TestXxx function name declared in any *_test.go file,
// regardless of build tag or directory. Duplicated from
// tests/e2e/coverage_test.go's function of the same name (same idea, same
// body) per this package's existing duplication convention — see
// repoRoot's doc comment above — rather than shared.
func allTestFuncNames(t *testing.T, root string) map[string]bool {
	t.Helper()
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
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return names
}

// TestDocCitedTestNamesExist asserts that every backtick-quoted `TestXxx`
// identifier cited in the docs resolves to a real "func TestXxx" declared
// somewhere in the repo — the `TestXxx` placeholder itself excepted. This
// is the check that would have caught A5: a citation naming a test that
// proves less than the prose claims is a different problem this check
// cannot see, but a citation naming a test that flat-out does not exist
// (this ticket had to fix exactly one of those by hand, in
// docs/technical/tdd-mvp.md, before this test existed) is exactly what
// this catches.
func TestDocCitedTestNamesExist(t *testing.T) {
	root := repoRoot(t)

	real := allTestFuncNames(t, root)
	if len(real) == 0 {
		t.Fatal("found zero TestXxx function declarations anywhere in the repo; allTestFuncNames is almost certainly broken")
	}

	seen := map[string]bool{}
	for _, path := range testNameDocPaths(t, root) {
		content := readFile(t, path)
		for _, m := range docTestNameRe.FindAllStringSubmatch(content, -1) {
			name := m[1]
			if name == testNamePlaceholder {
				continue
			}
			seen[name] = true
		}
	}

	// Anti-vacuity guard: a check that silently passes because it found
	// nothing to check is as dangerous as a bug in the check itself
	// (mirrors TestJustRecipesReferencedInDocsExist above,
	// tests/e2e/coverage_test.go:51,58, and internal/core/arch_test.go:70).
	if len(seen) == 0 {
		t.Fatal("found zero cited `TestXxx` names across the docs; the scan (testNameDocPaths/docTestNameRe) is almost certainly broken")
	}

	for name := range seen {
		if !real[name] {
			t.Errorf("docs cite `%s`, which is not a func %s declared anywhere in the repo", name, name)
		}
	}
}
