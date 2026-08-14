//go:build e2e

// Package e2e is the black-box acceptance tier: it runs the compiled
// agent-logs-extractor binary as a subprocess and asserts on its
// observable behavior (stdout, stderr, exit code, filesystem effects)
// against docs/acceptance.md. The only internal/ import permitted anywhere
// in this package is internal/testing/ (logfixture) — no internal/core or
// internal/adapters package may ever be imported here; that constraint is
// exactly what keeps this tier honestly black-box.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// alxbin is the absolute path of the binary under test.
var alxbin string

func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// runSuite resolves $ALXBIN, the binary under test. An unset $ALXBIN is not
// a failure (D1): the suite builds one itself, into a temp directory it
// cleans up afterward, and exports ALXBIN so every sandbox invocation below
// resolves the same binary. The just/container recipes pre-build and set
// $ALXBIN so every scenario in one `just test-e2e` run shares one binary
// instead of paying a rebuild per test file.
func runSuite(m *testing.M) (int, error) {
	alxbin = os.Getenv("ALXBIN")
	if alxbin == "" {
		dir, err := os.MkdirTemp("", "alxbin-")
		if err != nil {
			return 0, err
		}
		defer os.RemoveAll(dir)
		alxbin = filepath.Join(dir, "agent-logs-extractor")
		build := exec.Command("go", "build", "-o", alxbin, "./cmd/agent-logs-extractor")
		// repoRootForCoverage (coverage_test.go, untagged so it's always
		// part of this package) duplicated here byte-for-byte until this
		// cleanup; call it instead of keeping a second copy.
		build.Dir = repoRootForCoverage()
		if out, err := build.CombinedOutput(); err != nil {
			return 0, fmt.Errorf("building binary under test: %v\n%s", err, out)
		}
		os.Setenv("ALXBIN", alxbin)
	}
	return m.Run(), nil
}
