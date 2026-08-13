package jsonlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// --- test doc builders -----------------------------------------------------

func doc(vendor model.Vendor, uuid, text string) model.SessionDoc {
	id := string(vendor) + ":" + uuid
	return model.SessionDoc{
		Session: model.Session{
			SessionID:     id,
			Vendor:        vendor,
			ProjectPath:   "/home/user/proj",
			ProjectName:   "proj",
			StartedAt:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			EndedAt:       time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC),
			VendorVersion: "1.0.0",
			SourcePath:    "/fixture/" + uuid + ".jsonl",
		},
		Messages: []model.Message{
			{
				MessageID: id + "-m0",
				SessionID: id,
				Seq:       0,
				Role:      model.RoleUser,
				CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				Text:      text,
				Raw:       json.RawMessage(`{"type":"user"}`),
			},
		},
		ToolCalls: []model.ToolCall{
			{
				ToolCallID: id + "-t0",
				SessionID:  id,
				MessageID:  id + "-m0",
				Seq:        0,
				ToolName:   "Bash",
				Arguments:  json.RawMessage(`{"command":"echo hi"}`),
				Output:     "hi",
				Status:     model.StatusOK,
				CreatedAt:  time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC),
			},
		},
	}
}

// --- equality helpers (field-wise: reflect.DeepEqual is unsafe here, see
// model.go's documented nil-Raw round-trip gotcha, plan §F5) -----------------

func sessionDocsEqual(a, b model.SessionDoc) bool {
	if !sessionsEqual(a.Session, b.Session) {
		return false
	}
	if len(a.Messages) != len(b.Messages) {
		return false
	}
	for i := range a.Messages {
		if !messagesEqual(a.Messages[i], b.Messages[i]) {
			return false
		}
	}
	if len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		if !toolCallsEqual(a.ToolCalls[i], b.ToolCalls[i]) {
			return false
		}
	}
	return true
}

func sessionsEqual(a, b model.Session) bool {
	return a.SessionID == b.SessionID &&
		a.Vendor == b.Vendor &&
		a.ProjectPath == b.ProjectPath &&
		a.ProjectName == b.ProjectName &&
		a.StartedAt.Equal(b.StartedAt) &&
		a.EndedAt.Equal(b.EndedAt) &&
		a.GitBranch == b.GitBranch &&
		a.VendorVersion == b.VendorVersion &&
		a.SourcePath == b.SourcePath
}

func messagesEqual(a, b model.Message) bool {
	return a.MessageID == b.MessageID &&
		a.SessionID == b.SessionID &&
		a.Seq == b.Seq &&
		a.ParentMessageID == b.ParentMessageID &&
		a.Role == b.Role &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.Text == b.Text &&
		a.Model == b.Model &&
		rawEqual(a.Raw, b.Raw)
}

func toolCallsEqual(a, b model.ToolCall) bool {
	return a.ToolCallID == b.ToolCallID &&
		a.SessionID == b.SessionID &&
		a.MessageID == b.MessageID &&
		a.Seq == b.Seq &&
		a.ToolName == b.ToolName &&
		rawEqual(a.Arguments, b.Arguments) &&
		a.Output == b.Output &&
		a.Status == b.Status &&
		a.CreatedAt.Equal(b.CreatedAt)
}

// rawEqual treats a nil json.RawMessage and the literal "null" as
// equivalent, since a nil Raw round-trips through JSON as
// json.RawMessage("null"), never back to nil (model.go).
func rawEqual(a, b json.RawMessage) bool {
	an, bn := string(a), string(b)
	if len(a) == 0 {
		an = "null"
	}
	if len(b) == 0 {
		bn = "null"
	}
	return an == bn
}

// --- filesystem inspection helpers -----------------------------------------

func readSessionDocs(t *testing.T, fsys afero.Fs, sessionsRoot string) map[string]model.SessionDoc {
	t.Helper()
	out := make(map[string]model.SessionDoc)
	exists, err := afero.DirExists(fsys, sessionsRoot)
	if err != nil {
		t.Fatalf("checking %s exists: %v", sessionsRoot, err)
	}
	if !exists {
		return out
	}
	err = afero.Walk(fsys, sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := afero.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		var d model.SessionDoc
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("unmarshal %s: %w", path, err)
		}
		rel, err := filepath.Rel(sessionsRoot, path)
		if err != nil {
			return err
		}
		out[rel] = d
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", sessionsRoot, err)
	}
	return out
}

func snapshotBytes(t *testing.T, fsys afero.Fs, root string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	exists, err := afero.DirExists(fsys, root)
	if err != nil {
		t.Fatalf("checking %s exists: %v", root, err)
	}
	if !exists {
		return out
	}
	err = afero.Walk(fsys, root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := afero.ReadFile(fsys, path)
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
		t.Fatalf("snapshotting %s: %v", root, err)
	}
	return out
}

func assertSnapshotsEqual(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("snapshot size mismatch: got %d files %v, want %d files %v", len(got), sortedKeys(got), len(want), sortedKeys(want))
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Fatalf("missing file %q in snapshot", k)
			continue
		}
		if !bytes.Equal(gv, wv) {
			t.Fatalf("file %q differs:\ngot:  %s\nwant: %s", k, gv, wv)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func dirEntryNames(t *testing.T, fsys afero.Fs, root string) []string {
	t.Helper()
	entries, err := afero.ReadDir(fsys, root)
	if err != nil {
		t.Fatalf("reading dir %s: %v", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func hasPrefixEntry(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func commitDocs(t *testing.T, store *Store, docs ...model.SessionDoc) {
	t.Helper()
	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()
	for _, d := range docs {
		if err := r.Put(ctx, d); err != nil {
			t.Fatalf("Put(%s): %v", d.Session.SessionID, err)
		}
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// --- U1 ----------------------------------------------------------------

func TestRootReturnsConfiguredRoot(t *testing.T) {
	store := New(afero.NewMemMapFs(), "/store/root", nil)
	if got := store.Root(); got != "/store/root" {
		t.Errorf("Root() = %q, want %q", got, "/store/root")
	}
}

// --- U2 ----------------------------------------------------------------

func TestCommitWritesDocsAtExpectedPaths(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	claudeDoc := doc(model.VendorClaude, "aaa-uuid", "hello claude")
	codexDoc := doc(model.VendorCodex, "bbb-uuid", "hello codex")

	commitDocs(t, store, claudeDoc, codexDoc)

	sessionsRoot := filepath.Join(store.Root(), sessionsDir)
	wantPaths := []string{"claude/aaa-uuid.json", "codex/bbb-uuid.json"}
	for _, p := range wantPaths {
		exists, err := afero.Exists(fsys, filepath.Join(sessionsRoot, p))
		if err != nil {
			t.Fatalf("checking %s: %v", p, err)
		}
		if !exists {
			t.Errorf("expected file %s to exist", p)
		}
	}

	got := readSessionDocs(t, fsys, sessionsRoot)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2: %v", len(got), sortedKeys(got))
	}
	if gotDoc, ok := got["claude/aaa-uuid.json"]; !ok || !sessionDocsEqual(gotDoc, claudeDoc) {
		t.Errorf("claude doc mismatch: got %+v, want %+v", gotDoc, claudeDoc)
	}
	if gotDoc, ok := got["codex/bbb-uuid.json"]; !ok || !sessionDocsEqual(gotDoc, codexDoc) {
		t.Errorf("codex doc mismatch: got %+v, want %+v", gotDoc, codexDoc)
	}
}

// --- U3 ----------------------------------------------------------------

func TestCommittedFileIsSingleLineJSON(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	d := doc(model.VendorClaude, "aaa-uuid", "html <b>bold</b> & stuff")
	commitDocs(t, store, d)

	path := filepath.Join(store.Root(), sessionsDir, "claude", "aaa-uuid.json")
	data, err := afero.ReadFile(fsys, path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	if n := bytes.Count(data, []byte("\n")); n != 1 {
		t.Errorf("expected exactly one newline, got %d in %q", n, data)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Errorf("expected trailing newline, got %q", data)
	}
	if idx := bytes.IndexByte(data, '\n'); idx != len(data)-1 {
		t.Errorf("newline not at EOF: found at %d, len %d", idx, len(data))
	}
	if !json.Valid(data) {
		t.Errorf("committed file is not valid JSON: %q", data)
	}
	if !bytes.Contains(data, []byte("<b>bold</b> & stuff")) {
		t.Errorf("expected unescaped HTML to survive verbatim, got %q", data)
	}
}

// --- U4 ----------------------------------------------------------------

func TestPutSameSessionIDReplaces(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	first := doc(model.VendorClaude, "aaa-uuid", "first")
	second := doc(model.VendorClaude, "aaa-uuid", "second")
	if err := r.Put(ctx, first); err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	if err := r.Put(ctx, second); err != nil {
		t.Fatalf("Put(second): %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	sessionsRoot := filepath.Join(store.Root(), sessionsDir)
	got := readSessionDocs(t, fsys, sessionsRoot)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1: %v", len(got), sortedKeys(got))
	}
	gotDoc := got["claude/aaa-uuid.json"]
	if !sessionDocsEqual(gotDoc, second) {
		t.Errorf("expected second Put to win: got %+v, want %+v", gotDoc, second)
	}
}

// --- U5 ----------------------------------------------------------------

func TestCommitReplacesPreviousGenerationWholesale(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	docA := doc(model.VendorClaude, "aaa-uuid", "gen1-a")
	docB1 := doc(model.VendorClaude, "bbb-uuid", "gen1-b")
	commitDocs(t, store, docA, docB1)

	docB2 := doc(model.VendorClaude, "bbb-uuid", "gen2-b")
	commitDocs(t, store, docB2)

	sessionsRoot := filepath.Join(store.Root(), sessionsDir)
	got := readSessionDocs(t, fsys, sessionsRoot)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1: %v", len(got), sortedKeys(got))
	}
	gotDoc, ok := got["claude/bbb-uuid.json"]
	if !ok {
		t.Fatalf("expected claude/bbb-uuid.json to be present")
	}
	if !sessionDocsEqual(gotDoc, docB2) {
		t.Errorf("gen2 doc mismatch: got %+v, want %+v", gotDoc, docB2)
	}
	if _, stillThere := got["claude/aaa-uuid.json"]; stillThere {
		t.Errorf("gen1-only doc aaa-uuid should be gone after gen2 commit")
	}
}

// --- U6 ----------------------------------------------------------------

func TestDiscardLeavesPreviousStoreIntact(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	commitDocs(t, store, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	before := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))

	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r.Put(ctx, doc(model.VendorClaude, "ccc-uuid", "gen2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	after := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)

	rootEntries := dirEntryNames(t, fsys, store.Root())
	if hasPrefixEntry(rootEntries, stagingPrefix) {
		t.Errorf("expected no leftover staging dir after Discard, got entries %v", rootEntries)
	}
}

// --- U7 ----------------------------------------------------------------

func TestDiscardAfterCommitIsNoOp(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	before := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))

	if err := r.Discard(); err != nil {
		t.Errorf("Discard after Commit should be a no-op, got error: %v", err)
	}

	after := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)
}

func TestDiscardTwice(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r.Discard(); err != nil {
		t.Fatalf("first Discard: %v", err)
	}
	if err := r.Discard(); err != nil {
		t.Errorf("second Discard should be a no-op, got error: %v", err)
	}
}

func TestDeferDiscardAfterCommitIsSafe(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	func() {
		r, err := store.BeginRebuild(ctx)
		if err != nil {
			t.Fatalf("BeginRebuild: %v", err)
		}
		defer r.Discard()
		if err := r.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := r.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}()

	sessionsRoot := filepath.Join(store.Root(), sessionsDir)
	got := readSessionDocs(t, fsys, sessionsRoot)
	if len(got) != 1 {
		t.Fatalf("expected the committed doc to survive the deferred Discard, got %d docs: %v", len(got), sortedKeys(got))
	}
}

// --- U8 ----------------------------------------------------------------

func TestPutAndCommitAfterFinish(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	before := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))

	if err := r.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "should not land")); !errors.Is(err, ports.ErrRebuildFinished) {
		t.Errorf("Put after finish: got %v, want errors.Is(_, ports.ErrRebuildFinished)", err)
	}
	if err := r.Commit(ctx); !errors.Is(err, ports.ErrRebuildFinished) {
		t.Errorf("Commit after finish: got %v, want errors.Is(_, ports.ErrRebuildFinished)", err)
	}

	after := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)
}

// --- U9 ----------------------------------------------------------------

func TestPutRejectsZeroDocAndNonNamespacedID(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	cases := []model.SessionDoc{
		{}, // zero value
		{Session: model.Session{Vendor: model.VendorClaude, SessionID: "not-namespaced"}},
	}
	for _, d := range cases {
		if err := r.Put(ctx, d); !errors.Is(err, ErrInvalidSessionDoc) {
			t.Errorf("Put(%+v): got %v, want errors.Is(_, ErrInvalidSessionDoc)", d, err)
		}
	}

	stagingEntries := dirEntryNames(t, fsys, r.(*rebuild).staging)
	if len(stagingEntries) != 0 {
		t.Errorf("expected no files written for invalid docs, found entries %v", stagingEntries)
	}
}

// --- U11 -----------------------------------------------------------------

func TestFsFailureMidWriteLeavesPreviousStoreIntact(t *testing.T) {
	base := afero.NewMemMapFs()

	plainStore := New(base, "/store", nil)
	commitDocs(t, plainStore,
		doc(model.VendorClaude, "aaa-uuid", "gen1-a"),
		doc(model.VendorClaude, "bbb-uuid", "gen1-b"),
	)
	before := snapshotBytes(t, base, filepath.Join(plainStore.Root(), sessionsDir))

	failing := &failFs{
		Fs: base,
		OpenFileErr: func(name string, flag int, perm os.FileMode) error {
			if strings.Contains(name, "bbb-uuid.json") {
				return errors.New("injected: open failed")
			}
			return nil
		},
	}
	failStore := New(failing, "/store", nil)

	ctx := context.Background()
	r, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen2-a")); err != nil {
		t.Fatalf("Put(A) should succeed, got: %v", err)
	}
	if err := r.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2-b")); err == nil {
		t.Fatalf("Put(B) should have failed via the injected fault")
	}

	if err := r.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	after := snapshotBytes(t, base, filepath.Join(plainStore.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)

	rootEntries := dirEntryNames(t, base, plainStore.Root())
	if hasPrefixEntry(rootEntries, stagingPrefix) {
		t.Errorf("expected no leftover staging dir, got entries %v", rootEntries)
	}
}

// --- U12 -----------------------------------------------------------------

func TestMarshalFailureMidWriteLeavesPreviousStoreIntact(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	commitDocs(t, store, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	before := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))

	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2")); err != nil {
		t.Fatalf("Put(good doc): %v", err)
	}

	badDoc := doc(model.VendorClaude, "ccc-uuid", "gen2-bad")
	// A non-nil, zero-length json.RawMessage fails to marshal at all
	// (model.go's documented warning): []byte{} is non-nil, unlike a bare
	// nil json.RawMessage.
	badDoc.Messages[0].Raw = json.RawMessage([]byte{})
	if err := r.Put(ctx, badDoc); err == nil {
		t.Fatalf("Put(bad doc) should have failed to marshal")
	}

	if err := r.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	after := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)
}

// --- U12b ------------------------------------------------------------------

// TestCommitAfterFailedPutRefusesToSwap pins the B1 fix: a Commit that
// follows a failed Put must not swap in a generation known to be missing a
// doc. Before the fix, Commit had no memory of the Put failure and happily
// replaced a good previous generation with a truncated one.
func TestCommitAfterFailedPutRefusesToSwap(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	commitDocs(t, store, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	before := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))

	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	badDoc := doc(model.VendorClaude, "bbb-uuid", "gen2-bad")
	// Same non-nil, zero-length json.RawMessage marshal failure as U12,
	// but here the point is what Commit does afterward, not what Discard
	// leaves behind.
	badDoc.Messages[0].Raw = json.RawMessage([]byte{})
	if err := r.Put(ctx, badDoc); err == nil {
		t.Fatalf("Put(bad doc) should have failed to marshal")
	}

	if err := r.Commit(ctx); err == nil {
		t.Fatalf("Commit after a failed Put should refuse to swap, got nil error")
	}

	after := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)
}

// --- U13 -----------------------------------------------------------------

func TestCommitRenameFailureRollsBack(t *testing.T) {
	base := afero.NewMemMapFs()

	plainStore := New(base, "/store", nil)
	commitDocs(t, plainStore, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	before := snapshotBytes(t, base, filepath.Join(plainStore.Root(), sessionsDir))

	liveDir := filepath.Join("/store", sessionsDir)
	failing := &failFs{
		Fs: base,
		RenameErr: func(oldname, newname string) error {
			if strings.HasPrefix(filepath.Base(oldname), stagingPrefix) && newname == liveDir {
				return errors.New("injected: rename staging -> sessions failed")
			}
			return nil
		},
	}
	failStore := New(failing, "/store", nil)

	ctx := context.Background()
	r, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err == nil {
		t.Fatalf("Commit should have failed via the injected rename fault")
	}

	// The rebuild must not be finished: a failed Commit leaves it open so
	// the deferred Discard still cleans up the staging tree.
	if r.(*rebuild).done {
		t.Errorf("rebuild.done should still be false after a failed Commit")
	}

	after := snapshotBytes(t, base, filepath.Join(plainStore.Root(), sessionsDir))
	assertSnapshotsEqual(t, after, before)

	if err := r.Discard(); err != nil {
		t.Fatalf("Discard after failed Commit: %v", err)
	}
	rootEntries := dirEntryNames(t, base, plainStore.Root())
	if hasPrefixEntry(rootEntries, stagingPrefix) {
		t.Errorf("expected no leftover staging dir after Discard, got entries %v", rootEntries)
	}
	if hasPrefixEntry(rootEntries, trashPrefix) {
		t.Errorf("expected the rollback to leave no trash dir, got entries %v", rootEntries)
	}
}

// TestCommitRollbackFailureRelocatesToOrphanPrefix pins the A6
// rollback-failure fix: when both the swap rename and the rollback rename
// fail, "sessions/" is left absent, and the previous generation must not be
// left at a trashPrefix path — sweep() would delete exactly that path on
// the very next BeginRebuild, expiring the recovery pointer the returned
// error names. It must instead be relocated to an orphanPrefix path that
// sweep leaves alone.
func TestCommitRollbackFailureRelocatesToOrphanPrefix(t *testing.T) {
	base := afero.NewMemMapFs()

	plainStore := New(base, "/store", nil)
	commitDocs(t, plainStore, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	gen1Bytes := snapshotBytes(t, base, filepath.Join(plainStore.Root(), sessionsDir))

	liveDir := filepath.Join("/store", sessionsDir)
	failing := &failFs{
		Fs: base,
		RenameErr: func(oldname, newname string) error {
			if newname != liveDir {
				return nil
			}
			switch {
			case strings.HasPrefix(filepath.Base(oldname), stagingPrefix):
				return errors.New("injected: rename staging -> sessions failed")
			case strings.HasPrefix(filepath.Base(oldname), trashPrefix):
				return errors.New("injected: rollback rename trash -> sessions failed")
			default:
				return nil
			}
		},
	}
	failStore := New(failing, "/store", nil)

	ctx := context.Background()
	r, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	commitErr := r.Commit(ctx)
	if commitErr == nil {
		t.Fatalf("Commit should have failed via both injected rename faults")
	}
	if !strings.Contains(commitErr.Error(), orphanPrefix) {
		t.Errorf("Commit error should name the orphan recovery path, got: %v", commitErr)
	}

	rootEntries := dirEntryNames(t, base, plainStore.Root())
	if hasPrefixEntry(rootEntries, trashPrefix) {
		t.Errorf("expected the previous generation to be relocated out of the trash prefix, got entries %v", rootEntries)
	}
	var orphanDir string
	for _, name := range rootEntries {
		if strings.HasPrefix(name, orphanPrefix) {
			orphanDir = filepath.Join(plainStore.Root(), name)
		}
	}
	if orphanDir == "" {
		t.Fatalf("expected an orphan-prefixed directory, got entries %v", rootEntries)
	}
	orphanBytes := snapshotBytes(t, base, orphanDir)
	assertSnapshotsEqual(t, orphanBytes, gen1Bytes)

	// The orphan directory must survive a subsequent BeginRebuild's sweep —
	// that is the entire point of relocating out of the trash prefix.
	r2, err := plainStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild after orphaning: %v", err)
	}
	defer r2.Discard()
	exists, err := afero.DirExists(base, orphanDir)
	if err != nil {
		t.Fatalf("checking orphan dir exists: %v", err)
	}
	if !exists {
		t.Errorf("expected the orphan directory to survive a subsequent BeginRebuild's sweep")
	}
}

// TestCommitMovesPreviousGenerationAsideBeforeAttemptingTheSwap kills the
// mutation where Commit's first rename (live -> trash) is deleted entirely,
// collapsing the two-rename swap into one clobbering rename. On MemMapFs a
// rename onto an existing directory silently succeeds, so the one-rename
// and two-rename designs are observationally identical to every other test
// in this file — that mutation passed the whole unit suite even though the
// integration tier (a real filesystem, where the clobbering rename fails
// with ENOTEMPTY/EEXIST) catches it immediately. This test instead asserts
// the aside-move itself as an outcome: by the moment the second rename is
// attempted, the previous generation's bytes must already be readable back
// at the trash path. Delete the first rename and nothing is ever written to
// a trashPrefix directory, so this snapshot comes back empty and the
// comparison below fails.
func TestCommitMovesPreviousGenerationAsideBeforeAttemptingTheSwap(t *testing.T) {
	base := afero.NewMemMapFs()

	plainStore := New(base, "/store", nil)
	commitDocs(t, plainStore, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	gen1Bytes := snapshotBytes(t, base, filepath.Join(plainStore.Root(), sessionsDir))

	liveDir := filepath.Join("/store", sessionsDir)
	var trashSnapshotAtSecondRename map[string][]byte
	secondRenameSeen := false

	failing := &failFs{
		Fs: base,
		RenameErr: func(oldname, newname string) error {
			if !strings.HasPrefix(filepath.Base(oldname), stagingPrefix) || newname != liveDir {
				return nil
			}
			secondRenameSeen = true
			trashName := trashPrefix + strings.TrimPrefix(filepath.Base(oldname), stagingPrefix)
			trashSnapshotAtSecondRename = snapshotBytes(t, base, filepath.Join("/store", trashName))
			return errors.New("injected: rename staging -> sessions failed")
		},
	}
	failStore := New(failing, "/store", nil)

	ctx := context.Background()
	r, err := failStore.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(ctx, doc(model.VendorClaude, "bbb-uuid", "gen2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err == nil {
		t.Fatalf("Commit should have failed via the injected rename fault")
	}

	if !secondRenameSeen {
		t.Fatalf("the second rename (staging -> sessions) was never attempted")
	}
	assertSnapshotsEqual(t, trashSnapshotAtSecondRename, gen1Bytes)
}

// --- U14 -----------------------------------------------------------------

func TestLeftoverStagingAndTrashSweptOnBeginRebuild(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	commitDocs(t, store, doc(model.VendorClaude, "aaa-uuid", "gen1"))
	liveBefore := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))

	staleStaging := filepath.Join(store.Root(), stagingPrefix+"stale")
	staleTrash := filepath.Join(store.Root(), trashPrefix+"stale")
	// MemMapFs creates missing parents implicitly on WriteFile; a real
	// filesystem does not, so these MkdirAlls are what keeps this setup
	// portable to afero.NewOsFs().
	if err := fsys.MkdirAll(filepath.Join(staleStaging, "claude"), dirMode); err != nil {
		t.Fatalf("creating stale staging dir: %v", err)
	}
	if err := fsys.MkdirAll(filepath.Join(staleTrash, "claude"), dirMode); err != nil {
		t.Fatalf("creating stale trash dir: %v", err)
	}
	if err := afero.WriteFile(fsys, filepath.Join(staleStaging, "claude", "leftover.json"), []byte("{}\n"), fileMode); err != nil {
		t.Fatalf("seeding stale staging dir: %v", err)
	}
	if err := afero.WriteFile(fsys, filepath.Join(staleTrash, "claude", "leftover.json"), []byte("{}\n"), fileMode); err != nil {
		t.Fatalf("seeding stale trash dir: %v", err)
	}

	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	for _, p := range []string{staleStaging, staleTrash} {
		exists, err := afero.DirExists(fsys, p)
		if err != nil {
			t.Fatalf("checking %s: %v", p, err)
		}
		if exists {
			t.Errorf("expected leftover %s to be swept by BeginRebuild", p)
		}
	}

	liveAfter := snapshotBytes(t, fsys, filepath.Join(store.Root(), sessionsDir))
	assertSnapshotsEqual(t, liveAfter, liveBefore)
}

// --- U15 -----------------------------------------------------------------

func TestBeginRebuildCreatesMissingRoot(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/does/not/exist", nil)

	exists, err := afero.DirExists(fsys, store.Root())
	if err != nil {
		t.Fatalf("checking root exists: %v", err)
	}
	if exists {
		t.Fatalf("test setup: root should not exist yet")
	}

	r, err := store.BeginRebuild(context.Background())
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	exists, err = afero.DirExists(fsys, store.Root())
	if err != nil {
		t.Fatalf("checking root exists: %v", err)
	}
	if !exists {
		t.Errorf("expected BeginRebuild to create the missing store root")
	}
}

func TestCommitWithNoPutsYieldsEmptySessionsDir(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)
	ctx := context.Background()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit with zero Puts: %v", err)
	}

	sessionsRoot := filepath.Join(store.Root(), sessionsDir)
	exists, err := afero.DirExists(fsys, sessionsRoot)
	if err != nil {
		t.Fatalf("checking sessions dir: %v", err)
	}
	if !exists {
		t.Fatalf("expected an empty sessions dir to be committed")
	}
	entries := dirEntryNames(t, fsys, sessionsRoot)
	if len(entries) != 0 {
		t.Errorf("expected an empty sessions dir, got entries %v", entries)
	}
}

// --- U16 -----------------------------------------------------------------

func TestCancelledContext(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.BeginRebuild(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("BeginRebuild(cancelled): got %v, want errors.Is(_, context.Canceled)", err)
	}

	r, err := store.BeginRebuild(context.Background())
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(cancelled, doc(model.VendorClaude, "aaa-uuid", "x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put(cancelled): got %v, want errors.Is(_, context.Canceled)", err)
	}
	if err := r.Commit(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("Commit(cancelled): got %v, want errors.Is(_, context.Canceled)", err)
	}
}

// --- U17 -----------------------------------------------------------------

func TestCommitFailsWhenSessionsIsNotADirectory(t *testing.T) {
	fsys := afero.NewMemMapFs()
	store := New(fsys, "/store", nil)

	sessionsPath := filepath.Join(store.Root(), sessionsDir)
	// MemMapFs creates missing parents implicitly on WriteFile; a real
	// filesystem does not, so this MkdirAll is what keeps this setup
	// portable to afero.NewOsFs().
	if err := fsys.MkdirAll(store.Root(), dirMode); err != nil {
		t.Fatalf("creating store root: %v", err)
	}
	if err := afero.WriteFile(fsys, sessionsPath, []byte("not a directory"), fileMode); err != nil {
		t.Fatalf("seeding sessions-as-a-file: %v", err)
	}

	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	defer r.Discard()

	if err := r.Put(ctx, doc(model.VendorClaude, "aaa-uuid", "gen1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err == nil {
		t.Fatalf("Commit should fail when sessions exists and is not a directory")
	}

	data, err := afero.ReadFile(fsys, sessionsPath)
	if err != nil {
		t.Fatalf("reading %s after failed Commit: %v", sessionsPath, err)
	}
	if string(data) != "not a directory" {
		t.Errorf("expected the non-directory sessions path to be untouched, got %q", data)
	}
}
