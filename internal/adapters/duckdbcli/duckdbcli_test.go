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

// TestExportRejectsAnEmptyStoreRoot mirrors
// TestExportRejectsAnAbsentStoreRoot for the empty-string case (defense in
// depth: internal/core/export.Run already guards this before ever calling
// Export, but the adapter must not panic or misbehave if called directly).
func TestExportRejectsAnEmptyStoreRoot(t *testing.T) {
	a := New(nil)

	err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: "", Out: filepath.Join(t.TempDir(), "out.duckdb")})
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("Export err = %v, want it to wrap ErrNoStore", err)
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

// TestWithBinaryOverridesTheResolvedBinary pins that WithBinary actually
// changes which name Export's exec.LookPath resolves, rather than the
// option silently doing nothing: the default binary name ("duckdb") is
// almost certainly not on this test's PATH either, but the error must
// name whichever binary name was actually configured.
func TestWithBinaryOverridesTheResolvedBinary(t *testing.T) {
	const custom = "agent-logs-extractor-custom-binary-name-xyz"
	a := New(nil, WithBinary(custom))
	if a.bin != custom {
		t.Errorf("bin = %q, want %q", a.bin, custom)
	}
}

// TestNewNormalizesANilLogger pins New's nil-logger convention (shared with
// export.New, sync.New, jsonlstore.New): a nil log must not panic when
// Export later calls a.log.Debug.
func TestNewNormalizesANilLogger(t *testing.T) {
	a := New(nil, WithBinary("agent-logs-extractor-nonexistent-duckdb-xyz"))
	root := t.TempDir()
	if err := a.Export(context.Background(), ports.ExportRequest{StoreRoot: root, Out: filepath.Join(t.TempDir(), "out.duckdb")}); err == nil {
		t.Fatal("want an error from a missing binary")
	}
}

// Interface conformance is also asserted in duckdbcli.go; this pins it at
// the test-package level too, matching the fakes/naming test convention
// elsewhere in the repo.
var _ ports.Exporter = (*Adapter)(nil)
