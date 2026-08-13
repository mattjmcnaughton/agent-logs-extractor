package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWithinHandlesRootParent pins within's contract for parent == "/":
// building parent+"/" for a root parent used to yield "//", which
// "/home/user/x/" does not have as a prefix, so within silently returned
// false for every child of "/" -- the one parent value guardDisjoint can
// plausibly see as a real -dst.
func TestWithinHandlesRootParent(t *testing.T) {
	if !within("/home/user/x", "/") {
		t.Fatal("within(\"/home/user/x\", \"/\") = false, want true")
	}
	if within("/w/proj-out", "/w/proj") {
		t.Fatal("within(\"/w/proj-out\", \"/w/proj\") = true, want false (sibling paths sharing a prefix)")
	}
	if !within("/w/proj/out", "/w/proj") {
		t.Fatal("within(\"/w/proj/out\", \"/w/proj\") = false, want true")
	}
}

func TestRunRejectsDstAncestorOfSrc(t *testing.T) {
	work := t.TempDir()
	projDir := filepath.Join(work, "projects", "-home-alice-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(projDir, "s1.jsonl")
	content := `{"type":"user","cwd":"/home/alice/proj"}` + "\n" +
		`{"type":"assistant","cwd":"/home/alice/proj"}` + "\n"
	if err := os.WriteFile(srcFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// dst is the parent of src -- the exact fat-finger the guard exists to
	// catch: regenerating "-src ~/.claude/projects/<x> -dst
	// ~/.claude/projects" would otherwise truncate the source file in
	// place.
	dst := filepath.Join(work, "projects")

	var stderr bytes.Buffer
	code := run([]string{"-src", projDir, "-dst", dst}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for -dst ancestor of -src, got 0; stderr:\n%s", stderr.String())
	}

	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("reading src after run: %v", err)
	}
	if string(got) != content {
		t.Fatalf("source file was modified:\nwant: %q\ngot:  %q", content, got)
	}
}

func TestRunRejectsSrcAncestorOfDst(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "home")
	dst := filepath.Join(work, "home", "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := run([]string{"-src", src, "-dst", dst}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for -src ancestor of -dst, got 0; stderr:\n%s", stderr.String())
	}
}

// TestRunReVerifiesOnRerunIntoPopulatedDst reproduces the "formats drift,
// regenerate the fixture" workflow: -dst already holds output from a
// previous run, and a second run must still scan and report every file it
// (re)writes -- not silently skip verification just because the output
// paths already existed. It exploits a real gap the CLI's own verification
// exists to catch: -branch's value is spliced in after redactBuiltins runs
// (see rewriteGitBranch), so a leftover finding there is genuine, not
// contrived, and the previous "diff dst before/after" implementation
// silently reported nothing here.
func TestRunReVerifiesOnRerunIntoPopulatedDst(t *testing.T) {
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", "-home-user-proj")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "s1.jsonl")
	dst := filepath.Join(work, "dst")

	content := `{"type":"user","cwd":"/home/user/proj","gitBranch":"feat/x"}` + "\n"
	if err := os.WriteFile(srcFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr1 bytes.Buffer
	if code := run([]string{"-src", srcDir, "-dst", dst}, &stderr1); code != 0 {
		t.Fatalf("first run: expected exit 0, got %d; stderr:\n%s", code, stderr1.String())
	}
	if !strings.Contains(stderr1.String(), "s1.jsonl: clean") {
		t.Fatalf("first run: expected the written file to be reported clean, stderr:\n%s", stderr1.String())
	}

	// Rerun into the already-populated -dst with a -branch value that
	// leaves a genuine leftover finding.
	var stderr2 bytes.Buffer
	code := run([]string{"-src", srcDir, "-dst", dst, "-branch", "leak@evil.example.org"}, &stderr2)
	if code == 0 {
		t.Fatalf("second run: expected non-zero exit for leftover finding, got 0; stderr:\n%s", stderr2.String())
	}
	if !strings.Contains(stderr2.String(), "s1.jsonl: findings remain") {
		t.Fatalf("second run: expected the rewritten file to be reported even though -dst was already populated, stderr:\n%s", stderr2.String())
	}
	if !strings.Contains(stderr2.String(), "line 1: findings remain") {
		t.Fatalf("second run: expected a line-numbered finding to be reported, stderr:\n%s", stderr2.String())
	}
}

func TestRunCreatesFreshDst(t *testing.T) {
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", "-home-user-proj")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "s1.jsonl")
	if err := os.WriteFile(srcFile, []byte(`{"type":"user","cwd":"/home/user/proj"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(work, "does", "not", "exist", "yet")

	var stderr bytes.Buffer
	code := run([]string{"-src", srcDir, "-dst", dst}, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0 for fresh nonexistent -dst, got %d; stderr:\n%s", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(dst, "-home-user-proj", "s1.jsonl")); err != nil {
		t.Fatalf("expected tree to be created under fresh -dst: %v", err)
	}
}

func TestRunDryRunLeavesRealDstUntouched(t *testing.T) {
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", "-home-user-proj")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "s1.jsonl"), []byte(`{"type":"user","cwd":"/home/user/proj"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(work, "dst")

	var stderr bytes.Buffer
	code := run([]string{"-src", srcDir, "-dst", dst, "-dry-run"}, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0 for clean dry run, got %d; stderr:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("expected -dst to remain nonexistent after -dry-run, stat err = %v", err)
	}
}

// TestRunRejectsEmptySrc reproduces the degenerate-command-substitution
// case from the fixture README's recipes: -src resolves to a directory
// with nothing under it (e.g. `$(ls ...)` came back empty), scrub.Tree
// legitimately writes zero files, and the tool must fail loudly rather
// than exit 0 in silence -- an explicit invocation that scrubs nothing is
// operator error, not success.
func TestRunRejectsEmptySrc(t *testing.T) {
	work := t.TempDir()
	srcDir := filepath.Join(work, "empty-src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(work, "dst")

	var stderr bytes.Buffer
	code := run([]string{"-src", srcDir, "-dst", dst}, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for -src yielding no files, got %d; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), srcDir) {
		t.Fatalf("expected the -src path to be named in the error, stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("expected -dst to remain uncreated when nothing was written, stat err = %v", err)
	}
}

// TestRunReportsCorrectLineNumberForDirtyMultilineFile pins the
// whole-file-first verification strategy: Findings is checked once over
// the whole file, and only a dirty file gets re-split into lines purely to
// attach a line number to the report. Only the middle line carries a
// "gitBranch" key, so -branch's leftover email finding lands on line 2
// specifically -- this pins that the reported line number is the finding's
// real location, not just "line 1" (which the pre-existing single-line
// tests can't distinguish from an off-by-one bug).
func TestRunReportsCorrectLineNumberForDirtyMultilineFile(t *testing.T) {
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", "-home-user-proj")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"user","cwd":"/home/user/proj"}` + "\n" +
		`{"type":"assistant","cwd":"/home/user/proj","gitBranch":"feat/x"}` + "\n" +
		`{"type":"user","cwd":"/home/user/proj"}` + "\n"
	if err := os.WriteFile(filepath.Join(srcDir, "s1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(work, "dst")

	var stderr bytes.Buffer
	code := run([]string{"-src", srcDir, "-dst", dst, "-branch", "leak@evil.example.org"}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for leftover finding, got 0; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "s1.jsonl: findings remain") {
		t.Fatalf("expected the dirty file to be reported, stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "line 2: findings remain") {
		t.Fatalf("expected the finding to be attributed to line 2, stderr:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "line 1: findings remain") || strings.Contains(stderr.String(), "line 3: findings remain") {
		t.Fatalf("expected only line 2 to be reported dirty, stderr:\n%s", stderr.String())
	}
}

func TestRunVerifiesCopiedNonJSONLFiles(t *testing.T) {
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", "-home-user-proj")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "s1.jsonl"), []byte(`{"type":"user","cwd":"/home/user/proj"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sidecar file that isn't line-oriented JSONL but still carries a
	// finding -- Tree copies it verbatim, and the tool must still catch it.
	sidecar := `{"agentType":"general-purpose","description":"email me at leak@evil.example.org"}`
	if err := os.WriteFile(filepath.Join(srcDir, "sidecar.meta.json"), []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(work, "dst")

	var stderr bytes.Buffer
	code := run([]string{"-src", srcDir, "-dst", dst}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for dirty copied sidecar, got 0; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "leak@evil.example.org") {
		t.Fatalf("expected sidecar finding to be reported, stderr:\n%s", stderr.String())
	}
}
