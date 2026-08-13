package ports

import (
	"context"
	"errors"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// ErrRebuildFinished is what Put and Commit return once a rebuild has been
// committed or discarded. Implementations must return it (or wrap it) so
// callers can errors.Is against one value regardless of adapter.
var ErrRebuildFinished = errors.New("ports: store rebuild already finished")

// ErrInvalidSessionDoc is what Put returns for a doc whose session cannot
// be stored: an empty or non-vendor-namespaced session id, or a vendor that
// is itself a path-traversal element. Implementations must return it (or
// wrap it) so callers — including #8's sync, which must pre-filter with
// ValidSessionDoc before calling Put rather than treat this as a normal
// per-doc failure — can errors.Is against one value regardless of adapter.
var ErrInvalidSessionDoc = errors.New("ports: session doc has no usable session id")

// ValidSessionDoc reports whether sess carries a usable session id: a
// non-empty Vendor that is not itself a path-traversal element ("." or
// ".."), and a SessionID of the exact form "<vendor>:<rest>" with rest
// non-empty.
//
// This is the single source of truth for what a CanonicalStore.Put may
// accept. It lives here, alongside ErrInvalidSessionDoc, rather than in an
// adapter, precisely so every implementation can share it instead of
// reimplementing the check: jsonlstore.relPath, fakes.FakeStoreRebuild.Put,
// and any caller that wants to skip an invalid doc *before* calling Put
// (#8's sync loop) must all call ValidSessionDoc rather than hand-rolling
// their own version of "empty" or "not namespaced" — that duplication is
// exactly the mechanism by which the store's actual rejection set (empty,
// non-namespaced, bare-prefix, or traversal-vendor) previously drifted from
// a narrower pre-filter that only checked for an empty SessionID. A false
// result here is precisely the set of docs a conforming Put must reject
// with ErrInvalidSessionDoc (or a wrapping error).
func ValidSessionDoc(sess model.Session) bool {
	vendor := string(sess.Vendor)
	if vendor == "" || vendor == "." || vendor == ".." {
		return false
	}

	prefix := vendor + ":"
	id := sess.SessionID
	return strings.HasPrefix(id, prefix) && id != prefix
}

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
