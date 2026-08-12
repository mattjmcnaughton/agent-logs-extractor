// Package sync holds the Sync use case: parse every vendor source and
// rebuild the canonical store in one atomic pass (TDD decision 6).
package sync

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// ErrNotImplemented is returned by Run until the sync slice lands (#8).
// Delete it, and the early return in Run, in that ticket.
var ErrNotImplemented = errors.New("sync: not implemented yet")

// SourceRequest names one vendor source to ingest and the root to read it
// from. Wiring supplies the default root; --claude-path / --codex-path
// override it.
type SourceRequest struct {
	Vendor model.Vendor
	Root   string
}

// Request is one `sync` invocation. Sources is the set selected by
// --vendor, already resolved to roots; an empty Sources ingests nothing.
type Request struct {
	Sources []SourceRequest
}

// VendorSummary is one vendor's contribution to a sync run.
type VendorSummary struct {
	Vendor    model.Vendor
	Sessions  int
	Messages  int
	ToolCalls int
	// Skipped counts skipped records by reason across the vendor's files;
	// nil means none. `sync` prints the total and logs the breakdown at
	// debug level.
	Skipped model.SkipCounts
}

// Summary is the ingest accounting one sync run reports (US-1).
type Summary struct {
	Vendors []VendorSummary
}

// Sync is the sync use case: source(s) in, canonical store out.
type Sync struct {
	sources map[model.Vendor]ports.ConversationSource
	store   ports.CanonicalStore
	log     *slog.Logger
}

// New indexes the available sources by vendor. A Request naming a vendor
// with no registered source is an error at Run time. Two sources reporting
// the same vendor is a wiring mistake, not a valid configuration: the
// later one wins and is logged at warn, rather than silently dropping the
// earlier one. A nil log is normalized to a discard logger here, at the
// boundary, so s.log is always a total value: every later call site (Run,
// and any #8 adds) can call it directly with no nil check of its own.
func New(sources []ports.ConversationSource, store ports.CanonicalStore, log *slog.Logger) *Sync {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	indexed := make(map[model.Vendor]ports.ConversationSource, len(sources))
	for _, s := range sources {
		vendor := s.Vendor()
		if _, dup := indexed[vendor]; dup {
			log.Warn("sync: duplicate vendor source registered; the later one wins", "vendor", vendor)
		}
		indexed[vendor] = s
	}
	return &Sync{sources: indexed, store: store, log: log}
}

// Run parses every requested source and rebuilds the store atomically,
// returning the ingest summary.
func (s *Sync) Run(ctx context.Context, req Request) (Summary, error) {
	return Summary{}, ErrNotImplemented
}
