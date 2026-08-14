//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// wantStdout asserts res.stdout is exactly want.
func wantStdout(t *testing.T, res result, want string) {
	t.Helper()
	if res.stdout != want {
		t.Errorf("stdout = %q, want %q (stderr: %s)", res.stdout, want, res.stderr)
	}
}

// wantCode asserts res.code is exactly want.
func wantCode(t *testing.T, res result, want int) {
	t.Helper()
	if res.code != want {
		t.Errorf("exit code = %d, want %d (stdout: %q, stderr: %q)", res.code, want, res.stdout, res.stderr)
	}
}

// wantStderrContains asserts res.stderr contains want as a substring.
func wantStderrContains(t *testing.T, res result, want string) {
	t.Helper()
	if !strings.Contains(res.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", res.stderr, want)
	}
}

// hashTree returns a hash of every regular file's relative path and
// content under dir, so two calls straddling a binary invocation can prove
// "nothing changed" (AC-SCOPE-01) without diffing file-by-file. A missing
// dir hashes to a fixed sentinel rather than erroring, so "the directory
// was never created" trivially differs from "the directory has content".
func hashTree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	if _, err := os.Stat(dir); err != nil {
		return "absent"
	}

	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	sort.Strings(paths)

	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			t.Fatalf("computing relative path for %s: %v", p, err)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		fmt.Fprintf(h, "%s\x00", rel)
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// noLeftovers fails the test if storeRoot holds any ".staging-"/".trash-"/
// ".orphan-" prefixed entry — jsonlstore's own leftover-sweep contract
// (internal/core/sync/sync_integration_test.go's leftovers helper mirrors
// this at the use-case tier; this is the binary-level counterpart).
func noLeftovers(t *testing.T, storeRoot string) {
	t.Helper()
	entries, err := os.ReadDir(storeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading %s: %v", storeRoot, err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".staging-") || strings.HasPrefix(name, ".trash-") || strings.HasPrefix(name, ".orphan-") {
			t.Errorf("leftover directory %s under %s after a clean run", name, storeRoot)
		}
	}
}

// requireDuckDB resolves a real duckdb binary on the host PATH. Absent and
// ALX_REQUIRE_DUCKDB is unset: skip (not fail) — this sandbox has no
// duckdb, per this ticket's environment. Absent and ALX_REQUIRE_DUCKDB is
// set (CI's gate-expensive-native/container jobs, and this Dockerfile.duckdb
// image's own ENV): fail hard, so a broken install is unmistakably a test
// failure rather than a silent skip.
func requireDuckDB(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		if os.Getenv("ALX_REQUIRE_DUCKDB") != "" {
			t.Fatalf("duckdb not found on PATH and ALX_REQUIRE_DUCKDB is set: %v", err)
		}
		t.Skipf("duckdb not found on PATH; skipping (set ALX_REQUIRE_DUCKDB=1 to make this fatal): %v", err)
	}
	return bin
}

// duckdbJSON runs sql against the database at dbPath via the real duckdb
// CLI's -json output mode and parses the result, mirroring
// internal/adapters/duckdbcli's own integration-tier queryJSON helper. TZ=UTC
// keeps rendering deterministic across runner timezones.
func duckdbJSON(t *testing.T, bin, dbPath, sql string) []map[string]any {
	t.Helper()
	cmd := exec.Command(bin, dbPath, "-json", "-c", sql)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("duckdb query failed: %v\nquery:\n%s\nstderr:\n%s", err, sql, stderr.String())
	}
	trimmed := strings.TrimSpace(stdout.String())
	if trimmed == "" {
		return nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		t.Fatalf("parsing duckdb -json output: %v\nquery:\n%s\noutput:\n%s", err, sql, stdout.String())
	}
	return rows
}

// isUnder reports whether path is base itself or lies strictly beneath it,
// compared as plain path strings (both are expected already-clean absolute
// paths from filepath.Join, so no further cleaning is needed). Deliberately
// not implemented via filepath.Rel: for two unrelated sibling directories
// Rel still returns a "../.." relative path rather than an error, which
// does not by itself distinguish "under" from "not under".
func isUnder(path, base string) bool {
	if path == base {
		return true
	}
	return strings.HasPrefix(path, base+string(filepath.Separator))
}

// walkFiles calls visit(path) for every regular file under dir. A missing
// dir is not an error — it simply visits nothing, matching os.ReadDir's own
// "not created yet" case for a store that has never been synced.
func walkFiles(dir string, visit func(path string)) error {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			visit(path)
		}
		return nil
	})
}

// asInt coerces a duckdb -json numeric value (unmarshaled as float64 by
// encoding/json) to int.
func asInt(t *testing.T, v any) int {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("value %v (%T) is not a number", v, v)
	}
	return int(f)
}
