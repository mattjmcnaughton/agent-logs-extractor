// Package ports declares the interfaces the core depends on. Adapters in
// internal/adapters/ implement them; the core never sees a concrete
// implementation. Ports speak the unified data model (internal/core/model),
// which imports nothing, so there is no cycle.
package ports

import (
	"context"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// ConversationSource enumerates and parses one vendor's session logs. Every
// implementation is strictly read-only with respect to the vendor directory
// (TDD decision 9): it never writes to ~/.claude or ~/.codex.
type ConversationSource interface {
	// Vendor identifies the vendor this source reads.
	Vendor() model.Vendor

	// List returns the path of every session log beneath root, in a stable
	// order. A missing or empty root is not an error — it yields no paths,
	// so a machine with only one vendor installed syncs cleanly (US-7).
	List(ctx context.Context, root string) ([]string, error)

	// Parse normalizes one session log into a session doc. Malformed lines
	// and unrecognized record types are counted in the returned stats and
	// skipped, never fatal (TDD decision 7). An error means the file could
	// not be read at all; a file that yields no usable records returns a
	// zero doc, its skip stats, and a nil error.
	Parse(ctx context.Context, path string) (model.SessionDoc, model.ParseStats, error)
}
