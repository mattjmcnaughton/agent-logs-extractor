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
// justfile starts with a letter.
var justRecipeRefRe = regexp.MustCompile("`just ([a-zA-Z][a-zA-Z0-9_-]*)")

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
// cell, skipping any cell with none (e.g. a "—" placeholder cell).
func namesFromCells(cells []string) []string {
	var names []string
	for _, c := range cells {
		if m := backtickNameRe.FindStringSubmatch(c); m != nil {
			names = append(names, m[1])
		}
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

// TestPortNamesInArchitectureTableExist asserts that every backtick-quoted
// name in docs/architecture.md's "## Ports" table's first column is a real
// interface declared in internal/ports/. This is the mechanical form of the
// plan's claim C: StoreRebuild is a fourth exported interface older docs
// never mentioned, and this test is what stops a Ports table from silently
// going stale (or being mistyped) again.
func TestPortNamesInArchitectureTableExist(t *testing.T) {
	root := repoRoot(t)
	content := readFile(t, filepath.Join(root, "docs", "architecture.md"))

	cells := parseMarkdownTable(content, "## Ports")
	if len(cells) == 0 {
		t.Fatal("parsed zero rows from docs/architecture.md's \"## Ports\" table; parseMarkdownTable or the doc's table shape is almost certainly broken")
	}
	names := namesFromCells(cells)
	if len(names) == 0 {
		t.Fatal("parsed zero backtick-quoted port names from docs/architecture.md's \"## Ports\" table")
	}

	real := realPortNames(t, root)
	for _, n := range names {
		if !real[n] {
			t.Errorf("docs/architecture.md's Ports table names %q, which is not an interface declared in internal/ports/", n)
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

// TestAdapterPackageNamesInArchitectureTableExist asserts that every
// backtick-quoted name in docs/architecture.md's "## Adapters" table's
// first column is a real package directory under internal/adapters/.
func TestAdapterPackageNamesInArchitectureTableExist(t *testing.T) {
	root := repoRoot(t)
	content := readFile(t, filepath.Join(root, "docs", "architecture.md"))

	cells := parseMarkdownTable(content, "## Adapters")
	if len(cells) == 0 {
		t.Fatal("parsed zero rows from docs/architecture.md's \"## Adapters\" table; parseMarkdownTable or the doc's table shape is almost certainly broken")
	}
	names := namesFromCells(cells)
	if len(names) == 0 {
		t.Fatal("parsed zero backtick-quoted adapter package names from docs/architecture.md's \"## Adapters\" table")
	}

	real := realAdapterPackages(t, root)
	for _, n := range names {
		if !real[n] {
			t.Errorf("docs/architecture.md's Adapters table names %q, which is not a package directory under internal/adapters/", n)
		}
	}
}
