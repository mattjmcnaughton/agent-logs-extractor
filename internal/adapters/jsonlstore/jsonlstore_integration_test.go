//go:build integration

package jsonlstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// These tests exercise the store against a real filesystem
// (afero.NewOsFs()) rooted at a real temp directory, reading the result
// back with plain os/filepath.WalkDir rather than afero, so a bug that only
// afero's abstraction papers over cannot hide from this tier.

// --- I1 ------------------------------------------------------------------

func TestSwapOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := afero.NewOsFs()
	store := New(fsys, root, nil)
	ctx := context.Background()

	// Generation 1: {A, B}.
	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1-a")); err != nil {
		t.Fatalf("Put(A): %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen1-b")); err != nil {
		t.Fatalf("Put(B): %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit gen1: %v", err)
	}

	// Generation 2: {B'} only.
	r2, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild gen2: %v", err)
	}
	if err := r2.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2-b")); err != nil {
		t.Fatalf("Put(B'): %v", err)
	}
	if err := r2.Commit(ctx); err != nil {
		t.Fatalf("Commit gen2: %v", err)
	}

	files := walkFiles(t, root)
	want := []string{filepath.Join("sessions", "claude", "bbb-uuid.json")}
	assertStringSlicesEqual(t, files, want)

	for _, name := range mustReadDirNames(t, root) {
		if strings.HasPrefix(name, stagingPrefix) || strings.HasPrefix(name, trashPrefix) {
			t.Errorf("leftover directory %q after a clean two-generation swap", name)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "sessions", "claude", "bbb-uuid.json"))
	if err != nil {
		t.Fatalf("reading final doc: %v", err)
	}
	assertDocBytesEqual(t, data, doc(model.VendorClaude, "bbb-uuid", "gen2-b"))
}

// --- I2 ------------------------------------------------------------------

func TestDiscardOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := afero.NewOsFs()
	store := New(fsys, root, nil)
	ctx := context.Background()

	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	before := readFileMap(t, filepath.Join(root, "sessions"))

	r2, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild gen2: %v", err)
	}
	if err := r2.Put(ctx, doc(model.VendorClaude, "ccc-uuid", "gen2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r2.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	after := readFileMap(t, filepath.Join(root, "sessions"))
	assertFileMapsEqual(t, after, before)

	for _, name := range mustReadDirNames(t, root) {
		if strings.HasPrefix(name, stagingPrefix) {
			t.Errorf("leftover staging directory %q after Discard", name)
		}
	}
}

// --- I2b -----------------------------------------------------------------

// TestPutRejectsVendorTraversalOnRealFilesystem pins the B2 fix on a real
// filesystem: before it, a Vendor of ".." escaped to itself and relPath
// joined it as a directory element, so Put wrote a file as a sibling of the
// staging directory (inside the store root but outside "<staging>/") that
// survived Commit as a stray file forever. Vendor "." would similarly
// resolve to the staging directory itself.
func TestPutRejectsVendorTraversalOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := afero.NewOsFs()
	store := New(fsys, root, nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	before := mustReadDirNames(t, root)

	badDoc := doc(model.VendorClaude, "aaa-uuid", "x")
	badDoc.Session.Vendor = ".."
	badDoc.Session.SessionID = "..:x"
	if err := r.Put(ctx, badDoc); err == nil {
		t.Fatalf("Put with a traversal vendor should have failed")
	}

	// Nothing should have landed outside the staging directory: no new
	// entry directly under root...
	after := mustReadDirNames(t, root)
	if len(after) != len(before) {
		t.Errorf("root directory gained entries after a rejected Put: before %v, after %v", before, after)
	}
	// ...and specifically no "x.json" sibling of staging, which is exactly
	// what the unrejected traversal used to write.
	stray := filepath.Join(root, "x.json")
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("expected no file to land outside the staging dir at %s", stray)
	}
}

// --- I3 ------------------------------------------------------------------

func TestFsFailureMidWriteOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	base := afero.NewOsFs()

	plainStore := New(base, root, nil)
	ctx := context.Background()
	r1, err := plainStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1-a")); err != nil {
		t.Fatalf("Put(A): %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen1-b")); err != nil {
		t.Fatalf("Put(B): %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit gen1: %v", err)
	}
	before := readFileMap(t, filepath.Join(root, "sessions"))

	failing := &failFs{
		Fs: base,
		OpenFileErr: func(name string, flag int, perm os.FileMode) error {
			if strings.Contains(name, "bbb-uuid.json") {
				return errors.New("injected: open failed")
			}
			return nil
		},
	}
	failStore := New(failing, root, nil)

	r2, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild gen2: %v", err)
	}
	defer r2.Discard()

	if err := r2.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen2-a")); err != nil {
		t.Fatalf("Put(A) should succeed: %v", err)
	}
	if err := r2.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2-b")); err == nil {
		t.Fatalf("Put(B) should have failed via the injected fault")
	}
	if err := r2.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	after := readFileMap(t, filepath.Join(root, "sessions"))
	assertFileMapsEqual(t, after, before)
}

// --- I3a -----------------------------------------------------------------

// TestCommitRenameFailureRollsBackOnRealFilesystem mirrors the unit tier's
// TestCommitRenameFailureRollsBack on a real filesystem. failfs_test.go
// says failFs carries no build tag specifically so both tiers can pin
// Commit's rollback with it, but until now only OpenFileErr was ever
// exercised on OsFs — RenameErr, and therefore this rollback path, the
// highest-consequence failure mode in the package, was pinned only on
// MemMapFs, whose rename-onto-existing-directory semantics are exactly the
// ones known to differ from a real filesystem's.
func TestCommitRenameFailureRollsBackOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	base := afero.NewOsFs()

	plainStore := New(base, root, nil)
	ctx := context.Background()
	r1, err := plainStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit gen1: %v", err)
	}
	before := readFileMap(t, filepath.Join(root, "sessions"))

	liveDir := filepath.Join(root, sessionsDir)
	failing := &failFs{
		Fs: base,
		RenameErr: func(oldname, newname string) error {
			if strings.HasPrefix(filepath.Base(oldname), stagingPrefix) && newname == liveDir {
				return errors.New("injected: rename staging -> sessions failed")
			}
			return nil
		},
	}
	failStore := New(failing, root, nil)

	r2, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild gen2: %v", err)
	}
	defer r2.Discard()

	if err := r2.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r2.Commit(ctx); err == nil {
		t.Fatalf("Commit should have failed via the injected rename fault")
	}

	after := readFileMap(t, filepath.Join(root, "sessions"))
	assertFileMapsEqual(t, after, before)

	if err := r2.Discard(); err != nil {
		t.Fatalf("Discard after failed Commit: %v", err)
	}
	for _, name := range mustReadDirNames(t, root) {
		if strings.HasPrefix(name, stagingPrefix) {
			t.Errorf("leftover staging directory %q after Discard", name)
		}
		if strings.HasPrefix(name, trashPrefix) {
			t.Errorf("leftover trash directory %q: rollback should have restored it to sessions/", name)
		}
	}
}

// --- I3b -----------------------------------------------------------------

// TestCommitAfterFailedPutRefusesToSwapOnRealFilesystem mirrors the B1 unit
// test on a real filesystem: a failed Put must poison the rebuild so the
// following Commit errors instead of replacing gen1 with a truncated gen2.
func TestCommitAfterFailedPutRefusesToSwapOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	base := afero.NewOsFs()

	plainStore := New(base, root, nil)
	ctx := context.Background()
	r1, err := plainStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit gen1: %v", err)
	}
	before := readFileMap(t, filepath.Join(root, "sessions"))

	failing := &failFs{
		Fs: base,
		OpenFileErr: func(name string, flag int, perm os.FileMode) error {
			if strings.Contains(name, "bbb-uuid.json") {
				return errors.New("injected: open failed")
			}
			return nil
		},
	}
	failStore := New(failing, root, nil)

	r2, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild gen2: %v", err)
	}
	defer r2.Discard()

	if err := r2.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2")); err == nil {
		t.Fatalf("Put should have failed via the injected fault")
	}
	if err := r2.Commit(ctx); err == nil {
		t.Fatalf("Commit after a failed Put should refuse to swap, got nil error")
	}

	after := readFileMap(t, filepath.Join(root, "sessions"))
	assertFileMapsEqual(t, after, before)
}

// --- I4 ------------------------------------------------------------------

func TestCommitFailsWhenSessionsIsNotADirectoryOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := afero.NewOsFs()
	store := New(fsys, root, nil)
	ctx := context.Background()

	if err := os.MkdirAll(root, dirMode); err != nil {
		t.Fatalf("preparing root: %v", err)
	}
	sessionsPath := filepath.Join(root, sessionsDir)
	if err := os.WriteFile(sessionsPath, []byte("not a directory"), fileMode); err != nil {
		t.Fatalf("seeding sessions-as-a-file: %v", err)
	}

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()
	if err := r.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err == nil {
		t.Fatalf("Commit should fail: OsFs must reject a rename onto a non-directory (ENOTDIR)")
	}

	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("reading %s after failed Commit: %v", sessionsPath, err)
	}
	if string(data) != "not a directory" {
		t.Errorf("expected sessions path untouched, got %q", data)
	}
}

// --- I5 ------------------------------------------------------------------

func TestLeftoverStagingSweptOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := afero.NewOsFs()
	store := New(fsys, root, nil)
	ctx := context.Background()

	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	staleStaging := filepath.Join(root, stagingPrefix+"stale")
	staleTrash := filepath.Join(root, trashPrefix+"stale")
	if err := os.MkdirAll(filepath.Join(staleStaging, "claude"), dirMode); err != nil {
		t.Fatalf("seeding stale staging dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleStaging, "claude", "leftover.json"), []byte("{}\n"), fileMode); err != nil {
		t.Fatalf("seeding stale staging file: %v", err)
	}
	if err := os.MkdirAll(staleTrash, dirMode); err != nil {
		t.Fatalf("seeding stale trash dir: %v", err)
	}

	r2, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r2.Discard()

	if _, err := os.Stat(staleStaging); !os.IsNotExist(err) {
		t.Errorf("expected stale staging dir to be swept, stat err = %v", err)
	}
	if _, err := os.Stat(staleTrash); !os.IsNotExist(err) {
		t.Errorf("expected stale trash dir to be swept, stat err = %v", err)
	}

	// The live generation from before the sweep must be untouched.
	data, err := os.ReadFile(filepath.Join(root, "sessions", "claude", "aaa-uuid.json"))
	if err != nil {
		t.Fatalf("reading live doc after sweep: %v", err)
	}
	if len(data) == 0 {
		t.Errorf("live doc unexpectedly empty after sweep")
	}
}

// --- I6 ------------------------------------------------------------------

func TestModesAndSingleLineJSONOnRealFilesystem(t *testing.T) {
	root := t.TempDir()
	fsys := afero.NewOsFs()
	store := New(fsys, root, nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	d := doc(model.VendorClaude, "aaa-uuid", "hello")
	if err := r.Put(ctx, d); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	sessionsPath := filepath.Join(root, "sessions")
	fi, err := os.Stat(sessionsPath)
	if err != nil {
		t.Fatalf("stat sessions dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != dirMode {
		t.Errorf("sessions dir mode = %o, want %o", perm, dirMode)
	}

	claudeDir := filepath.Join(sessionsPath, "claude")
	fi, err = os.Stat(claudeDir)
	if err != nil {
		t.Fatalf("stat claude dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != dirMode {
		t.Errorf("claude dir mode = %o, want %o", perm, dirMode)
	}

	filePath := filepath.Join(claudeDir, "aaa-uuid.json")
	fi, err = os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat doc file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != fileMode {
		t.Errorf("doc file mode = %o, want %o", perm, fileMode)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("reading doc file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one line, got %d: %q", len(lines), data)
	}
	var got model.SessionDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Session.SessionID != d.Session.SessionID {
		t.Errorf("session_id = %q, want %q", got.Session.SessionID, d.Session.SessionID)
	}
}

// --- shared helpers --------------------------------------------------------

func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return files
}

func mustReadDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readFileMap(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

func assertFileMapsEqual(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("file map size mismatch: got %d, want %d", len(got), len(want))
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Fatalf("missing file %q", k)
			continue
		}
		if string(gv) != string(wv) {
			t.Fatalf("file %q differs:\ngot:  %s\nwant: %s", k, gv, wv)
		}
	}
}

func assertStringSlicesEqual(t *testing.T, got, want []string) {
	t.Helper()
	gotSorted, wantSorted := slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))
	if !slices.Equal(gotSorted, wantSorted) {
		t.Fatalf("got %v, want %v", gotSorted, wantSorted)
	}
}
