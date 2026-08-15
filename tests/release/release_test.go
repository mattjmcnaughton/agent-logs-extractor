// Package release is a mechanical drift check between
// .github/workflows/release.yml (plus the semantic-release config it runs)
// and the tree that workflow builds. It carries NO build tag deliberately,
// for the same reason tests/docs/docs_test.go and
// tests/e2e/coverage_test.go carry none: it does no I/O beyond reading
// local files, needs no subprocess, no binary, no network, and no duckdb,
// so it runs as part of the untagged `go test ./...` / `just gate` — which
// is CI's "Gate" job — and catches release-pipeline drift on every push.
//
// Why this package exists at all: release.yml cannot be proven by running
// it. Its trigger is `workflow_run`, which by design never fires from a
// pull request, so the file's first real execution is always the first
// push to main after it merges. Everything checkable without executing it
// is therefore checked here, by reading release.yml as data.
//
// The single highest-value check is
// TestReleaseWorkflowLdflagsPathMatchesTree. The `-X <path>=<version>`
// argument is only a string to the Go linker: it does not have to name a
// symbol that exists. Get it wrong — a module rename, a moved
// internal/version package, a typo — and the release still goes green
// while every shipped binary reports "dev". Nothing else in the repo
// notices, because `just build` and tests/e2e/ both build without any
// -ldflags at all.
//
// Stdlib only, on purpose. go.mod stays at afero + cobra (the wiring's two
// direct dependencies); adding a YAML library so a test could parse a
// workflow file would be a real dependency bought for a test's
// convenience. release.yml is parsed here by regexp and line-scanning
// instead, and `just gate`'s green is backed up by a real `yq` parse
// during development.
//
// Shared helpers (repoRoot, releaseLdflags, releaseMatrixTargets, ...)
// live in THIS untagged file so that ldflags_integration_test.go, which
// carries //go:build integration, can use them — the same arrangement
// tests/e2e/ uses, where the untagged coverage_test.go and the e2e-tagged
// files compile into one package.
package release

import (
	"bufio"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the directory
// containing go.mod. Duplicated from tests/docs/docs_test.go's repoRoot
// and tests/e2e/coverage_test.go's repoRootForCoverage (same idea, same
// body) rather than shared, per the convention those two files already
// established — a shared test-helper package would have to live under
// internal/testing/, which is for fakes and fixtures, not for locating the
// repo root from three leaf test packages.
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// releaseWorkflowPath / ciWorkflowPath name the two workflow files every
// check below reads. They are relative to the repo root.
const (
	releaseWorkflowPath = ".github/workflows/release.yml"
	ciWorkflowPath      = ".github/workflows/ci.yml"
)

func releaseWorkflow(t *testing.T, root string) string {
	t.Helper()
	return readFile(t, filepath.Join(root, filepath.FromSlash(releaseWorkflowPath)))
}

func ciWorkflow(t *testing.T, root string) string {
	t.Helper()
	return readFile(t, filepath.Join(root, filepath.FromSlash(ciWorkflowPath)))
}

// releaseVersionExpr is the GitHub Actions expression release.yml
// interpolates the semantic-release version into. The integration tier's
// TestReleaseLdflagsInjectVersion substitutes a sentinel for exactly this
// span, so it can build with the real ldflags string rather than a
// hand-copied approximation of it.
const releaseVersionExpr = "${{ needs.release.outputs.new-release-version }}"

// ldflagsRe captures the whole double-quoted argument of `-ldflags` in
// release.yml's build step.
var ldflagsRe = regexp.MustCompile(`-ldflags "([^"]*)"`)

// releaseLdflags returns release.yml's -ldflags argument verbatim, e.g.
//
//	-s -w -X github.com/.../internal/version.Version=${{ ... }}
//
// It t.Fatals unless the file contains exactly one such argument: two
// would mean the build step was duplicated (and this test would be
// checking an arbitrary one of them), zero would mean the regexp or the
// workflow's shape drifted and every check built on it had gone vacuous.
func releaseLdflags(t *testing.T, root string) string {
	t.Helper()
	matches := ldflagsRe.FindAllStringSubmatch(releaseWorkflow(t, root), -1)
	if len(matches) != 1 {
		t.Fatalf("found %d `-ldflags \"...\"` arguments in %s, want exactly 1; the workflow's build step or ldflagsRe has drifted", len(matches), releaseWorkflowPath)
	}
	return matches[0][1]
}

// ldflagsXRe captures the symbol path of the `-X <path>=<version>` pair,
// pinning the value side to the exact Actions expression as well: an
// ldflags string that injected some other expression (a literal, a stale
// output name from a renamed job) would fail to match and be reported as a
// drift rather than silently accepted.
var ldflagsXRe = regexp.MustCompile(`-X ([^\s=]+)=\$\{\{ needs\.release\.outputs\.new-release-version \}\}`)

// releaseLdflagsSymbolPath returns the fully-qualified symbol path the
// release build injects into, e.g.
// "github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version".
func releaseLdflagsSymbolPath(t *testing.T, ldflags string) string {
	t.Helper()
	matches := ldflagsXRe.FindAllStringSubmatch(ldflags, -1)
	if len(matches) != 1 {
		t.Fatalf("found %d `-X <path>=%s` pairs in %s's ldflags %q, want exactly 1", len(matches), releaseVersionExpr, releaseWorkflowPath, ldflags)
	}
	return matches[0][1]
}

// moduleRe matches go.mod's module line.
var moduleRe = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)

func modulePath(t *testing.T, root string) string {
	t.Helper()
	m := moduleRe.FindStringSubmatch(readFile(t, filepath.Join(root, "go.mod")))
	if m == nil {
		t.Fatal("no `module <path>` line found in go.mod")
	}
	return m[1]
}

// TestReleaseWorkflowLdflagsPathMatchesTree is the reason this package
// exists. It proves the release workflow's -X symbol path names a symbol
// that actually exists in this tree, from two independent directions:
//
//  1. against go.mod — the path must be exactly
//     "<module>/internal/version.Version", so renaming the module without
//     touching release.yml is caught;
//  2. against the source — the package directory the path points at must
//     really declare `package version` and a top-level `var Version`, so
//     moving or renaming the version package (or its variable) without
//     touching release.yml is caught too.
//
// Check 2 deliberately derives the directory it reads FROM the workflow's
// own symbol path rather than hardcoding internal/version: hardcoding
// would only prove that internal/version exists, which is not in doubt —
// what is in doubt is whether release.yml still points at it.
func TestReleaseWorkflowLdflagsPathMatchesTree(t *testing.T) {
	root := repoRoot(t)
	symbolPath := releaseLdflagsSymbolPath(t, releaseLdflags(t, root))

	// (1) against go.mod.
	module := modulePath(t, root)
	want := module + "/internal/version.Version"
	if symbolPath != want {
		t.Errorf("%s injects into %q, but go.mod's module is %q, so the only symbol that can work is %q.\n"+
			"A -X path is just a string to the linker: this mismatch does NOT fail the release, it silently ships binaries reporting \"dev\".",
			releaseWorkflowPath, symbolPath, module, want)
	}

	// (2) against the source, via the workflow's own path.
	dot := strings.LastIndex(symbolPath, ".")
	if dot < 0 {
		t.Fatalf("%s's -X path %q has no `.<Symbol>` suffix", releaseWorkflowPath, symbolPath)
	}
	pkgPath, symbolName := symbolPath[:dot], symbolPath[dot+1:]
	rel := strings.TrimPrefix(pkgPath, module+"/")
	if rel == pkgPath {
		t.Fatalf("%s's -X package path %q is not inside module %q, so no directory in this tree can satisfy it", releaseWorkflowPath, pkgPath, module)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))

	wantClause := path.Base(pkgPath)
	pkgClause, hasVar := scanPackageForVar(t, dir, symbolName)
	if pkgClause != wantClause {
		t.Errorf("%s's -X path implies package clause %q at %s, but that directory declares `package %s`", releaseWorkflowPath, wantClause, rel, pkgClause)
	}
	if !hasVar {
		t.Errorf("%s injects into %s, but %s declares no top-level `var %s` — the linker would silently do nothing", releaseWorkflowPath, symbolPath, rel, symbolName)
	}
}

// scanPackageForVar parses every non-test .go file in dir and reports the
// package clause they declare plus whether any of them declares a
// top-level `var <name>`. It t.Fatals if dir holds no non-test Go files at
// all — an empty scan reporting "no var Version" would be a true result
// for a false reason.
func scanPackageForVar(t *testing.T, dir, name string) (pkgClause string, hasVar bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", filepath.Join(dir, e.Name()), err)
		}
		scanned++
		pkgClause = f.Name.Name
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range vs.Names {
					if ident.Name == name {
						hasVar = true
					}
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("found zero non-test .go files under %s; the scan is almost certainly broken", dir)
	}
	return pkgClause, hasVar
}

// buildTargetRe matches the trailing `./cmd/<name>` line of release.yml's
// folded `run: >` build command.
var buildTargetRe = regexp.MustCompile(`(?m)^\s*(\./cmd/[A-Za-z0-9_.-]+)\s*$`)

// releaseBuildTarget returns the package release.yml builds, e.g.
// "./cmd/agent-logs-extractor".
func releaseBuildTarget(t *testing.T, root string) string {
	t.Helper()
	matches := buildTargetRe.FindAllStringSubmatch(releaseWorkflow(t, root), -1)
	if len(matches) != 1 {
		t.Fatalf("found %d `./cmd/<name>` build targets in %s, want exactly 1", len(matches), releaseWorkflowPath)
	}
	return matches[0][1]
}

// outFlagRe matches the `-o <asset>` argument of the same build command.
var outFlagRe = regexp.MustCompile(`(?m)^\s*-o\s+(\S.*?)\s*$`)

// TestReleaseWorkflowBuildTargetExists asserts the package release.yml
// hands `go build` is a real main package in this tree, and that the
// binary it writes is named per-matrix-leg rather than to one fixed name
// (four legs writing the same filename would upload four assets that
// clobber each other on the release).
func TestReleaseWorkflowBuildTargetExists(t *testing.T) {
	root := repoRoot(t)
	content := releaseWorkflow(t, root)

	target := releaseBuildTarget(t, root)
	dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(target, "./")))
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("%s builds %s, but %s does not exist: %v", releaseWorkflowPath, target, dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s builds %s, but %s is not a directory", releaseWorkflowPath, target, dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	foundMain := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src := readFile(t, filepath.Join(dir, e.Name()))
		for _, line := range strings.Split(src, "\n") {
			if strings.TrimSpace(line) == "package main" {
				foundMain = true
			}
		}
	}
	if !foundMain {
		t.Errorf("%s builds %s, but no .go file under %s declares `package main`", releaseWorkflowPath, target, dir)
	}

	outs := outFlagRe.FindAllStringSubmatch(content, -1)
	if len(outs) != 1 {
		t.Fatalf("found %d `-o <asset>` arguments in %s, want exactly 1", len(outs), releaseWorkflowPath)
	}
	const wantOut = "${{ matrix.asset-name }}"
	if outs[0][1] != wantOut {
		t.Errorf("%s writes its binary to %q; want %q so each matrix leg produces a distinctly-named asset instead of four uploads clobbering one filename", releaseWorkflowPath, outs[0][1], wantOut)
	}
}

// workflowRunNameRe matches release.yml's `workflows: ["CI"]` trigger.
var workflowRunNameRe = regexp.MustCompile(`(?m)^\s*workflows:\s*\[\s*"([^"]+)"\s*\]`)

// topLevelNameRe matches a workflow file's top-level `name:` — anchored at
// column 0, so a job's or a step's indented `name:` is never mistaken for
// it.
var topLevelNameRe = regexp.MustCompile(`(?m)^name:\s*(\S.*?)\s*$`)

// TestReleaseWorkflowBindsToTheCIWorkflowName asserts release.yml's
// `workflow_run` trigger names the workflow ci.yml actually declares.
//
// This is the silent-failure check. A `workflow_run` trigger naming a
// workflow that does not exist is not an error to GitHub: the Release
// workflow simply never fires, forever, with no red build anywhere to say
// so. Renaming CI (or adding a second workflow and renaming this one) is
// exactly the kind of unrelated-looking change that would do it.
func TestReleaseWorkflowBindsToTheCIWorkflowName(t *testing.T) {
	root := repoRoot(t)

	trigger := workflowRunNameRe.FindStringSubmatch(releaseWorkflow(t, root))
	if trigger == nil {
		t.Fatalf("no `workflows: [\"<name>\"]` trigger found in %s", releaseWorkflowPath)
	}
	ciName := topLevelNameRe.FindStringSubmatch(ciWorkflow(t, root))
	if ciName == nil {
		t.Fatalf("no top-level `name:` found in %s", ciWorkflowPath)
	}

	if trigger[1] != ciName[1] {
		t.Errorf("%s triggers on workflow_run of %q, but %s is named %q.\n"+
			"GitHub does not error on a workflow_run trigger naming a workflow that does not exist — the Release workflow would simply never fire, silently.",
			releaseWorkflowPath, trigger[1], ciWorkflowPath, ciName[1])
	}
}

// goVersionRe matches a setup-go `go-version: "X"` pin.
var goVersionRe = regexp.MustCompile(`(?m)^\s*go-version:\s*"([^"]+)"`)

// goVersionPins returns every go-version pin in content, t.Fataling if
// there are none (an empty scan would make the comparison below vacuously
// true).
func goVersionPins(t *testing.T, desc, content string) []string {
	t.Helper()
	matches := goVersionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		t.Fatalf("found zero `go-version: \"...\"` pins in %s; goVersionRe or the workflow's shape has drifted", desc)
	}
	pins := make([]string, 0, len(matches))
	for _, m := range matches {
		pins = append(pins, m[1])
	}
	return pins
}

// TestReleaseWorkflowGoVersionMatchesCI asserts the toolchain that builds
// the released binaries is the same one the gate ran against. Deliberately
// stricter than the sibling repos, which pin CI to an exact patch and the
// release build to "1.25.x": a release built by a toolchain no gate ever
// exercised is a difference nobody would notice until it mattered.
func TestReleaseWorkflowGoVersionMatchesCI(t *testing.T) {
	root := repoRoot(t)

	ciPins := goVersionPins(t, ciWorkflowPath, ciWorkflow(t, root))
	for _, p := range ciPins {
		if p != ciPins[0] {
			t.Fatalf("%s pins more than one Go version (%v); resolve that before comparing against %s", ciWorkflowPath, ciPins, releaseWorkflowPath)
		}
	}
	releasePins := goVersionPins(t, releaseWorkflowPath, releaseWorkflow(t, root))
	for _, p := range releasePins {
		if p != ciPins[0] {
			t.Errorf("%s pins go-version %q, but %s pins %q; released binaries must be built by the toolchain the gate ran", releaseWorkflowPath, p, ciWorkflowPath, ciPins[0])
		}
	}
}

// matrixTarget is one leg of release.yml's build matrix.
type matrixTarget struct {
	assetName string
	goos      string
	goarch    string
}

var (
	matrixAssetRe  = regexp.MustCompile(`^\s*-\s+asset-name:\s*(\S+)\s*$`)
	matrixGoosRe   = regexp.MustCompile(`^\s+goos:\s*([a-z0-9]+)\s*$`)
	matrixGoarchRe = regexp.MustCompile(`^\s+goarch:\s*([a-z0-9]+)\s*$`)
)

// releaseMatrixTargets line-scans release.yml's `matrix: include:` block.
// The three regexps deliberately require plain lowercase literal values,
// so the build step's `goos: ${{ matrix.goos }}` env passthrough is never
// mistaken for a matrix declaration.
func releaseMatrixTargets(t *testing.T, root string) []matrixTarget {
	t.Helper()
	var targets []matrixTarget
	var cur *matrixTarget
	sc := bufio.NewScanner(strings.NewReader(releaseWorkflow(t, root)))
	for sc.Scan() {
		line := sc.Text()
		if m := matrixAssetRe.FindStringSubmatch(line); m != nil {
			targets = append(targets, matrixTarget{assetName: m[1]})
			cur = &targets[len(targets)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := matrixGoosRe.FindStringSubmatch(line); m != nil {
			cur.goos = m[1]
			continue
		}
		if m := matrixGoarchRe.FindStringSubmatch(line); m != nil {
			cur.goarch = m[1]
			continue
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning %s: %v", releaseWorkflowPath, err)
	}
	if len(targets) == 0 {
		t.Fatalf("parsed zero matrix legs from %s; releaseMatrixTargets or the workflow's matrix shape is almost certainly broken", releaseWorkflowPath)
	}
	return targets
}

// wantMatrixTargets is the set of platforms a release is expected to ship:
// Linux and macOS, x86_64 and arm64. Windows is deliberately absent (the
// sibling repos ship the same four).
var wantMatrixTargets = map[string]bool{
	"linux/amd64":  true,
	"linux/arm64":  true,
	"darwin/amd64": true,
	"darwin/arm64": true,
}

// assetNamePrefix is what every release asset must be called, so a
// downloaded binary is self-identifying.
const assetNamePrefix = "agent-logs-extractor-"

// TestReleaseWorkflowMatrixCoversFourTargets asserts the matrix ships
// exactly the four intended platforms — no more (a leg nobody meant to add
// silently uploads an asset) and no fewer (dropping darwin/arm64 in a
// refactor would quietly stop shipping the most common developer
// platform).
func TestReleaseWorkflowMatrixCoversFourTargets(t *testing.T) {
	root := repoRoot(t)
	targets := releaseMatrixTargets(t, root)

	got := make(map[string]bool, len(targets))
	for _, tgt := range targets {
		if tgt.goos == "" || tgt.goarch == "" {
			t.Errorf("matrix leg %q is missing goos and/or goarch (got goos=%q goarch=%q)", tgt.assetName, tgt.goos, tgt.goarch)
			continue
		}
		key := tgt.goos + "/" + tgt.goarch
		if got[key] {
			t.Errorf("matrix declares %s more than once", key)
		}
		got[key] = true
		if !strings.HasPrefix(tgt.assetName, assetNamePrefix) {
			t.Errorf("matrix leg %s has asset-name %q, want a name starting with %q", key, tgt.assetName, assetNamePrefix)
		}
	}

	for key := range wantMatrixTargets {
		if !got[key] {
			t.Errorf("%s's matrix does not build %s", releaseWorkflowPath, key)
		}
	}
	for key := range got {
		if !wantMatrixTargets[key] {
			t.Errorf("%s's matrix builds %s, which is not one of the four intended targets %v", releaseWorkflowPath, key, wantMatrixTargets)
		}
	}
}

// --- .releaserc.json -------------------------------------------------------

// releasercConfig is the subset of .releaserc.json these checks read.
// Plugins are a heterogeneous array: a bare string, or a [name, options]
// pair — hence json.RawMessage rather than a typed shape.
type releasercConfig struct {
	Branches []string          `json:"branches"`
	Plugins  []json.RawMessage `json:"plugins"`
}

// pluginOptions returns the options object of the named plugin, or nil if
// the plugin is configured bare (or absent).
func pluginOptions(t *testing.T, cfg releasercConfig, name string) map[string]any {
	t.Helper()
	for _, raw := range cfg.Plugins {
		var bare string
		if err := json.Unmarshal(raw, &bare); err == nil {
			continue // configured with no options
		}
		var pair []json.RawMessage
		if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
			continue
		}
		var pluginName string
		if err := json.Unmarshal(pair[0], &pluginName); err != nil || pluginName != name {
			continue
		}
		var opts map[string]any
		if err := json.Unmarshal(pair[1], &opts); err != nil {
			t.Fatalf(".releaserc.json: plugin %s's options are not an object: %v", name, err)
		}
		return opts
	}
	return nil
}

// TestReleasercIsWellFormed asserts .releaserc.json parses as JSON at all
// (semantic-release fails at run time on a malformed config, which on this
// pipeline means discovering it only after a merge to main) and that the
// three settings the rest of the pipeline actually depends on are present.
func TestReleasercIsWellFormed(t *testing.T) {
	root := repoRoot(t)
	raw := readFile(t, filepath.Join(root, ".releaserc.json"))

	var cfg releasercConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf(".releaserc.json does not parse as JSON: %v", err)
	}

	if len(cfg.Branches) != 1 || cfg.Branches[0] != "main" {
		t.Errorf(".releaserc.json branches = %v, want [\"main\"]; release.yml's workflow_run trigger only ever fires on main", cfg.Branches)
	}

	// The `[skip ci]` marker is half of the infinite-loop guard: without
	// it, the chore(release) commit @semantic-release/git pushes to main
	// would re-trigger CI, which would re-trigger Release. (The other half
	// is that a push made with GITHUB_TOKEN does not trigger workflows at
	// all — belt and braces, deliberately.)
	gitOpts := pluginOptions(t, cfg, "@semantic-release/git")
	if gitOpts == nil {
		t.Fatal(".releaserc.json configures no @semantic-release/git options; the release commit's message cannot be checked")
	}
	msg, _ := gitOpts["message"].(string)
	if !strings.Contains(msg, "[skip ci]") {
		t.Errorf("@semantic-release/git message %q does not contain \"[skip ci]\"; the release commit would re-trigger CI", msg)
	}
	if !strings.Contains(msg, "${nextRelease.version}") {
		t.Errorf("@semantic-release/git message %q does not interpolate ${nextRelease.version}; every release commit would carry the same subject", msg)
	}

	changelogOpts := pluginOptions(t, cfg, "@semantic-release/changelog")
	if changelogOpts == nil {
		t.Fatal(".releaserc.json configures no @semantic-release/changelog options")
	}
	if got, _ := changelogOpts["changelogFile"].(string); got != "CHANGELOG.md" {
		t.Errorf("@semantic-release/changelog changelogFile = %q, want \"CHANGELOG.md\" — the same path @semantic-release/git commits as an asset", got)
	}
}

// --- package.json <-> pnpm-lock.yaml ---------------------------------------

// packageJSON is the subset of package.json these checks read.
type packageJSON struct {
	DevDependencies map[string]string `json:"devDependencies"`
}

// lockEntryRe matches a `importers: -> .: -> devDependencies:` entry name
// at six-space indentation, optionally single-quoted (pnpm quotes scoped
// names like '@semantic-release/git').
var lockEntryRe = regexp.MustCompile(`^ {6}'?([^':]+)'?:\s*$`)

// lockSpecifierRe matches the `specifier:` line under such an entry.
var lockSpecifierRe = regexp.MustCompile(`^ {8}specifier:\s*(\S+)\s*$`)

// TestPnpmLockMatchesPackageJSON stands guard over the one way this
// pipeline can fail before it does anything at all: release.yml runs
// `pnpm install --frozen-lockfile`, which hard-errors if pnpm-lock.yaml
// disagrees with package.json. Bumping a devDependency and forgetting to
// regenerate the lock is invisible locally (nothing in `just gate` runs
// pnpm) and turns the Release job red on main, after the merge.
func TestPnpmLockMatchesPackageJSON(t *testing.T) {
	root := repoRoot(t)

	var pkg packageJSON
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "package.json"))), &pkg); err != nil {
		t.Fatalf("package.json does not parse as JSON: %v", err)
	}
	if len(pkg.DevDependencies) == 0 {
		t.Fatal("package.json declares zero devDependencies; the comparison below would be vacuous")
	}

	locked := lockedDevDependencies(t, filepath.Join(root, "pnpm-lock.yaml"))
	if len(locked) == 0 {
		t.Fatal("parsed zero devDependencies from pnpm-lock.yaml's importers -> `.` block; lockEntryRe or the lockfile's shape is almost certainly broken")
	}

	for name, want := range pkg.DevDependencies {
		got, ok := locked[name]
		if !ok {
			t.Errorf("package.json declares devDependency %s, which pnpm-lock.yaml does not lock; `pnpm install --frozen-lockfile` would fail in the Release job", name)
			continue
		}
		if got != want {
			t.Errorf("devDependency %s: package.json wants %q, pnpm-lock.yaml locks specifier %q; regenerate the lock with `pnpm install --lockfile-only`", name, want, got)
		}
	}
	for name := range locked {
		if _, ok := pkg.DevDependencies[name]; !ok {
			t.Errorf("pnpm-lock.yaml locks devDependency %s, which package.json no longer declares", name)
		}
	}
}

// lockedDevDependencies line-scans pnpm-lock.yaml for the root importer's
// devDependencies block and returns name -> specifier. Line-scanned rather
// than YAML-parsed on purpose: see this file's package doc on why go.mod
// stays free of a YAML dependency.
func lockedDevDependencies(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()

	const (
		outside = iota
		inImporters
		inRootImporter
		inDevDeps
	)
	state := outside
	deps := make(map[string]string)
	var current string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		switch state {
		case outside:
			if line == "importers:" {
				state = inImporters
			}
		case inImporters:
			if line == "  .:" {
				state = inRootImporter
			} else if !strings.HasPrefix(line, "  ") {
				state = outside // left the importers block entirely
			}
		case inRootImporter:
			if line == "    devDependencies:" {
				state = inDevDeps
			} else if !strings.HasPrefix(line, "    ") {
				return deps // left the root importer
			}
		case inDevDeps:
			if !strings.HasPrefix(line, "      ") {
				return deps // left the devDependencies block
			}
			if m := lockEntryRe.FindStringSubmatch(line); m != nil {
				current = m[1]
				continue
			}
			if m := lockSpecifierRe.FindStringSubmatch(line); m != nil && current != "" {
				deps[current] = m[1]
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning %s: %v", path, err)
	}
	return deps
}
