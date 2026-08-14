// Package sync holds the Sync use case: parse every vendor source and
// rebuild the canonical store in one atomic pass (TDD decision 6).
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// ErrNoSourceForVendor is returned by Run when a Request names a vendor
// with no registered source — e.g. `sync --vendor codex` before #10 lands.
var ErrNoSourceForVendor = errors.New("sync: no source registered for vendor")

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
	// FilesUnreadable counts session files whose Parse call itself failed
	// — the file could not be read at all (ports.ConversationSource.Parse's
	// doc), not a record within it. This is a categorically different,
	// strictly weaker failure than a Put/Commit error: it is data the tool
	// never had, so — like every other skip reason — it is counted and
	// lenient rather than fatal to the run (see Run's doc). Printed in the
	// CLI summary only when non-zero.
	FilesUnreadable int
}

// TotalSkipped sums Skipped across every reason.
func (v VendorSummary) TotalSkipped() int {
	n := 0
	for _, c := range v.Skipped {
		n += c
	}
	return n
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
//
// The CLI's default fan-out (no --vendor passed) is filtered by Vendors():
// it syncs whatever this Sync actually has a source for, rather than
// hardcoding both vendor names, so a deferred adapter (Codex, until #10)
// is simply absent from the default run instead of making Run fail.
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

// Vendors is the set of vendors this Sync can ingest, sorted. Nil-receiver-
// safe (cli.NewRoot's tests build a Deps{} with a nil Sync just to inspect
// the command tree), so callers never need a nil check before calling it.
func (s *Sync) Vendors() []model.Vendor {
	if s == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(s.sources))
}

// Run parses every requested source and rebuilds the store atomically,
// returning the ingest summary.
//
// Severity is a three-way split, not two:
//   - A malformed record or an unrecognized record type within a file
//     (model.SkipCounts, via ports.ConversationSource.Parse's stats) is
//     data the tool read successfully but chose not to turn into a row.
//     Counted, lenient, never fatal (TDD decision 7).
//   - A Parse error — the file itself could not be read at all — is data
//     the tool never had. It is also counted and lenient: warned at the
//     call site, tallied in VendorSummary.FilesUnreadable, and the run
//     continues past it. One unreadable file (a permissions error, a
//     transient I/O fault) must not permanently freeze every future sync
//     at a stale generation, and claudesource.List already extends the
//     same warn-and-continue treatment to an unreadable project
//     *directory* — treating an unreadable *file* as fatal here would be
//     an indefensible asymmetry with that.
//   - A List, Put, or Commit failure is data the tool *had* and would be
//     silently dropping (Put/Commit), or a failure to even enumerate what
//     there is to ingest (List). Those abort the run and leave the
//     previous store generation intact — the store's own guarantee
//     (jsonlstore's rebuild.firstErr) backs this for Put/Commit; List and
//     BeginRebuild errors are handled the same way here for consistency.
//
// An empty Request.Sources performs no rebuild at all — not even an empty
// Commit — because a full-rebuild Commit with zero Puts would wipe out a
// live store that Request simply wasn't asked to touch.
func (s *Sync) Run(ctx context.Context, req Request) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if len(req.Sources) == 0 {
		return Summary{}, nil
	}

	// Resolve every vendor to a registered source before touching the
	// store at all, so a bad --vendor value never leaves a partially
	// rebuilt generation behind.
	type work struct {
		req SourceRequest
		src ports.ConversationSource
	}
	items := make([]work, 0, len(req.Sources))
	for _, sr := range req.Sources {
		src, ok := s.sources[sr.Vendor]
		if !ok {
			return Summary{}, fmt.Errorf("%w: %s", ErrNoSourceForVendor, sr.Vendor)
		}
		items = append(items, work{req: sr, src: src})
	}

	r, err := s.store.BeginRebuild(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("sync: beginning store rebuild: %w", err)
	}
	defer func() { _ = r.Discard() }()

	// counts is keyed by session id, shared across every vendor in this
	// Request, so dedup is global — matching the store's own global
	// session-id key — rather than per-vendor-call-local. Two SourceRequest
	// entries can legitimately name the same vendor twice (two roots), and
	// nothing stops a source from emitting a doc whose Session.Vendor
	// disagrees with the SourceRequest it came from (guarded against
	// separately, below, but defense-in-depth costs nothing here): either
	// way, a session id that more than one ingestVendor call Puts must only
	// ever contribute to one vendor's printed tally, matching the one doc
	// it actually leaves in the store.
	counts := make(map[string][2]int) // session id -> {messages, toolCalls}

	summary := Summary{Vendors: make([]VendorSummary, 0, len(items))}
	for _, it := range items {
		vs, err := s.ingestVendor(ctx, r, it.src, it.req, counts)
		if err != nil {
			return Summary{}, err
		}
		summary.Vendors = append(summary.Vendors, vs)
	}

	if err := r.Commit(ctx); err != nil {
		return Summary{}, fmt.Errorf("sync: committing store rebuild: %w", err)
	}
	s.log.Debug("sync: store rebuilt", "root", s.store.Root(), "vendors", len(summary.Vendors))
	return summary, nil
}

// ingestVendor lists and parses every session file src reports under
// sr.Root, writes each into r, and accumulates one vendor's summary. counts
// is Run's shared, global session-id -> (messages, toolCalls) map (see
// Run's doc): two files that parse to the same session id both go into the
// store (the later Put wins) and must only ever be counted once, with the
// later doc's message and tool-call counts — otherwise the printed summary
// would not reconcile against what the store actually holds. before
// snapshots counts as this call found it, so the tally at the end can tell
// "an id this call itself put for the first time" (credited to this
// vendor) apart from "an id an earlier vendor in this same Run already put"
// (already credited there; not re-counted here even though this call's Put
// just overwrote it in the store).
func (s *Sync) ingestVendor(ctx context.Context, r ports.StoreRebuild, src ports.ConversationSource, sr SourceRequest, counts map[string][2]int) (VendorSummary, error) {
	vs := VendorSummary{Vendor: sr.Vendor}
	before := maps.Clone(counts)
	own := make(map[string]bool) // ids this call Put, regardless of whether they pre-existed in counts
	skips := model.SkipCounts{}

	paths, err := src.List(ctx, sr.Root)
	if err != nil {
		return VendorSummary{}, fmt.Errorf("sync: listing %s session logs under %s: %w", sr.Vendor, sr.Root, err)
	}
	s.log.Debug("sync: listed session logs", "vendor", sr.Vendor, "root", sr.Root, "files", len(paths))

	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return VendorSummary{}, err
		}

		doc, stats, err := src.Parse(ctx, p)
		if err != nil {
			// A Parse error means the file could not be read at all — data
			// the tool never had, not data it is dropping. See Run's doc
			// for the full reasoning behind treating this as lenient.
			s.log.Warn("sync: session log could not be read; skipping it", "vendor", sr.Vendor, "path", p, "error", err)
			vs.FilesUnreadable++
			continue
		}
		for reason, n := range stats.Skipped {
			skips[reason] += n
		}

		if len(doc.Messages) == 0 && len(doc.ToolCalls) == 0 && doc.Session.SessionID == "" {
			s.log.Debug("sync: session log yielded no records; nothing stored", "vendor", sr.Vendor, "path", p)
			continue
		}
		if !ports.ValidSessionDoc(doc.Session) {
			s.log.Warn("sync: session doc has no storable session id; skipping it",
				"vendor", doc.Session.Vendor, "session_id", doc.Session.SessionID, "path", p)
			continue
		}
		if doc.Session.Vendor != sr.Vendor {
			s.log.Warn("sync: session doc's vendor does not match the source it came from; skipping it",
				"source_vendor", sr.Vendor, "doc_vendor", doc.Session.Vendor, "session_id", doc.Session.SessionID, "path", p)
			continue
		}
		if _, dup := counts[doc.Session.SessionID]; dup {
			s.log.Warn("sync: two session logs produced the same session id; the later one wins",
				"vendor", sr.Vendor, "session_id", doc.Session.SessionID, "path", p)
		}

		if err := r.Put(ctx, doc); err != nil {
			return VendorSummary{}, fmt.Errorf("sync: writing session %s (from %s): %w", doc.Session.SessionID, p, err)
		}
		counts[doc.Session.SessionID] = [2]int{len(doc.Messages), len(doc.ToolCalls)}
		own[doc.Session.SessionID] = true
	}

	for id := range own {
		if _, existedBefore := before[id]; existedBefore {
			// Already credited to an earlier vendor in this Run; this
			// call's Put overwrote the store entry (the later Put still
			// wins there), but must not double-count it in the summary.
			continue
		}
		vs.Sessions++
		c := counts[id]
		vs.Messages += c[0]
		vs.ToolCalls += c[1]
	}
	if len(skips) > 0 {
		vs.Skipped = skips
	}

	for _, reason := range slices.Sorted(maps.Keys(vs.Skipped)) {
		s.log.Debug("sync: skipped records", "vendor", vs.Vendor, "reason", reason, "count", vs.Skipped[reason])
	}

	return vs, nil
}
