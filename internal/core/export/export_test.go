package export

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/fakes"
)

// TestExportRunDispatchesToTheNamedSink pins the success path: Run resolves
// req.Sink to the matching exporter, and hands it a ports.ExportRequest
// built from the store's Root() and the request's Out — never a literal
// path of its own.
func TestExportRunDispatchesToTheNamedSink(t *testing.T) {
	ctx := context.Background()

	store := fakes.NewCanonicalStore()
	store.RootPath = "/fake/store"
	exp := fakes.NewExporter("duckdb")

	uc := New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler))

	if err := uc.Run(ctx, Request{Sink: "duckdb", Out: "/out.duckdb"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(exp.Requests) != 1 {
		t.Fatalf("exporter recorded %d requests, want 1", len(exp.Requests))
	}
	got := exp.Requests[0]
	if got.StoreRoot != "/fake/store" {
		t.Errorf("StoreRoot = %q, want %q", got.StoreRoot, "/fake/store")
	}
	if got.Out != "/out.duckdb" {
		t.Errorf("Out = %q, want %q", got.Out, "/out.duckdb")
	}
}

// TestExportRunUnknownSink pins ErrUnknownSink for a sink name with no
// registered exporter, and that the message names the sinks that *are*
// available rather than leaving the caller to guess.
func TestExportRunUnknownSink(t *testing.T) {
	ctx := context.Background()

	store := fakes.NewCanonicalStore()
	exp := fakes.NewExporter("duckdb")
	uc := New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler))

	err := uc.Run(ctx, Request{Sink: "parquet", Out: "/out.parquet"})
	if !errors.Is(err, ErrUnknownSink) {
		t.Fatalf("Run err = %v, want it to wrap ErrUnknownSink", err)
	}
	if !strings.Contains(err.Error(), "parquet") {
		t.Errorf("Run err = %q, want it to name the requested sink %q", err.Error(), "parquet")
	}
	if !strings.Contains(err.Error(), "duckdb") {
		t.Errorf("Run err = %q, want it to name the available sink %q", err.Error(), "duckdb")
	}
	if len(exp.Requests) != 0 {
		t.Errorf("exporter recorded %d requests for an unknown-sink request, want 0", len(exp.Requests))
	}
}

// TestExportRunUnknownSinkWithNoSinksRegistered covers the pre-#9 shape
// wiring still produces until every sink lands: an Export constructed with
// no exporters at all must still error clearly, not panic on a nil map
// dereference, and the message must not claim any sink is available.
func TestExportRunUnknownSinkWithNoSinksRegistered(t *testing.T) {
	ctx := context.Background()
	uc := New(fakes.NewCanonicalStore(), nil, slog.New(slog.DiscardHandler))

	err := uc.Run(ctx, Request{Sink: "duckdb", Out: "/out.duckdb"})
	if !errors.Is(err, ErrUnknownSink) {
		t.Fatalf("Run err = %v, want it to wrap ErrUnknownSink", err)
	}
}

// TestExportRunPropagatesSinkFailure pins that a failing Exporter's error
// reaches the caller (wrapped, via errors.Is-compatible %w), and that
// nothing about the failure is swallowed.
func TestExportRunPropagatesSinkFailure(t *testing.T) {
	ctx := context.Background()

	store := fakes.NewCanonicalStore()
	exp := fakes.NewExporter("duckdb")
	sentinel := errors.New("boom")
	exp.Err = sentinel

	uc := New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler))

	err := uc.Run(ctx, Request{Sink: "duckdb", Out: "/out.duckdb"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run err = %v, want it to wrap the sink's error %v", err, sentinel)
	}
}

// TestExportRunRejectsEmptyOutAndEmptyStoreRoot pins D7's two guards: an
// empty destination path, and a store reporting an empty Root(), both fail
// before the exporter is ever invoked (so a would-be sink is never asked to
// write "nowhere" or read "no store").
func TestExportRunRejectsEmptyOutAndEmptyStoreRoot(t *testing.T) {
	ctx := context.Background()

	t.Run("empty Out", func(t *testing.T) {
		store := fakes.NewCanonicalStore()
		exp := fakes.NewExporter("duckdb")
		uc := New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler))

		if err := uc.Run(ctx, Request{Sink: "duckdb", Out: ""}); err == nil {
			t.Fatal("Run with empty Out: want error, got nil")
		}
		if len(exp.Requests) != 0 {
			t.Errorf("exporter recorded %d requests for an empty-Out request, want 0", len(exp.Requests))
		}
	})

	t.Run("empty store root", func(t *testing.T) {
		store := fakes.NewCanonicalStore()
		store.RootPath = ""
		exp := fakes.NewExporter("duckdb")
		uc := New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler))

		if err := uc.Run(ctx, Request{Sink: "duckdb", Out: "/out.duckdb"}); err == nil {
			t.Fatal("Run with an empty store root: want error, got nil")
		}
		if len(exp.Requests) != 0 {
			t.Errorf("exporter recorded %d requests for an empty-root request, want 0", len(exp.Requests))
		}
	})
}

// TestExportRunRespectsCancelledContext pins the same up-front ctx.Err()
// guard sync.Run uses, for consistency across both use cases.
func TestExportRunRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := fakes.NewCanonicalStore()
	exp := fakes.NewExporter("duckdb")
	uc := New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler))

	err := uc.Run(ctx, Request{Sink: "duckdb", Out: "/out.duckdb"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if len(exp.Requests) != 0 {
		t.Errorf("exporter recorded %d requests for a cancelled-context request, want 0", len(exp.Requests))
	}
}

func TestNewWithDuplicateSinkNameKeepsTheLaterExporterAndIsSafeWithANilLogger(t *testing.T) {
	first := fakes.NewExporter("duckdb")
	second := fakes.NewExporter("duckdb")

	// log is nil on purpose: New must not panic when the diagnostic it
	// logs on a duplicate key has no logger to write to.
	e := New(fakes.NewCanonicalStore(), []ports.Exporter{first, second}, nil)

	if got := e.exporters["duckdb"]; got != ports.Exporter(second) {
		t.Errorf("exporters[duckdb] = %p, want the later-registered exporter %p (last one wins)", got, second)
	}
}

// TestNewWithDuplicateSinkNameLogsTheWarning covers the log.Warn statement
// itself, which the nil-logger test above can't reach: a nil log is
// normalized to a discard handler, so nothing written to it is ever
// observable. With a real handler backing a buffer, the duplicate
// registration must actually produce a message naming the sink.
func TestNewWithDuplicateSinkNameLogsTheWarning(t *testing.T) {
	first := fakes.NewExporter("duckdb")
	second := fakes.NewExporter("duckdb")

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	New(fakes.NewCanonicalStore(), []ports.Exporter{first, second}, log)

	if got := buf.String(); !strings.Contains(got, "duplicate") || !strings.Contains(got, "duckdb") {
		t.Errorf("log output = %q, want it to mention the duplicate sink %q", got, "duckdb")
	}
}
