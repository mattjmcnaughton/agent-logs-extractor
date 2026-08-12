package fakes

import (
	"context"
	"errors"
	"reflect"
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
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:one"}}); err != nil {
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
	if err := r2.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:two"}}); err != nil {
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
	if err := r0.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:live"}}); err != nil {
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
	if err := r1.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:discarded"}}); err != nil {
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
	if err := r2.Put(ctx, model.SessionDoc{Session: model.Session{SessionID: "claude:failed"}}); err != nil {
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

	doc := model.SessionDoc{Session: model.Session{SessionID: "claude:one"}}
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
	if got := exp.LastRequest(); !reflect.DeepEqual(got, second) {
		t.Errorf("LastRequest() = %+v, want %+v", got, second)
	}
}
