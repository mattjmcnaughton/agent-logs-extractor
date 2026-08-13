// Package jsonlstore implements ports.CanonicalStore over afero. The live
// generation lives at "<root>/sessions/<vendor>/<stem>.json" — one session
// doc per file, marshaled compact and terminated with a newline, so each
// file is, incidentally, a valid one-record newline-delimited-JSON file
// (naming.go's encodeDoc). Every sync is a full rebuild staged in a
// sibling directory under root and swapped in with two renames
// (rebuild.go), never a per-record write to the live tree.
//
// Guarantees, stated precisely rather than optimistically:
//   - A rebuild that returns an error from Put or Commit leaves the
//     previously committed generation exactly as it was; so does any
//     Discard. This is the "atomic swap" the ticket asks for, and it holds
//     with one named exception: nothing under "sessions/" is touched until
//     both renames in Commit have succeeded, AND Commit itself refuses to
//     attempt the swap at all once any Put on that rebuild has failed
//     (rebuild.go's firstErr) — a generation known to be missing a doc is
//     never allowed to overwrite a good one. The exception is the rollback
//     path: if the swap rename fails and the rollback rename that tries to
//     restore the previous generation *also* fails, "sessions/" is left
//     absent rather than restored (see rebuild.go's Commit doc).
//   - It is NOT crash-atomic. Commit renames the live "sessions" directory
//     aside and then renames the staged directory into its place; a
//     process killed between those two renames (SIGKILL, power loss) can
//     leave "sessions/" absent, with the previous generation sitting at
//     ".trash-<rand>" and the new one at ".staging-<rand>" (now itself the
//     rename target, so misleadingly named ".staging-" for what is really
//     the finished generation). The store is fully reconstructible by
//     re-running sync — it derives entirely from read-only vendor logs —
//     so this window is an accepted cost of a portable, dependency-free
//     two-rename swap rather than a platform-specific atomic rename
//     (renameat2(RENAME_EXCHANGE) is Linux-only) or a symlink flip (no
//     afero MemMapFs support, privileged on Windows). BeginRebuild sweeps
//     any ".staging-*"/".trash-*" leftovers it finds before starting a new
//     rebuild, so the tree self-heals on the next sync. A ".orphan-*"
//     directory is the one exception sweep leaves alone on purpose: it
//     only appears after the rollback-failure case above, and it is a
//     recovery pointer for a human, not sync leftovers.
//   - No fsync: afero exposes none, and the store is derived data, so
//     power-loss durability of a *committed* generation is not claimed
//     either.
//   - No concurrency locking. Two rebuilds against the same root will
//     interfere: BeginRebuild's leftover sweep removes every ".staging-*"
//     it finds, including a concurrently-running sibling's, after which
//     that sibling would go on to commit a partial generation. Callers
//     must serialize rebuilds against one root themselves.
package jsonlstore

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

const (
	sessionsDir   = "sessions"
	stagingPrefix = ".staging-"
	trashPrefix   = ".trash-"
	// orphanPrefix marks a previous generation that Commit could not roll
	// back into place after a failed swap. sweep only matches
	// stagingPrefix/trashPrefix, so an ".orphan-" directory survives every
	// future BeginRebuild until a human removes it by hand.
	orphanPrefix = ".orphan-"

	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// Store implements ports.CanonicalStore over an injected afero.Fs. fs is
// injected — unlike fetch-context's filestore, which hardcodes
// afero.NewOsFs() — specifically so this package can be unit-tested against
// afero.NewMemMapFs(); real callers (main.go, #8) pass afero.NewOsFs().
type Store struct {
	fs   afero.Fs
	root string
	log  *slog.Logger
}

// New returns a Store rooted at root. It performs no I/O; the root
// directory is created on the first BeginRebuild. root is filepath.Clean'd
// (an empty root is left as "" rather than cleaned to "." so BeginRebuild
// can reject it explicitly). A nil log is normalized to a discard logger,
// the same convention sync.New and claudesource.New use.
func New(fs afero.Fs, root string, log *slog.Logger) *Store {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Store{fs: fs, root: root, log: log}
}

// Root is the store's on-disk location, as documented by
// ports.CanonicalStore.
func (s *Store) Root() string {
	return s.root
}

// BeginRebuild creates the store root if missing, sweeps any leftover
// staging/trash directories from a previous interrupted rebuild, and
// starts a fresh pending generation staged beside the live store.
func (s *Store) BeginRebuild(ctx context.Context) (ports.StoreRebuild, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.root == "" {
		return nil, fmt.Errorf("jsonlstore: store root is empty")
	}

	if err := s.fs.MkdirAll(s.root, dirMode); err != nil {
		return nil, fmt.Errorf("jsonlstore: creating store root %s: %w", s.root, err)
	}

	// Sweep before creating our own staging dir, so the sweep can never
	// remove the very directory it is about to hand back.
	s.sweep()

	staging, err := afero.TempDir(s.fs, s.root, stagingPrefix)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: creating staging directory: %w", err)
	}

	return &rebuild{s: s, staging: staging}, nil
}

// sweep best-effort removes any ".staging-*"/".trash-*" directories left
// under root by a rebuild that never finished (a crash, or a returned
// error the caller didn't clean up after). Failures are logged at warn and
// never propagated: a leftover that resists cleanup this time is retried
// on the next BeginRebuild, and it never blocks starting a new rebuild.
func (s *Store) sweep() {
	entries, err := afero.ReadDir(s.fs, s.root)
	if err != nil {
		s.log.Warn("jsonlstore: sweep: reading store root failed", "root", s.root, "error", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, stagingPrefix) && !strings.HasPrefix(name, trashPrefix) {
			continue
		}
		path := filepath.Join(s.root, name)
		if err := s.fs.RemoveAll(path); err != nil {
			s.log.Warn("jsonlstore: sweep: removing leftover directory failed", "path", path, "error", err)
		}
	}
}

// Interface conformance.
var _ ports.CanonicalStore = (*Store)(nil)
