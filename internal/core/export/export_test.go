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

func TestExportRunIsNotImplementedAndAsksNoSinkForAnything(t *testing.T) {
	ctx := context.Background()

	store := fakes.NewCanonicalStore()
	exp := fakes.NewExporter("duckdb")

	log := slog.New(slog.DiscardHandler)
	uc := New(store, []ports.Exporter{exp}, log)

	err := uc.Run(ctx, Request{Sink: "duckdb", Out: "/out.duckdb"})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Run err = %v, want ErrNotImplemented", err)
	}
	if len(exp.Requests) != 0 {
		t.Errorf("exporter recorded %d requests, want 0", len(exp.Requests))
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
