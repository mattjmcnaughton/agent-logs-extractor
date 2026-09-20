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
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/testlogs"
)

// These tests exercise the Sync use case against the real claudesource and
// jsonlstore adapters, on a real filesystem (afero.NewOsFs()), reading the
// generated synthetic vendor examples. They are the use-case-level counterpart to
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
		{Vendor: model.VendorClaude, Root: testlogs.ClaudeRoot(t)},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := sync.Summary{Vendors: []sync.VendorSummary{{
		Vendor:    model.VendorClaude,
		Sessions:  3,
		Messages:  15,
		ToolCalls: 4,
		Skipped:   model.SkipCounts{model.SkipBookkeeping: 4},
	}}}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}

	files := sessionsFiles(t, storeRoot)
	wantFiles := []string{
		filepath.Join(storeRoot, "sessions", "claude", "parent.json"),
		filepath.Join(storeRoot, "sessions", "claude", "failure.json"),
		filepath.Join(storeRoot, "sessions", "claude", "single.json"),
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
		"parent.json":  {7, 2},
		"failure.json": {4, 1},
		"single.json":  {4, 1},
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
	req := sync.Request{Sources: []sync.SourceRequest{{Vendor: model.VendorClaude, Root: testlogs.ClaudeRoot(t)}}}

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
		{Vendor: model.VendorClaude, Root: testlogs.PathologicalClaudeRoot(t)},
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
			model.SkipBookkeeping:       3,
			model.SkipMalformedLine:     1,
			model.SkipUnknownRecordType: 1,
		},
	}}}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if got := summary.Vendors[0].TotalSkipped(); got != 5 {
		t.Errorf("TotalSkipped() = %d, want 5", got)
	}

	files := sessionsFiles(t, storeRoot)
	wantFile := filepath.Join(storeRoot, "sessions", "claude", "single.json")
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
		{Vendor: model.VendorClaude, Root: testlogs.ClaudeRoot(t)},
	}}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	before := readFileMap(t, filepath.Join(storeRoot, "sessions"))

	_, err := s.Run(ctx, sync.Request{Sources: []sync.SourceRequest{
		{Vendor: model.VendorCodex, Root: testlogs.CodexRoot(t)},
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

// rejectSwapFS fails the staged-generation rename after all parsing/writes.
// The rollback rename remains available, exercising Sync's deferred Discard.
type rejectSwapFS struct{ afero.Fs }

func (f rejectSwapFS) Rename(old, new string) error {
	if strings.HasPrefix(filepath.Base(old), ".staging-") && filepath.Base(new) == "sessions" {
		return os.ErrPermission
	}
	return f.Fs.Rename(old, new)
}

func TestSyncPreviousGenerationSurvivesAFailedCommit(t *testing.T) {
	storeRoot := t.TempDir()
	s, _ := newSync(t, storeRoot)
	ctx := context.Background()
	req := sync.Request{Sources: []sync.SourceRequest{{Vendor: model.VendorClaude, Root: testlogs.ClaudeRoot(t)}}}
	if _, err := s.Run(ctx, req); err != nil {
		t.Fatal(err)
	}
	before := readFileMap(t, filepath.Join(storeRoot, "sessions"))
	failingStore := jsonlstore.New(rejectSwapFS{afero.NewOsFs()}, storeRoot, nil)
	s = sync.New([]ports.ConversationSource{claudesource.New(nil)}, failingStore, nil)
	_, err := s.Run(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "committing store rebuild") {
		t.Fatalf("want Commit failure, got %v", err)
	}
	if got := leftovers(t, storeRoot); len(got) != 0 {
		t.Fatalf("deferred Discard left %v", got)
	}
	after := readFileMap(t, filepath.Join(storeRoot, "sessions"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed commit changed previous generation")
	}
}
