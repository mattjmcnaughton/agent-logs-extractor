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

// seedStore runs one full BeginRebuild/Put/Commit cycle against store, so
// tests can set up a "previously committed generation" to assert stays
// untouched (or gets replaced) by the Run under test.
func seedStore(t *testing.T, store *fakes.FakeCanonicalStore, docs ...model.SessionDoc) {
	t.Helper()
	ctx := context.Background()
	r, err := store.BeginRebuild(ctx)
	if err != nil {
		t.Fatalf("seedStore: BeginRebuild: %v", err)
	}
	for _, d := range docs {
		if err := r.Put(ctx, d); err != nil {
			t.Fatalf("seedStore: Put: %v", err)
		}
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatalf("seedStore: Commit: %v", err)
	}
}

func TestRunIngestsEverySourceAndCommitsOnce(t *testing.T) {
	ctx := context.Background()

	claudeSrc := fakes.NewConversationSource(model.VendorClaude)
	claudeSrc.Seed("/claude", "/claude/a.jsonl",
		model.SessionDoc{
			Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:a"},
			Messages: []model.Message{{}, {}},
		},
		model.ParseStats{},
	)

	codexSrc := fakes.NewConversationSource(model.VendorCodex)
	codexSrc.Seed("/codex", "/codex/b.jsonl",
		model.SessionDoc{
			Session:   model.Session{Vendor: model.VendorCodex, SessionID: "codex:b"},
			ToolCalls: []model.ToolCall{{}},
		},
		model.ParseStats{},
	)

	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:stale"}})

	s := New([]ports.ConversationSource{claudeSrc, codexSrc}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{
		{Vendor: model.VendorClaude, Root: "/claude"},
		{Vendor: model.VendorCodex, Root: "/codex"},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := Summary{Vendors: []VendorSummary{
		{Vendor: model.VendorClaude, Sessions: 1, Messages: 2},
		{Vendor: model.VendorCodex, Sessions: 1, ToolCalls: 1},
	}}
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("summary = %+v, want %+v", summary, want)
	}

	if got := store.SessionIDs(); !reflect.DeepEqual(got, []string{"claude:a", "codex:b"}) {
		t.Errorf("store SessionIDs() = %v, want [claude:a codex:b] (stale gone)", got)
	}
}

func TestRunSkipsUnstorableSessionDocsWithoutFailing(t *testing.T) {
	cases := []struct {
		name string
		doc  model.SessionDoc
	}{
		{name: "zero doc", doc: model.SessionDoc{}},
		{name: "empty session id with messages", doc: model.SessionDoc{
			Session:  model.Session{Vendor: model.VendorClaude, SessionID: ""},
			Messages: []model.Message{{}},
		}},
		{name: "unnamespaced id", doc: model.SessionDoc{
			Session:  model.Session{Vendor: model.VendorClaude, SessionID: "nope"},
			Messages: []model.Message{{}},
		}},
		{name: "bare prefix id", doc: model.SessionDoc{
			Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:"},
			Messages: []model.Message{{}},
		}},
		{name: "traversal vendor", doc: model.SessionDoc{
			Session:  model.Session{Vendor: model.Vendor(".."), SessionID: "..:x"},
			Messages: []model.Message{{}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			src := fakes.NewConversationSource(model.VendorClaude)
			src.Seed("/root", "/root/bad.jsonl", tc.doc, model.ParseStats{})
			good := model.SessionDoc{
				Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:good"},
				Messages: []model.Message{{}, {}},
			}
			src.Seed("/root", "/root/good.jsonl", good, model.ParseStats{})

			store := fakes.NewCanonicalStore()
			s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

			summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := Summary{Vendors: []VendorSummary{{Vendor: model.VendorClaude, Sessions: 1, Messages: 2}}}
			if !reflect.DeepEqual(summary, want) {
				t.Errorf("summary = %+v, want %+v (only the good sibling counted)", summary, want)
			}
			if got := store.SessionIDs(); !reflect.DeepEqual(got, []string{"claude:good"}) {
				t.Errorf("store SessionIDs() = %v, want only [claude:good]", got)
			}
		})
	}
}

func TestRunMergesSkipCountsAcrossFilesAndVendors(t *testing.T) {
	ctx := context.Background()

	claudeSrc := fakes.NewConversationSource(model.VendorClaude)
	claudeSrc.Seed("/claude", "/claude/a.jsonl", model.SessionDoc{}, model.ParseStats{Skipped: model.SkipCounts{model.SkipMalformedLine: 1}})
	claudeSrc.Seed("/claude", "/claude/b.jsonl", model.SessionDoc{}, model.ParseStats{Skipped: model.SkipCounts{model.SkipMalformedLine: 2, model.SkipUnknownRecordType: 1}})
	claudeSrc.Seed("/claude", "/claude/c.jsonl", model.SessionDoc{}, model.ParseStats{})

	codexSrc := fakes.NewConversationSource(model.VendorCodex)
	codexSrc.Seed("/codex", "/codex/x.jsonl", model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorCodex, SessionID: "codex:x"},
		Messages: []model.Message{{}},
	}, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	s := New([]ports.ConversationSource{claudeSrc, codexSrc}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{
		{Vendor: model.VendorClaude, Root: "/claude"},
		{Vendor: model.VendorCodex, Root: "/codex"},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	claudeSummary := summary.Vendors[0]
	wantSkipped := model.SkipCounts{model.SkipMalformedLine: 3, model.SkipUnknownRecordType: 1}
	if !reflect.DeepEqual(claudeSummary.Skipped, wantSkipped) {
		t.Errorf("claude Skipped = %v, want %v", claudeSummary.Skipped, wantSkipped)
	}
	if got := claudeSummary.TotalSkipped(); got != 4 {
		t.Errorf("claude TotalSkipped() = %d, want 4", got)
	}

	codexSummary := summary.Vendors[1]
	if codexSummary.Skipped != nil {
		t.Errorf("codex Skipped = %v, want nil", codexSummary.Skipped)
	}
}

func TestRunDeduplicatesSessionIDsInTheSummary(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	first := model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:dup"},
		Messages: []model.Message{{MessageID: "claude:1"}},
	}
	second := model.SessionDoc{
		Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:dup"},
		Messages: []model.Message{
			{MessageID: "claude:2"}, {MessageID: "claude:3"}, {MessageID: "claude:4"}, {MessageID: "claude:5"},
		},
	}
	// a.jsonl sorts before b.jsonl, so `first` is Put before `second`; the
	// later Put must win both in the store and in the summary counts.
	src.Seed("/root", "/root/a.jsonl", first, model.ParseStats{})
	src.Seed("/root", "/root/b.jsonl", second, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	vs := summary.Vendors[0]
	if vs.Sessions != 1 || vs.Messages != 4 {
		t.Errorf("vs = %+v, want Sessions=1 Messages=4 (deduplicated, later doc wins)", vs)
	}
	got := store.Committed["claude:dup"]
	if !reflect.DeepEqual(got, second) {
		t.Errorf("store holds %+v, want the second (later) doc %+v", got, second)
	}
}

func TestRunWithNoSessionsUnderARootReportsZero(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude) // never Seed()ed: List(root) reports (nil, nil), same as a missing root (US-7)
	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:stale"}})

	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/missing"}}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Summary{Vendors: []VendorSummary{{Vendor: model.VendorClaude}}}
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("summary = %+v, want %+v", summary, want)
	}
	// A missing/empty root still drives a full rebuild — it just rebuilds
	// to nothing, wiping whatever the previous generation held.
	if got := store.SessionIDs(); len(got) != 0 {
		t.Errorf("store SessionIDs() = %v, want empty", got)
	}
}

func TestRunErrorsWhenAVendorHasNoRegisteredSource(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:existing"}})
	wantIDs := store.SessionIDs()

	s := New(nil, store, slog.New(slog.DiscardHandler)) // no sources registered at all

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if !errors.Is(err, ErrNoSourceForVendor) {
		t.Fatalf("err = %v, want ErrNoSourceForVendor", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (untouched; resolution precedes BeginRebuild)", got, wantIDs)
	}
}

func TestRunListErrorAbortsAndLeavesTheStoreIntact(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	src.ListErrs["/root"] = errors.New("boom")

	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:existing"}})
	wantIDs := store.SessionIDs()

	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (untouched)", got, wantIDs)
	}
}

func TestRunParseErrorIsCountedAndDoesNotAbort(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	src.Seed("/root", "/root/bad.jsonl", model.SessionDoc{}, model.ParseStats{})
	src.ParseErrs["/root/bad.jsonl"] = errors.New("permission denied")

	good := model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:good"},
		Messages: []model.Message{{}},
	}
	src.Seed("/root", "/root/good.jsonl", good, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if err != nil {
		t.Fatalf("Run: %v (a Parse error must not abort the run)", err)
	}
	vs := summary.Vendors[0]
	if vs.FilesUnreadable != 1 {
		t.Errorf("FilesUnreadable = %d, want 1", vs.FilesUnreadable)
	}
	if vs.Sessions != 1 {
		t.Errorf("Sessions = %d, want 1 (the sibling still ingested)", vs.Sessions)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, []string{"claude:good"}) {
		t.Errorf("store SessionIDs() = %v, want [claude:good]", got)
	}
}

func TestRunPutErrorAbortsAndLeavesTheStoreIntact(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	doc := model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:bad-write"},
		Messages: []model.Message{{}},
	}
	src.Seed("/root", "/root/a.jsonl", doc, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:existing"}})
	wantIDs := store.SessionIDs()
	store.PutErrs["claude:bad-write"] = errors.New("disk full")

	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (untouched)", got, wantIDs)
	}
}

func TestRunBeginRebuildErrorIsReported(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	store := fakes.NewCanonicalStore()
	store.BeginErr = errors.New("store unavailable")

	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
}

func TestRunCommitErrorLeavesThePreviousGenerationIntact(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	doc := model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:new"},
		Messages: []model.Message{{}},
	}
	src.Seed("/root", "/root/a.jsonl", doc, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:existing"}})
	wantIDs := store.SessionIDs()
	store.CommitErr = errors.New("swap failed")

	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (untouched)", got, wantIDs)
	}
}

func TestRunWithACancelledContextDoesNotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := fakes.NewConversationSource(model.VendorClaude)
	doc := model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:new"},
		Messages: []model.Message{{}},
	}
	src.Seed("/root", "/root/a.jsonl", doc, model.ParseStats{})

	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:existing"}})
	wantIDs := store.SessionIDs()

	s := New([]ports.ConversationSource{src}, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (untouched; a cancelled context must never reach Commit)", got, wantIDs)
	}
}

func TestRunWithNoRequestedSourcesDoesNotTouchTheStore(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewCanonicalStore()
	seedStore(t, store, model.SessionDoc{Session: model.Session{Vendor: model.VendorClaude, SessionID: "claude:existing"}})
	wantIDs := store.SessionIDs()

	s := New(nil, store, slog.New(slog.DiscardHandler))

	summary, err := s.Run(ctx, Request{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Errorf("summary = %+v, want zero Summary", summary)
	}
	if got := store.SessionIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("store SessionIDs() = %v, want %v (untouched; an empty Request never rebuilds)", got, wantIDs)
	}
}

func TestVendorsReportsRegisteredSourcesSorted(t *testing.T) {
	codexSrc := fakes.NewConversationSource(model.VendorCodex)
	claudeSrc := fakes.NewConversationSource(model.VendorClaude)

	s := New([]ports.ConversationSource{codexSrc, claudeSrc}, fakes.NewCanonicalStore(), slog.New(slog.DiscardHandler))

	got := s.Vendors()
	want := []model.Vendor{model.VendorClaude, model.VendorCodex}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Vendors() = %v, want %v", got, want)
	}

	var nilSync *Sync
	if got := nilSync.Vendors(); got != nil {
		t.Errorf("(*Sync)(nil).Vendors() = %v, want nil", got)
	}
}

func TestRunLogsTheSkipBreakdownAtDebug(t *testing.T) {
	ctx := context.Background()
	src := fakes.NewConversationSource(model.VendorClaude)
	src.Seed("/root", "/root/a.jsonl", model.SessionDoc{}, model.ParseStats{Skipped: model.SkipCounts{model.SkipMalformedLine: 2}})

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	store := fakes.NewCanonicalStore()
	s := New([]ports.ConversationSource{src}, store, log)

	if _, err := s.Run(ctx, Request{Sources: []SourceRequest{{Vendor: model.VendorClaude, Root: "/root"}}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "malformed_line") || !strings.Contains(got, "count=2") {
		t.Errorf("log output = %q, want it to report the skip breakdown by reason", got)
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
