package sync

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
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
