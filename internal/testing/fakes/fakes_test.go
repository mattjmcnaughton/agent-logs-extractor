package fakes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

func TestFakeCanonicalStoreCommitReplacesTheLiveGeneration(t *testing.T) {
	ctx := context.Background()
	store := NewCanonicalStore()

	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:one"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, []string{"claude:one"}) {
		t.Fatalf("after first commit SessionIDs() = %v, want [claude:one]", got)
	}

	r2, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r2.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:two"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r2.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got := store.SessionIDs()
	want := []string{"claude:two"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after second commit SessionIDs() = %v, want %v (replacement, not merge)", got, want)
	}
}

func TestFakeCanonicalStoreDiscardAndFailedCommitLeaveTheLiveStoreIntact(t *testing.T) {
	ctx := context.Background()
	store := NewCanonicalStore()

	// Seed a committed generation.
	r0, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r0.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:live"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r0.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := []string{"claude:live"}

	// Discard should leave the store untouched.
	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:discarded"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("after Discard SessionIDs() = %v, want %v", got, want)
	}

	// A failed commit should also leave the store untouched.
	store.CommitErr = errors.New("boom")
	r2, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r2.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:failed"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r2.Commit(ctx); err == nil {
		t.Fatal("Commit: want error, got nil")
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("after failed Commit SessionIDs() = %v, want %v", got, want)
	}
}

func TestFakeConversationSourceListsSeededPathsAndUnknownRootYieldsNone(t *testing.T) {
	ctx := context.Background()
	src := NewConversationSource(model.VendorClaude)

	doc := model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:one"}}
	stats := model.ParseStats{Skipped: map[model.SkipReason]int{model.SkipMalformedLine: 1}}
	src.Seed("/root", "/root/b.jsonl", doc, stats)
	src.Seed("/root", "/root/a.jsonl", model.SessionDoc{}, model.ParseStats{})

	paths, err := src.List(ctx, "/root")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"/root/a.jsonl", "/root/b.jsonl"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("List(/root) = %v, want %v (sorted)", paths, want)
	}

	gotDoc, gotStats, err := src.Parse(ctx, "/root/b.jsonl")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(gotDoc, doc) {
		t.Errorf("Parse doc = %+v, want %+v", gotDoc, doc)
	}
	if !reflect.DeepEqual(gotStats, stats) {
		t.Errorf("Parse stats = %+v, want %+v", gotStats, stats)
	}

	unknown, err := src.List(ctx, "/unknown")
	if err != nil {
		t.Fatalf("List(/unknown): %v", err)
	}
	if unknown != nil {
		t.Errorf("List(/unknown) = %v, want nil", unknown)
	}
}

func TestFakeExporterRecordsEachRequest(t *testing.T) {
	ctx := context.Background()
	exp := NewExporter("duckdb")

	first := ports.ExportRequest{StoreRoot: "/store", Out: "/out1.duckdb"}
	second := ports.ExportRequest{StoreRoot: "/store", Out: "/out2.duckdb"}

	if err := exp.Export(ctx, first); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := exp.Export(ctx, second); err != nil {
		t.Fatalf("Export: %v", err)
	}

	if len(exp.Requests) != 2 {
		t.Fatalf("len(Requests) = %d, want 2", len(exp.Requests))
	}
	if got := exp.Requests[len(exp.Requests)-1]; !reflect.DeepEqual(got, second) {
		t.Errorf("Requests[len-1] = %+v, want %+v", got, second)
	}
}

func TestFakeStoreRebuildDiscardThenCommitErrorsAndLeavesTheLiveStoreIntact(t *testing.T) {
	ctx := context.Background()
	store := NewCanonicalStore()

	// Seed a committed generation.
	r0, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r0.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:live"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r0.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := []string{"claude:live"}

	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:pending"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r1.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	// Commit after Discard must error, not resurrect the discarded
	// pending generation over the live store.
	if err := r1.Commit(ctx); err == nil {
		t.Error("Commit after Discard: want error, got nil")
	}
	// Put after Discard must also error, not silently rebuild into a
	// generation nothing can observe.
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:sneaked-in"}}); err == nil {
		t.Error("Put after Discard: want error, got nil")
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("SessionIDs() = %v, want %v (live store untouched)", got, want)
	}
}

func TestFakeStoreRebuildPutAfterCommitDoesNotAliasIntoTheLiveStore(t *testing.T) {
	ctx := context.Background()
	store := NewCanonicalStore()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:a"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := []string{"claude:a"}

	// A Put on the finished rebuild must be rejected outright, so it
	// cannot sneak into the live store even if a future change reused the
	// same underlying map.
	if err := r.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:sneaked-in"}}); err == nil {
		t.Error("Put after Commit: want error, got nil")
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("SessionIDs() = %v, want %v (live store untouched)", got, want)
	}

	// The aliasing claim itself, not just the done guard the assertions
	// above already exercise: mutate the rebuild's pending map directly
	// (same package, so it is reachable even though Put is rejected once
	// done) and confirm the live store does not see it. Deleting
	// maps.Clone from Commit — swapping in the pending map itself instead
	// of a copy — would make this assertion fail while every assertion
	// above it still passes.
	fr, ok := r.(*FakeStoreRebuild)
	if !ok {
		t.Fatalf("r is %T, want *FakeStoreRebuild", r)
	}
	fr.pending["claude:mutated-directly"] = model.SessionDoc{Session: model.Session{SessionID: "claude:mutated-directly"}}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("SessionIDs() after mutating pending directly = %v, want %v (Commit must not alias into the live store)", got, want)
	}
}

func TestFakeStoreRebuildPutRejectsInvalidSessionDoc(t *testing.T) {
	ctx := context.Background()
	store := NewCanonicalStore()

	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}

	cases := []model.SessionDoc{
		{},
		{Session: model.Session{Vendor: model.VendorClaude, SessionID: "not-namespaced"}},
		{Session: model.Session{Vendor: model.VendorClaude, SessionID: "codex:wrong-vendor"}},
	}
	for _, d := range cases {
		if err := r.Put(ctx, d); !errors.Is(err, ErrInvalidSessionDoc) {
			t.Errorf("Put(%+v): got %v, want errors.Is(_, ErrInvalidSessionDoc)", d, err)
		}
	}
}

func TestFakeStoreRebuildCommitAfterFailedPutRefusesToSwap(t *testing.T) {
	ctx := context.Background()
	store := NewCanonicalStore()

	// Seed a committed generation.
	r0, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	if err := r0.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:live"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := r0.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := []string{"claude:live"}

	// A lenient caller that treats a Put failure as a counted-not-fatal
	// skip and proceeds to Commit anyway must still be refused: Commit
	// must not swap in a generation known to be missing a doc.
	r1, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("BeginRebuild: %v", err)
	}
	store.PutErrs["claude:boom"] = errors.New("boom")
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:boom"}}); err == nil {
		t.Fatalf("Put: want scripted error, got nil")
	}
	if err := r1.Commit(ctx); err == nil {
		t.Error("Commit after a failed Put: want error, got nil")
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("SessionIDs() after refused Commit = %v, want %v (live store untouched)", got, want)
	}
}

func TestFakeConversationSourceParsedRecordsTheSetOfPathsReadNotTheirOrder(t *testing.T) {
	ctx := context.Background()
	src := NewConversationSource(model.VendorClaude)
	src.Seed("/root", "/root/a.jsonl", model.SessionDoc{}, model.ParseStats{})
	src.Seed("/root", "/root/b.jsonl", model.SessionDoc{}, model.ParseStats{})

	// Parse out of sorted order deliberately: Parsed records an outcome
	// (which paths were read), not a call sequence, so the assertion
	// below sorts before comparing rather than checking order.
	if _, _, err := src.Parse(ctx, "/root/b.jsonl"); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, _, err := src.Parse(ctx, "/root/a.jsonl"); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got := slices.Clone(src.Parsed)
	slices.Sort(got)
	want := []string{"/root/a.jsonl", "/root/b.jsonl"}
	if !slices.Equal(got, want) {
		t.Errorf("Parsed (sorted) = %v, want %v", got, want)
	}
}
