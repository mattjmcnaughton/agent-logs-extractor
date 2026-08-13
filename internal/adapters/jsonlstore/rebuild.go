package jsonlstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// rebuild implements ports.StoreRebuild. It is unexported: BeginRebuild
// hands one back as the port, and nothing outside this package can
// construct one without a Store.
type rebuild struct {
	s       *Store
	staging string // "" once the rebuild has finished (committed or discarded)
	done    bool
}

// Put encodes doc and writes it into the pending generation at
// "<staging>/<vendor>/<stem>.json". Nothing under the live "sessions/" tree
// is touched; a failure here (a bad doc, a marshal error, an I/O error)
// leaves the previous generation completely untouched.
func (r *rebuild) Put(ctx context.Context, doc model.SessionDoc) error {
	if r.done {
		return ports.ErrRebuildFinished
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	rel, err := relPath(doc.Session)
	if err != nil {
		return err
	}

	data, err := encodeDoc(doc)
	if err != nil {
		return fmt.Errorf("jsonlstore: encoding session doc %q: %w", doc.Session.SessionID, err)
	}

	target := filepath.Join(r.staging, rel)
	if err := r.s.fs.MkdirAll(filepath.Dir(target), dirMode); err != nil {
		return fmt.Errorf("jsonlstore: creating directory for %s: %w", rel, err)
	}
	if err := afero.WriteFile(r.s.fs, target, data, fileMode); err != nil {
		return fmt.Errorf("jsonlstore: writing %s: %w", rel, err)
	}

	r.s.log.Debug("jsonlstore: put", "session_id", doc.Session.SessionID, "path", target)
	return nil
}

// Commit performs the two-rename swap: the live "sessions" directory (if
// any) moves aside to a uniquely-named trash directory, then the staged
// generation moves into "sessions". If the second rename fails, Commit
// attempts to roll the first one back before returning, so a returned
// error never leaves the store without a live generation. done is set only
// once both renames have succeeded, so a failed Commit leaves the rebuild
// unfinished and the caller's deferred Discard still cleans up the staging
// tree.
func (r *rebuild) Commit(ctx context.Context) error {
	if r.done {
		return ports.ErrRebuildFinished
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	live := filepath.Join(r.s.root, sessionsDir)
	trash := filepath.Join(r.s.root, trashPrefix+strings.TrimPrefix(filepath.Base(r.staging), stagingPrefix))

	haveLive, err := r.statLiveIsDir(live)
	if err != nil {
		return err
	}

	if haveLive {
		if err := r.s.fs.Rename(live, trash); err != nil {
			return fmt.Errorf("jsonlstore: moving previous generation aside: %w", err)
		}
	}

	if err := r.s.fs.Rename(r.staging, live); err != nil {
		var rollbackErr error
		if haveLive {
			rollbackErr = r.s.fs.Rename(trash, live)
		}
		if rollbackErr != nil {
			return fmt.Errorf("jsonlstore: swap failed and rollback failed; the previous store is at %s: %w",
				trash, errors.Join(err, rollbackErr))
		}
		return fmt.Errorf("jsonlstore: swap failed, rolled back to the previous generation: %w", err)
	}

	r.done = true
	r.staging = ""

	if haveLive {
		// Best-effort: the swap already succeeded and the live store is
		// correct either way. A leftover trash directory is swept by the
		// next BeginRebuild.
		if err := r.s.fs.RemoveAll(trash); err != nil {
			r.s.log.Warn("jsonlstore: removing previous generation failed; will be swept on next rebuild", "path", trash, "error", err)
		}
	}

	return nil
}

// statLiveIsDir reports whether live exists and is a directory. Existing
// but not a directory is a hard error rather than a silent clobber: on
// afero.OsFs the subsequent rename would fail with ENOTDIR anyway, but
// afero.MemMapFs would silently overwrite it, so this guard is load-bearing
// on the fake, not just defensive on the real filesystem.
func (r *rebuild) statLiveIsDir(live string) (bool, error) {
	fi, err := r.s.fs.Stat(live)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return false, fmt.Errorf("jsonlstore: %s exists and is not a directory", live)
		}
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("jsonlstore: stat %s: %w", live, err)
	}
}

// Discard drops the pending generation, leaving the live store untouched.
// The done flag is the entire state machine: it makes Discard after Commit
// a no-op, Discard called twice a no-op, and `defer r.Discard()` always
// safe to write regardless of how Commit/Put came out.
func (r *rebuild) Discard() error {
	if r.done {
		return nil
	}
	r.done = true
	staging := r.staging
	r.staging = ""

	if staging == "" {
		return nil
	}
	// RemoveAll of a missing path is not an error on either afero.MemMapFs
	// or afero.OsFs, so no existence check is needed first.
	if err := r.s.fs.RemoveAll(staging); err != nil {
		r.s.log.Warn("jsonlstore: discard: removing staging directory failed", "path", staging, "error", err)
		return fmt.Errorf("jsonlstore: discard: removing %s: %w", staging, err)
	}
	return nil
}

// Interface conformance.
var _ ports.StoreRebuild = (*rebuild)(nil)
