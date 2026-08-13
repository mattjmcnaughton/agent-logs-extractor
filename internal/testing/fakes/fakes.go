// Package fakes provides hand-written in-memory fakes for every port, used
// by the unit tests in internal/core/. They can be seeded with canned data
// and scripted to fail. Tests assert on their observable state — what a
// commit left in the store, what a sink was asked to produce, *which*
// paths a source read — never on the order calls happened in or how many
// times a method ran.
package fakes

import (
	"context"
	"maps"
	"slices"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// ErrRebuildFinished is returned by FakeStoreRebuild once a rebuild has
// been committed or discarded: a real store rejects writes to a swapped-out
// generation, and the fake must too. Aliased to ports.ErrRebuildFinished so
// every caller of this fake and every real adapter (jsonlstore, #7) return
// the same sentinel, and callers can errors.Is against either name.
var ErrRebuildFinished = ports.ErrRebuildFinished

// ErrInvalidSessionDoc is returned by FakeStoreRebuild.Put for a doc whose
// session id is empty or not namespaced as "<vendor>:<rest>", mirroring
// jsonlstore's real rejection (jsonlstore.ErrInvalidSessionDoc wraps the
// same ports.ErrInvalidSessionDoc). Aliased for the same reason
// ErrRebuildFinished is: one sentinel every caller can errors.Is against
// regardless of which CanonicalStore they're pointed at.
var ErrInvalidSessionDoc = ports.ErrInvalidSessionDoc

// FakeConversationSource implements ports.ConversationSource.
type FakeConversationSource struct {
	VendorName model.Vendor                // vendor this source reports
	Paths      map[string][]string         // session files under each root; absent root yields none (US-7)
	Docs       map[string]model.SessionDoc // canned parse result per path
	Stats      map[string]model.ParseStats // canned parse accounting per path
	ListErrs   map[string]error            // scripted failures by root
	ParseErrs  map[string]error            // scripted failures by path
	// Listed is the set of roots List was asked about — an outcome tests
	// can assert against (e.g. "codex was never listed when --vendor
	// claude was passed"), not a call sequence, so callers must sort
	// before comparing.
	Listed []string
	// Parsed is the set of paths Parse was asked to read — an outcome
	// tests can assert against (e.g. "every seeded path got parsed"), not
	// a call sequence, so callers must sort before comparing.
	Parsed []string
}

// NewConversationSource allocates a source reporting vendor, with all
// scripting maps ready to seed.
func NewConversationSource(vendor model.Vendor) *FakeConversationSource {
	return &FakeConversationSource{
		VendorName: vendor,
		Paths:      make(map[string][]string),
		Docs:       make(map[string]model.SessionDoc),
		Stats:      make(map[string]model.ParseStats),
		ListErrs:   make(map[string]error),
		ParseErrs:  make(map[string]error),
	}
}

// Seed registers path as a session file under root, with doc/stats as its
// canned Parse result.
func (s *FakeConversationSource) Seed(root, path string, doc model.SessionDoc, stats model.ParseStats) {
	s.Paths[root] = append(s.Paths[root], path)
	s.Docs[path] = doc
	s.Stats[path] = stats
}

// Vendor returns the vendor this source reports.
func (s *FakeConversationSource) Vendor() model.Vendor {
	return s.VendorName
}

// List returns the seeded paths under root, sorted for deterministic
// assertions, or ListErrs[root] if scripted.
func (s *FakeConversationSource) List(ctx context.Context, root string) ([]string, error) {
	s.Listed = append(s.Listed, root)
	if err := s.ListErrs[root]; err != nil {
		return nil, err
	}
	sorted := slices.Clone(s.Paths[root])
	slices.Sort(sorted)
	return sorted, nil
}

// Parse returns the canned doc and stats for path, or ParseErrs[path] if
// scripted.
func (s *FakeConversationSource) Parse(ctx context.Context, path string) (model.SessionDoc, model.ParseStats, error) {
	s.Parsed = append(s.Parsed, path)
	if err := s.ParseErrs[path]; err != nil {
		return model.SessionDoc{}, model.ParseStats{}, err
	}
	return s.Docs[path], s.Stats[path], nil
}

// FakeCanonicalStore implements ports.CanonicalStore.
type FakeCanonicalStore struct {
	RootPath  string                      // what Root reports
	Committed map[string]model.SessionDoc // live store, by session id
	BeginErr  error
	PutErrs   map[string]error // by session id
	CommitErr error
}

// NewCanonicalStore allocates an empty store at a fixed synthetic path.
func NewCanonicalStore() *FakeCanonicalStore {
	return &FakeCanonicalStore{
		RootPath:  "/fake/store",
		Committed: make(map[string]model.SessionDoc),
		PutErrs:   make(map[string]error),
	}
}

// Root returns RootPath.
func (s *FakeCanonicalStore) Root() string {
	return s.RootPath
}

// BeginRebuild returns BeginErr if scripted, else a fresh
// *FakeStoreRebuild bound to this store. It also checks ctx.Err() first,
// mirroring jsonlstore.Store.BeginRebuild, so a #8 core unit test written
// against a cancelled context behaves the same against this fake as it
// would against the real store.
func (s *FakeCanonicalStore) BeginRebuild(ctx context.Context) (ports.StoreRebuild, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.BeginErr != nil {
		return nil, s.BeginErr
	}
	return &FakeStoreRebuild{
		store:   s,
		pending: make(map[string]model.SessionDoc),
	}, nil
}

// SessionIDs is the sorted set of ids in the live store — a stable
// assertion surface.
func (s *FakeCanonicalStore) SessionIDs() []string {
	return slices.Sorted(maps.Keys(s.Committed))
}

// FakeStoreRebuild implements ports.StoreRebuild.
type FakeStoreRebuild struct {
	store   *FakeCanonicalStore
	pending map[string]model.SessionDoc
	done    bool
	// firstErr is the first error any Put or Commit on this rebuild
	// produced. Once set, Commit refuses to swap — mirrors jsonlstore's B1
	// fix (rebuild.firstErr in the real adapter) so a lenient #8 sync loop
	// that treats a Put failure as a counted-not-fatal skip and proceeds to
	// Commit anyway sees the same "refused, previous generation intact"
	// behavior against the fake as it would against the real store.
	firstErr error
}

// Put stores doc into the pending generation, keyed by session id, or
// returns PutErrs[doc.Session.SessionID] if scripted. It also checks
// ctx.Err() and rejects a doc that ports.ValidSessionDoc reports invalid
// (empty/non-namespaced/bare-prefix session id, or a traversal vendor) with
// ErrInvalidSessionDoc — the same predicate jsonlstore.relPath calls, so the
// two implementations cannot drift apart — matching jsonlstore's real
// contract so #8's core unit tests (which run against this fake) can catch
// a sync loop that forgets to skip such docs before calling Put. Once the
// rebuild has finished (Commit or Discard), Put returns ErrRebuildFinished
// rather than silently writing into a generation nothing can observe. Any
// Put failure is remembered as firstErr, which poisons every later Commit
// on this rebuild; later Puts are otherwise unaffected.
func (r *FakeStoreRebuild) Put(ctx context.Context, doc model.SessionDoc) error {
	if r.done {
		return ErrRebuildFinished
	}
	if err := ctx.Err(); err != nil {
		r.recordErr(err)
		return err
	}
	if err := r.store.PutErrs[doc.Session.SessionID]; err != nil {
		r.recordErr(err)
		return err
	}
	if !ports.ValidSessionDoc(doc.Session) {
		r.recordErr(ErrInvalidSessionDoc)
		return ErrInvalidSessionDoc
	}
	r.pending[doc.Session.SessionID] = doc
	return nil
}

// recordErr remembers err as firstErr if nothing has failed on this
// rebuild yet, matching jsonlstore's rebuild.recordErr.
func (r *FakeStoreRebuild) recordErr(err error) {
	if r.firstErr == nil {
		r.firstErr = err
	}
}

// Commit returns firstErr if an earlier Put on this rebuild failed,
// ctx.Err() if the context is cancelled, or CommitErr if scripted, leaving
// the live store untouched in every case; otherwise it replaces the live
// store wholesale with a copy of the pending generation — a full rebuild,
// not a merge — and marks the rebuild finished. A copy, not the pending map
// itself, is stored so a later (rejected) Put on this rebuild can never
// alias into the live store. Commit after Discard, or a second Commit,
// returns ErrRebuildFinished and leaves the live store exactly as it was.
func (r *FakeStoreRebuild) Commit(ctx context.Context) error {
	if r.done {
		return ErrRebuildFinished
	}
	if r.firstErr != nil {
		return r.firstErr
	}
	if err := ctx.Err(); err != nil {
		r.recordErr(err)
		return err
	}
	if err := r.store.CommitErr; err != nil {
		r.recordErr(err)
		return err
	}
	r.store.Committed = maps.Clone(r.pending)
	r.done = true
	return nil
}

// Discard drops the pending generation and marks the rebuild finished,
// leaving the live store untouched. It is not an error to call it after
// Commit or a previous Discard — it is simply a no-op then — but it does
// finish the rebuild, so a Commit that follows a Discard is rejected by
// Commit's own done check rather than resurrecting the discarded
// generation.
func (r *FakeStoreRebuild) Discard() error {
	if r.done {
		return nil
	}
	r.done = true
	r.pending = nil
	return nil
}

// FakeExporter implements ports.Exporter.
type FakeExporter struct {
	SinkName string
	Requests []ports.ExportRequest // every request, in order
	Err      error
}

// NewExporter allocates an exporter reporting name.
func NewExporter(name string) *FakeExporter {
	return &FakeExporter{SinkName: name}
}

// Name returns SinkName.
func (e *FakeExporter) Name() string {
	return e.SinkName
}

// Export records req and returns Err if scripted.
func (e *FakeExporter) Export(ctx context.Context, req ports.ExportRequest) error {
	e.Requests = append(e.Requests, req)
	return e.Err
}

// Interface conformance.
var (
	_ ports.ConversationSource = (*FakeConversationSource)(nil)
	_ ports.CanonicalStore     = (*FakeCanonicalStore)(nil)
	_ ports.StoreRebuild       = (*FakeStoreRebuild)(nil)
	_ ports.Exporter           = (*FakeExporter)(nil)
)
