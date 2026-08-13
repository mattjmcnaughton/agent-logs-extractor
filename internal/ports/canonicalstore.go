package ports

import (
	"context"
	"errors"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// ErrRebuildFinished is what Put and Commit return once a rebuild has been
// committed or discarded. Implementations must return it (or wrap it) so
// callers can errors.Is against one value regardless of adapter.
var ErrRebuildFinished = errors.New("ports: store rebuild already finished")

// ErrInvalidSessionDoc is what Put returns for a doc whose session cannot
// be stored: an empty or non-vendor-namespaced session id. Implementations
// must return it (or wrap it) so callers — including #8's sync, which must
// skip such a doc before calling Put rather than treat this as a normal
// per-doc failure — can errors.Is against one value regardless of adapter.
var ErrInvalidSessionDoc = errors.New("ports: session doc has no usable session id")

// CanonicalStore owns the normalized store this tool writes. Every sync is
// a full rebuild (TDD decision 6): callers build a complete new generation
// through a StoreRebuild and swap it in atomically, so a failed sync always
// leaves the previous store intact.
type CanonicalStore interface {
	// Root is the store's on-disk location. Sinks that read the store
	// directly (the DuckDB exporter reads it with read_json) are handed
	// this path rather than deserialized documents.
	Root() string

	// BeginRebuild starts a new store generation, staged beside the live
	// store. The returned rebuild must be either committed or discarded;
	// Discard on a committed rebuild is a no-op, so `defer r.Discard()` is
	// always safe.
	BeginRebuild(ctx context.Context) (StoreRebuild, error)
}

// StoreRebuild accumulates one pending store generation. Once the rebuild
// is finished — committed or discarded — Put and Commit return
// ErrRebuildFinished (or wrap it); the generation is gone.
type StoreRebuild interface {
	// Put writes one session doc into the pending generation. Putting the
	// same session id twice replaces the earlier doc.
	Put(ctx context.Context, doc model.SessionDoc) error

	// Commit atomically replaces the live store with everything Put so far.
	// After Commit the live store contains exactly those docs and nothing
	// from the previous generation.
	Commit(ctx context.Context) error

	// Discard drops the pending generation, leaving the live store
	// untouched. It is safe to call after Commit and safe to call twice.
	Discard() error
}
