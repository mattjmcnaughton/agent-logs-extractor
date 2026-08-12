package sync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/fakes"
)

func TestSyncRunIsNotImplementedAndLeavesTheStoreIntact(t *testing.T) {
	ctx := context.Background()

	src := fakes.NewConversationSource(model.VendorClaude)
	src.Seed("/root", "/root/session.jsonl", model.SessionDoc{Session: model.Session{SessionID: "claude:one"}}, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:existing"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	wantIDs := store.SessionIDs()

	log := slog.New(slog.DiscardHandler)
	s := New([]ports.ConversationSource{src}, store, log)

	summary, err := s.Run(ctx, Request{
		Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}},
	})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Run err = %v, want ErrNotImplemented", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("Run summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (unchanged)", got, wantIDs)
	}
}

func TestNewWithDuplicateVendorKeepsTheLaterSourceAndIsSafeWithANilLogger(t *testing.T) {
	first := fakes.NewConversationSource(model.VendorClaude)
	second := fakes.NewConversationSource(model.VendorClaude)

	// log is nil on purpose: New must not panic when the diagnostic it
	// logs on a duplicate key has no logger to write to.
	s := New([]ports.ConversationSource{first, second}, fakes.NewCanonicalStore(), nil)

	if got := s.sources[model.VendorClaude]; got != ports.ConversationSource(second) {
		t.Errorf("sources[claude] = %p, want the later-registered source %p (last one wins)", got, second)
	}
}

// TestNewWithDuplicateVendorLogsTheWarning covers the log.Warn statement
// itself, which the nil-logger test above can't reach: a nil log is
// normalized to a discard handler, so nothing written to it is ever
// observable. With a real handler backing a buffer, the duplicate
// registration must actually produce a message naming the vendor.
func TestNewWithDuplicateVendorLogsTheWarning(t *testing.T) {
	first := fakes.NewConversationSource(model.VendorClaude)
	second := fakes.NewConversationSource(model.VendorClaude)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	New([]ports.ConversationSource{first, second}, fakes.NewCanonicalStore(), log)

	if got := buf.String(); !strings.Contains(got, "duplicate") || !strings.Contains(got, string(model.VendorClaude)) {
		t.Errorf("log output = %q, want it to mention the duplicate vendor %q", got, model.VendorClaude)
	}
}
