//go:build integration

// This file carries the integration tag because it shells out to the Go
// toolchain — several `go build` invocations, one of them cross-compiling
// four ways — which is far too slow for the untagged `just test` /
// `just gate`. It runs under `just test-integration`, i.e.
// `just gate-expensive`, which is what CI's two "Gate (expensive, ...)"
// jobs run.
//
// The static half of this package (release_test.go, untagged) proves the
// release workflow's -X path *names* a symbol that exists. This file
// proves the stronger, end-to-end thing: that building with that exact
// ldflags string actually makes the binary report the injected version.
// The two are not redundant — a symbol path can be spelled correctly and
// still fail to take effect (a `const Version`, an unexported one, a
// second package shadowing it), and only a real build-and-run catches
// that.
package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ldflagsProbeVersion is the sentinel substituted for release.yml's
// ${{ needs.release.outputs.new-release-version }} expression. It is
// deliberately nothing a real build could produce by accident: not "dev"
// (the compiled-in default), and not a plausible semver.
const ldflagsProbeVersion = "9.9.9-ldflags-probe"

// TestReleaseLdflagsInjectVersion discharges AC-RELEASE-01.
//
// It reads the -ldflags string out of .github/workflows/release.yml via
// releaseLdflags (release_test.go) and substitutes the sentinel for the
// Actions version expression. It must NEVER hardcode the flags: a
// hardcoded copy proves only that Go's -X mechanism works, which was never
// in doubt — the thing under test is the string release.yml will actually
// pass, so that string has to come from release.yml.
//
// This closes the gap the ticket's "Done when" names. AC-CLI-01 only
// asserts `version` prints something non-empty, and tests/e2e/ builds the
// binary with no -ldflags at all, so before this test nothing in the repo
// distinguished a correctly-injected release binary from one reporting
// "dev".
func TestReleaseLdflagsInjectVersion(t *testing.T) {
	root := repoRoot(t)

	ldflags := releaseLdflags(t, root)
	if !strings.Contains(ldflags, releaseVersionExpr) {
		t.Fatalf("release.yml's ldflags %q does not contain %s, so there is nothing to substitute a version into", ldflags, releaseVersionExpr)
	}
	substituted := strings.ReplaceAll(ldflags, releaseVersionExpr, ldflagsProbeVersion)

	target := releaseBuildTarget(t, root)
	bin := filepath.Join(t.TempDir(), "agent-logs-extractor-ldflags-probe")

	build := exec.Command("go", "build", "-trimpath", "-ldflags", substituted, "-o", bin, target)
	build.Dir = root
	// CGO off and the host's own GOOS/GOARCH: this binary has to RUN here,
	// unlike TestReleaseMatrixTargetsCompile below which only compiles.
	build.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+runtime.GOOS,
		"GOARCH="+runtime.GOARCH,
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build with release.yml's ldflags failed: %v\nldflags: %s\noutput:\n%s", err, substituted, out)
	}

	run := exec.Command(bin, "version")
	stdout, err := run.Output()
	if err != nil {
		t.Fatalf("running %s version: %v", bin, err)
	}
	got := strings.TrimSpace(string(stdout))

	if got == "dev" {
		t.Fatalf("a binary built with release.yml's own -ldflags reports version %q — the compiled-in default, meaning the -X injection did NOTHING.\n"+
			"The culprit is %s: its -X symbol path does not name the variable this tree actually declares, so the linker silently ignored it.\n"+
			"ldflags used (version expression substituted): %s",
			got, releaseWorkflowPath, substituted)
	}
	if got != ldflagsProbeVersion {
		t.Fatalf("`version` printed %q, want %q; built with %s's ldflags: %s", got, ldflagsProbeVersion, releaseWorkflowPath, substituted)
	}
}

// TestReleaseMatrixTargetsCompile cross-compiles the release build target
// for every leg of release.yml's matrix. It asserts only that compilation
// succeeds — the cross-built binaries cannot be run here — which is
// exactly the failure mode worth catching: a dependency (or a new file
// with a build constraint) that builds fine on the developer's linux/amd64
// but breaks darwin/arm64 would otherwise be discovered by a red
// build-binaries job after a release tag had already been cut and
// published.
func TestReleaseMatrixTargetsCompile(t *testing.T) {
	root := repoRoot(t)
	target := releaseBuildTarget(t, root)
	targets := releaseMatrixTargets(t, root)

	for _, tgt := range targets {
		t.Run(tgt.goos+"_"+tgt.goarch, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), tgt.assetName)
			cmd := exec.Command("go", "build", "-trimpath", "-o", out, target)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"CGO_ENABLED=0",
				"GOOS="+tgt.goos,
				"GOARCH="+tgt.goarch,
			)
			if combined, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cross-compiling %s for %s/%s failed: %v\noutput:\n%s", target, tgt.goos, tgt.goarch, err, combined)
			}
		})
	}
}
