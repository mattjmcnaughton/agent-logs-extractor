// Package export holds the Export use case: materialize the canonical store
// into one sink. The MVP ships one sink, duckdb; parquet/sqlite hang off
// the same Exporter port later.
package export

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// ErrNotImplemented is returned by Run until the export slice lands (#9).
// Delete it, and the early return in Run, in that ticket.
var ErrNotImplemented = errors.New("export: not implemented yet")

// Request is one `export <sink>` invocation.
type Request struct {
	// Sink is the sink to materialize into; it matches an injected
	// Exporter's Name().
	Sink string
	// Out is the destination path. Wiring supplies the default; --out
	// overrides it.
	Out string
}

// Export is the export use case.
type Export struct {
	store     ports.CanonicalStore
	exporters map[string]ports.Exporter
	log       *slog.Logger
}

// New indexes the available sinks by name.
func New(store ports.CanonicalStore, exporters []ports.Exporter, log *slog.Logger) *Export {
	indexed := make(map[string]ports.Exporter, len(exporters))
	for _, e := range exporters {
		indexed[e.Name()] = e
	}
	return &Export{store: store, exporters: indexed, log: log}
}

// Run materializes the store into the requested sink.
func (e *Export) Run(ctx context.Context, req Request) error {
	return ErrNotImplemented
}
