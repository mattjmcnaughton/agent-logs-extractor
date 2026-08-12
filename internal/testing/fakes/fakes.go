// Package fakes provides hand-written in-memory fakes for every port, used
// by the unit tests in internal/core/. They can be seeded with canned data
// and scripted to fail. Tests assert on their observable state — what a
// commit left in the store, what a sink was asked to produce — never on
// call sequences.
package fakes

import (
	"context"
	"sort"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// FakeConversationSource implements ports.ConversationSource.
type FakeConversationSource struct {
	VendorName model.Vendor                // vendor this source reports
	Paths      map[string][]string         // session files under each root; absent root yields none (US-7)
	Docs       map[string]model.SessionDoc // canned parse result per path
	Stats      map[string]model.ParseStats // canned parse accounting per path
	ListErrs   map[string]error            // scripted failures by root
	ParseErrs  map[string]error            // scripted failures by path
	Listed     []string                    // roots listed
	Parsed     []string                    // paths parsed
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
	paths := s.Paths[root]
	if paths == nil {
		return nil, nil
	}
	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.Strings(sorted)
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
// *FakeStoreRebuild bound to this store.
func (s *FakeCanonicalStore) BeginRebuild(ctx context.Context) (ports.StoreRebuild, error) {
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
	ids := make([]string, 0, len(s.Committed))
	for id := range s.Committed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// FakeStoreRebuild implements ports.StoreRebuild.
type FakeStoreRebuild struct {
	store   *FakeCanonicalStore
	pending map[string]model.SessionDoc
	done    bool
}

// Put stores doc into the pending generation, keyed by session id, or
// returns PutErrs[doc.Session.SessionID] if scripted.
func (r *FakeStoreRebuild) Put(ctx context.Context, doc model.SessionDoc) error {
	if err := r.store.PutErrs[doc.Session.SessionID]; err != nil {
		return err
	}
	r.pending[doc.Session.SessionID] = doc
	return nil
}

// Commit returns CommitErr if scripted, leaving the live store untouched;
// otherwise it replaces the live store wholesale with the pending
// generation — a full rebuild, not a merge.
func (r *FakeStoreRebuild) Commit(ctx context.Context) error {
	if err := r.store.CommitErr; err != nil {
		return err
	}
	r.store.Committed = r.pending
	r.done = true
	return nil
}

// Discard clears the pending generation. It is a no-op once Commit has
// run, and safe to call twice.
func (r *FakeStoreRebuild) Discard() error {
	if r.done {
		return nil
	}
	r.pending = make(map[string]model.SessionDoc)
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

// LastRequest is the most recent Export request, or the zero value if none.
func (e *FakeExporter) LastRequest() ports.ExportRequest {
	if len(e.Requests) == 0 {
		return ports.ExportRequest{}
	}
	return e.Requests[len(e.Requests)-1]
}

// Interface conformance.
var (
	_ ports.ConversationSource = (*FakeConversationSource)(nil)
	_ ports.CanonicalStore     = (*FakeCanonicalStore)(nil)
	_ ports.StoreRebuild       = (*FakeStoreRebuild)(nil)
	_ ports.Exporter           = (*FakeExporter)(nil)
)
