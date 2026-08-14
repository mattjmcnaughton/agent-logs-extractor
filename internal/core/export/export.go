// Package export holds the Export use case: materialize the canonical store
// into one sink. The MVP ships one sink, duckdb; parquet/sqlite hang off
// the same Exporter port later.
package export

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// ErrUnknownSink is returned by Run when Request.Sink names no registered
// exporter. The error names every sink that *is* available, derived from
// sinkNames() rather than a hardcoded list, so it can never go stale as
// sinks are added.
var ErrUnknownSink = errors.New("export: unknown sink")

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

// New indexes the available sinks by name. Two exporters reporting the
// same Name() is a wiring mistake, not a valid configuration: the later
// one wins and is logged at warn, rather than silently dropping the
// earlier one. A nil log is normalized to a discard logger here, at the
// boundary, so e.log is always a total value: every later call site (Run,
// and any #9 adds) can call it directly with no nil check of its own.
func New(store ports.CanonicalStore, exporters []ports.Exporter, log *slog.Logger) *Export {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	indexed := make(map[string]ports.Exporter, len(exporters))
	for _, e := range exporters {
		name := e.Name()
		if _, dup := indexed[name]; dup {
			log.Warn("export: duplicate sink name registered; the later one wins", "sink", name)
		}
		indexed[name] = e
	}
	return &Export{store: store, exporters: indexed, log: log}
}

// Run materializes the store into the requested sink: look up the sink by
// name, guard the two inputs an Exporter cannot sensibly proceed without
// (an empty destination path, a store with no root), and delegate to the
// sink's Export. This is the entire use case (D7) — denormalization,
// script generation, and the temp-then-rename write all live in the
// exporter adapter, which is the only side of the port that can vary by
// sink.
func (e *Export) Run(ctx context.Context, req Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	exp, ok := e.exporters[req.Sink]
	if !ok {
		return fmt.Errorf("%w: %q (available: %s)", ErrUnknownSink, req.Sink, strings.Join(e.sinkNames(), ", "))
	}
	if req.Out == "" {
		return fmt.Errorf("export: destination path is empty")
	}
	root := e.store.Root()
	if root == "" {
		return fmt.Errorf("export: canonical store has no root")
	}

	e.log.Debug("export: running sink", "sink", req.Sink, "root", root, "out", req.Out)
	if err := exp.Export(ctx, ports.ExportRequest{StoreRoot: root, Out: req.Out}); err != nil {
		return fmt.Errorf("export: %s: %w", req.Sink, err)
	}
	return nil
}

// sinkNames is the sorted set of registered sink names, mirroring
// sync.Vendors()'s own sorted-set convention so ErrUnknownSink's message
// is deterministic across runs despite Go's randomized map iteration.
func (e *Export) sinkNames() []string {
	names := make([]string, 0, len(e.exporters))
	for name := range e.exporters {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
