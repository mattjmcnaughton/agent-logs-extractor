package export

import (
	"context"
	"errors"
	"log/slog"
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
