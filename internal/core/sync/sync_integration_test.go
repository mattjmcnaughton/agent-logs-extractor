//go:build integration

package sync_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/jsonlstore"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// These tests exercise the Sync use case against the real claudesource and
// jsonlstore adapters, on a real filesystem (afero.NewOsFs()), reading the
// committed vendor log fixtures. They are the use-case-level counterpart to
// claudesource's golden tests and jsonlstore's own integration tier: no
// fake in this file, so a mismatch between what claudesource actually
// produces and what jsonlstore actually accepts cannot hide behind either
// package's own unit tests.

func newSync(t *testing.T, storeRoot string) (*sync.Sync, *jsonlstore.Store) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	store := jsonlstore.New(afero.NewOsFs(), storeRoot, log)
	src := claudesource.New(log)
	s := sync.New([]ports.ConversationSource{src}, store, log)
	return s, store
}

func sessionsFiles(t *testing.T, storeRoot string) []string {
	t.Helper()
	sessionsDir := filepath.Join(storeRoot, "sessions")
	var files []string
	err := filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", sessionsDir, err)
	}
	return files
}

func leftovers(t *testing.T, storeRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(storeRoot)
	if err != nil {
		t.Fatalf("reading %s: %v", storeRoot, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".staging-") || strings.HasPrefix(name, ".trash-") || strings.HasPrefix(name, ".orphan-") {
			out = append(out, name)
		}
	}
	return out
}

func readSessionDoc(t *testing.T, path string) model.SessionDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc model.SessionDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return doc
}

// --- I1 --------------------------------------------------------------------

func TestSyncOverTheClaudeFixtureTree(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)

	summary, err := s.Run(context.Background(), sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorClaude, Root: logfixture.ClaudeRoot()},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := sync.Summary{Vendors: []sync.VendorSummary{{
		Vendor:    model.VendorClaude,
		Sessions:  3,
		Messages:  15,
		ToolCalls: 4,
		Skipped:   model.SkipCounts{model.SkipBookkeeping: 23},
	}}}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}

	files := sessionsFiles(t, storeRoot)
	wantFiles := []string{
		filepath.Join(storeRoot, "sessions", "claude", "40def079-df87-46bd-ac03-5ffbf1a74ca2.json"),
		filepath.Join(storeRoot, "sessions", "claude", "8f1900e4-5b83-46c2-a244-2811996f87c3.json"),
		filepath.Join(storeRoot, "sessions", "claude", "94ba8eae-3476-51cf-a4d4-b0b5339db735.json"),
	}
	gotSet := map[string]bool{}
	for _, f := range files {
		gotSet[f] = true
	}
	if len(files) != len(wantFiles) {
		t.Fatalf("store files = %v, want exactly %v", files, wantFiles)
	}
	for _, f := range wantFiles {
		if !gotSet[f] {
			t.Errorf("missing expected store file %s (got %v)", f, files)
		}
	}

	if got := leftovers(t, storeRoot); len(got) != 0 {
		t.Errorf("leftover staging/trash/orphan directories after a clean sync: %v", got)
	}

	wantCounts := map[string][2]int{
		"40def079-df87-46bd-ac03-5ffbf1a74ca2.json": {7, 2},
		"8f1900e4-5b83-46c2-a244-2811996f87c3.json": {4, 1},
		"94ba8eae-3476-51cf-a4d4-b0b5339db735.json": {4, 1},
	}
	for _, f := range wantFiles {
		doc := readSessionDoc(t, f)
		base := filepath.Base(f)
		want := wantCounts[base]
		if len(doc.Messages) != want[0] || len(doc.ToolCalls) != want[1] {
			t.Errorf("%s: (messages, tool_calls) = (%d, %d), want %v", base, len(doc.Messages), len(doc.ToolCalls), want)
		}
		if doc.Session.Vendor != model.VendorClaude {
			t.Errorf("%s: session.vendor = %q, want %q", base, doc.Session.Vendor, model.VendorClaude)
		}
		stem := strings.TrimSuffix(base, ".json")
		if want := "claude:" + stem; doc.Session.SessionID != want {
			t.Errorf("%s: session_id = %q, want %q", base, doc.Session.SessionID, want)
		}
	}
}

// --- I2 --------------------------------------------------------------------

func TestSyncIsIdempotent(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)
	ctx := context.Background()
	req := sync.Request{Sources: []sync.SourceRequest{{Vendor: model.VendorClaude, Root: logfixture.ClaudeRoot()}}}

	summary1, err := s.Run(ctx, req)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	snapshot1 := readFileMap(t, filepath.Join(storeRoot, "sessions"))
	// Guards against vacuous pass: two empty snapshots also compare equal,
	// so without this the whole test would stay green even if every Put in
	// ingestVendor were skipped.
	if len(snapshot1) != 3 {
		t.Fatalf("first run produced %d store files, want 3", len(snapshot1))
	}

	summary2, err := s.Run(ctx, req)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	snapshot2 := readFileMap(t, filepath.Join(storeRoot, "sessions"))

	if !reflect.DeepEqual(summary2, summary1) {
		t.Errorf("second summary = %+v, want it to equal the first %+v", summary2, summary1)
	}
	if !reflect.DeepEqual(snapshot2, snapshot1) {
		t.Errorf("second run's on-disk store differs byte-for-byte from the first")
	}
	if got := leftovers(t, storeRoot); len(got) != 0 {
		t.Errorf("leftover staging/trash/orphan directories after two syncs: %v", got)
	}
}

func readFileMap(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
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

// --- I3 --------------------------------------------------------------------

func TestSyncOverThePathologicalFixtureTree(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)

	summary, err := s.Run(context.Background(), sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorClaude, Root: logfixture.PathologicalClaudeRoot()},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := sync.Summary{Vendors: []sync.VendorSummary{{
		Vendor:    model.VendorClaude,
		Sessions:  1,
		Messages:  4,
		ToolCalls: 1,
		Skipped: model.SkipCounts{
			model.SkipBookkeeping:       12,
			model.SkipMalformedLine:     1,
			model.SkipUnknownRecordType: 1,
		},
	}}}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if got := summary.Vendors[0].TotalSkipped(); got != 14 {
		t.Errorf("TotalSkipped() = %d, want 14", got)
	}

	files := sessionsFiles(t, storeRoot)
	wantFile := filepath.Join(storeRoot, "sessions", "claude", "94ba8eae-3476-51cf-a4d4-b0b5339db735.json")
	if len(files) != 1 || files[0] != wantFile {
		t.Fatalf("store files = %v, want exactly [%s]", files, wantFile)
	}

	doc := readSessionDoc(t, wantFile)
	if len(doc.Messages) != 4 {
		t.Errorf("messages = %d, want 4", len(doc.Messages))
	}
	if len(doc.ToolCalls) != 1 {
		t.Errorf("tool_calls = %d, want 1", len(doc.ToolCalls))
	}
}

// --- I4 --------------------------------------------------------------------

func TestSyncWithAMissingVendorRootCommitsAnEmptyStore(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)

	summary, err := s.Run(context.Background(), sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorClaude, Root: filepath.Join(storeRoot, "does-not-exist")},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := sync.Summary{Vendors: []sync.VendorSummary{{Vendor: model.VendorClaude}}}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}

	sessionsDir := filepath.Join(storeRoot, "sessions")
	fi, err := os.Stat(sessionsDir)
	if err != nil {
		t.Fatalf("stat %s: %v", sessionsDir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s exists but is not a directory", sessionsDir)
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", sessionsDir, err)
	}
	if len(entries) != 0 {
		t.Errorf("%s is not empty: %v", sessionsDir, entries)
	}
}

// --- I5 --------------------------------------------------------------------

// TestSyncPreviousGenerationSurvivesAVendorRejectedBeforeRebuild proves that
// a run rejected before BeginRebuild even runs (a --vendor value with no
// registered source) leaves an earlier good generation on disk exactly as
// it was. This is a real but comparatively shallow guarantee: Run's own
// vendor-resolution loop returns before ever touching the store, so this
// case can never exercise the deferred r.Discard() at sync.go's Run — see
// TestSyncPreviousGenerationSurvivesAFailedCommit below for that.
func TestSyncPreviousGenerationSurvivesAVendorRejectedBeforeRebuild(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)
	ctx := context.Background()

	if _, err := s.Run(ctx, sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorClaude, Root: logfixture.ClaudeRoot()},
	}}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	before := readFileMap(t, filepath.Join(storeRoot, "sessions"))

	_, err := s.Run(ctx, sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorCodex, Root: logfixture.CodexRoot()},
	}})
	if err == nil {
		t.Fatal("second Run: want an error (codex has no registered source), got nil")
	}

	after := readFileMap(t, filepath.Join(storeRoot, "sessions"))
	if !reflect.DeepEqual(after, before) {
		t.Errorf("store changed after a failed run: before %v, after %v", before, after)
	}
	if got := leftovers(t, storeRoot); len(got) != 0 {
		t.Errorf("leftover staging/trash/orphan directories after a failed run: %v", got)
	}
}

// TestSyncPreviousGenerationSurvivesAFailedCommit proves the deferred
// r.Discard() at sync.go's Run: a failure that occurs AFTER BeginRebuild —
// every doc already Put into the staging tree — must still clean up the
// staging directory and must never touch the previously committed
// generation. It corrupts "sessions" into a regular file so
// jsonlstore.rebuild.statLiveIsDir errors inside Commit, which is the
// earliest point in the real adapter where a post-BeginRebuild failure can
// be forced deterministically without reaching into unexported state.
func TestSyncPreviousGenerationSurvivesAFailedCommit(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)
	ctx := context.Background()

	if _, err := s.Run(ctx, sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorClaude, Root: logfixture.ClaudeRoot()},
	}}); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	sessionsPath := filepath.Join(storeRoot, "sessions")
	if err := os.RemoveAll(sessionsPath); err != nil {
		t.Fatalf("removing %s: %v", sessionsPath, err)
	}
	if err := os.WriteFile(sessionsPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", sessionsPath, err)
	}

	_, err := s.Run(ctx, sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorClaude, Root: logfixture.ClaudeRoot()},
	}})
	if err == nil {
		t.Fatal("second Run: want an error (sessions is a regular file, not a directory), got nil")
	}
	// Pin *where* the failure happens, not just that one happened. This test
	// only covers the deferred Discard if the run gets past BeginRebuild and
	// every Put and then fails in Commit. If a future jsonlstore change moved
	// the not-a-directory check into BeginRebuild, an err-is-non-nil-only
	// assertion would still pass while silently covering nothing — which is
	// exactly how this test's predecessor was vacuous.
	if !strings.Contains(err.Error(), "committing store rebuild") {
		t.Fatalf("second Run: want the failure to come from Commit (so the deferred Discard is on the unwind path), got: %v", err)
	}

	// The killer assertion: without the deferred r.Discard() in sync.go's
	// Run, the staging directory this run wrote every doc into survives as
	// a leftover, because nothing else in the failure path removes it.
	if got := leftovers(t, storeRoot); len(got) != 0 {
		t.Errorf("leftover staging/trash/orphan directories after a failed commit: %v (defer r.Discard() should have cleaned up the staging tree)", got)
	}

	// statLiveIsDir errors before either rename in Commit runs, so the
	// corrupted "sessions" regular file must be exactly as this test left
	// it: proof the failed commit never touched it.
	got, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", sessionsPath, err)
	}
	if string(got) != "x" {
		t.Errorf("sessions path changed during a failed commit: %q, want unchanged %q", got, "x")
	}
}
