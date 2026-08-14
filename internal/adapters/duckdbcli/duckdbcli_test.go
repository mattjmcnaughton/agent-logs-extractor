package duckdbcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// TestNameReturnsSinkName pins Name() against SinkName directly, so a typo
// in one but not the other fails here rather than only surfacing later at
// the CLI's Name()-keyed sink lookup.
func TestNameReturnsSinkName(t *testing.T) {
	a := New(nil)
	if got := a.Name(); got != SinkName {
		t.Errorf("Name() = %q, want %q", got, SinkName)
	}
}

// TestExportMissingBinaryErrorNamesTheInstallHint pins ErrBinaryNotFound
// and its install hint. The store root exists (empty, no sessions/) so the
// storeHasDocs check that runs first passes cleanly, isolating this test to
// exactly the binary-resolution failure.
func TestExportMissingBinaryErrorNamesTheInstallHint(t *testing.T) {
	a := New(nil, WithBinary("agent-logs-extractor-nonexistent-duckdb-xyz"))
	root := t.TempDir()

	err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: root, Out: filepath.Join(t.TempDir(), "out.duckdb")})
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("Export err = %v, want it to wrap ErrBinaryNotFound", err)
	}
	if !strings.Contains(err.Error(), "duckdb.org") {
		t.Errorf("Export err = %q, want it to name the install hint (duckdb.org)", err.Error())
	}
	if !strings.Contains(err.Error(), "export") {
		t.Errorf("Export err = %q, want it to note only export needs the binary", err.Error())
	}
}

// TestExportLeavesAPreviousOutputIntactWhenTheBinaryIsMissing proves the
// "never touch Out before a successful run" guarantee for the
// binary-resolution failure path specifically: the missing-binary check
// happens strictly before req.Out is ever opened, truncated, or removed.
func TestExportLeavesAPreviousOutputIntactWhenTheBinaryIsMissing(t *testing.T) {
	a := New(nil, WithBinary("agent-logs-extractor-nonexistent-duckdb-xyz"))
	root := t.TempDir()

	out := filepath.Join(t.TempDir(), "out.duckdb")
	want := []byte("previous export contents")
	if err := os.WriteFile(out, want, 0o600); err != nil {
		t.Fatalf("seeding previous output: %v", err)
	}

	if err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: root, Out: out}); err == nil {
		t.Fatal("Export with a missing binary: want error, got nil")
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading previous output after failed Export: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("previous output = %q, want untouched %q", got, want)
	}
}

// TestExportRejectsAnAbsentStoreRoot pins ErrNoStore for a store root that
// does not exist on disk at all, naming `sync` as the fix — as opposed to
// an existing-but-empty store, which is a legitimate empty export (see
// TestStoreHasDocs). No duckdb binary is ever resolved on this path: the
// store-root check runs first.
func TestExportRejectsAnAbsentStoreRoot(t *testing.T) {
	a := New(nil, WithBinary("agent-logs-extractor-nonexistent-duckdb-xyz"))
	root := filepath.Join(t.TempDir(), "does-not-exist")

	err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: root, Out: filepath.Join(t.TempDir(), "out.duckdb")})
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("Export err = %v, want it to wrap ErrNoStore", err)
	}
	if !strings.Contains(err.Error(), "sync") {
		t.Errorf("Export err = %q, want it to name `sync`", err.Error())
	}
}

// TestStoreHasDocs pins storeHasDocs' three-way contract (C.3) directly,
// independent of Export's other checks.
func TestStoreHasDocs(t *testing.T) {
	t.Run("absent root is ErrNoStore", func(t *testing.T) {
		_, err := storeHasDocs(filepath.Join(t.TempDir(), "nope"))
		if !errors.Is(err, ErrNoStore) {
			t.Fatalf("err = %v, want ErrNoStore", err)
		}
	})

	t.Run("root exists, sessions/ absent: false, nil", func(t *testing.T) {
		root := t.TempDir()
		got, err := storeHasDocs(root)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got {
			t.Error("got true, want false")
		}
	})

	t.Run("sessions/ present but empty: false, nil", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o700); err != nil {
			t.Fatalf("seeding sessions dir: %v", err)
		}
		got, err := storeHasDocs(root)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got {
			t.Error("got true, want false")
		}
	})

	t.Run("sessions/<vendor>/ holds no *.json: false, nil", func(t *testing.T) {
		root := t.TempDir()
		vendorDir := filepath.Join(root, "sessions", "claude")
		if err := os.MkdirAll(vendorDir, 0o700); err != nil {
			t.Fatalf("seeding vendor dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(vendorDir, "not-json.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("seeding non-json file: %v", err)
		}
		got, err := storeHasDocs(root)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got {
			t.Error("got true, want false")
		}
	})

	t.Run("a session file present: true, nil", func(t *testing.T) {
		root := t.TempDir()
		vendorDir := filepath.Join(root, "sessions", "claude")
		if err := os.MkdirAll(vendorDir, 0o700); err != nil {
			t.Fatalf("seeding vendor dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(vendorDir, "abc.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("seeding session file: %v", err)
		}
		got, err := storeHasDocs(root)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if !got {
			t.Error("got false, want true")
		}
	})
}

// buildFakeDuckdb writes a stand-in "duckdb" executable that ignores its
// stdin script entirely and just creates an empty file at its own last CLI
// argument (the snapshot path Export passes) before exiting 0 — enough to
// let Export's later os.Chmod/os.Rename succeed without a real duckdb CLI
// or any duckdb-shaped output. It is referenced only via WithBinary, an
// absolute path never placed on PATH, so nothing named "duckdb" is ever
// resolvable via exec.LookPath in this test process.
func buildFakeDuckdb(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-duckdb.sh")
	// POSIX "for last do :; done" is the standard idiom for "the last
	// positional parameter" in a shell with no arrays.
	script := "#!/bin/sh\nfor last do :; done\n: > \"$last\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing fake duckdb script: %v", err)
	}
	return path
}

// TestExportWithARelativeOutResolvesAgainstTheProcessCwdNotTheStoreRoot pins
// B1: a relative req.Out must resolve the same way a user's shell resolves
// `--out ./logs.duckdb` — against the process's cwd — even when req.Out's
// process cwd differs from req.StoreRoot (cmd.Dir for the duckdb
// subprocess). Before the fix, os.MkdirTemp created the temp export
// directory relative to the process cwd, but the resulting (still
// relative) snapshot path was then handed to a subprocess whose cwd is
// req.StoreRoot, so duckdb tried to resolve it there instead and failed to
// create it — the exact failure a real `--out ./logs.duckdb` run hits
// unless the caller happens to be cd'd into the store root.
func TestExportWithARelativeOutResolvesAgainstTheProcessCwdNotTheStoreRoot(t *testing.T) {
	fakeDuckdb := buildFakeDuckdb(t)
	a := New(nil, WithBinary(fakeDuckdb))

	storeRoot := t.TempDir()
	cwd := t.TempDir()
	t.Chdir(cwd)

	relOut := filepath.Join("sub", "out.duckdb")
	if err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: storeRoot, Out: relOut}); err != nil {
		t.Fatalf("Export with a relative Out (process cwd %s, store root %s): %v", cwd, storeRoot, err)
	}

	want := filepath.Join(cwd, "sub", "out.duckdb")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected the export at the cwd-relative path %s, but it was not created: %v", want, err)
	}

	entries, err := os.ReadDir(storeRoot)
	if err != nil {
		t.Fatalf("reading store root %s: %v", storeRoot, err)
	}
	for _, e := range entries {
		t.Errorf("store root %s unexpectedly gained entry %q; a relative Out must resolve against the process cwd, never the store root", storeRoot, e.Name())
	}
}

// TestExportRestrictsOutputFilePermissionsTo0600 pins that the exported
// database file ends up mode 0600 regardless of the ambient umask the
// duckdb subprocess created it under — the export holds the same verbatim
// prompts/code/output that jsonlstore's canonical store protects at 0600,
// so it must not be left more permissive by default (typically 0644 under
// a duckdb process's own umask). The fake binary here creates the snapshot
// with a plain shell redirection (`: > file`), which is umask-permissive
// by construction, so a passing test proves Export's own os.Chmod did the
// work rather than the snapshot happening to already be 0600.
func TestExportRestrictsOutputFilePermissionsTo0600(t *testing.T) {
	fakeDuckdb := buildFakeDuckdb(t)
	a := New(nil, WithBinary(fakeDuckdb))

	storeRoot := t.TempDir()
	out := filepath.Join(t.TempDir(), "out.duckdb")

	if err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: storeRoot, Out: out}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat %s: %v", out, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("output file mode = %o, want 0600", got)
	}
}

// Interface conformance is also asserted in duckdbcli.go; this pins it at
// the test-package level too, matching the fakes/naming test convention
// elsewhere in the repo.
var _ ports.Exporter = (*Adapter)(nil)
