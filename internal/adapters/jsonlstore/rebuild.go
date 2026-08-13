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
	// firstErr is the first error any Put or Commit on this rebuild
	// produced. Once set, Commit refuses to swap: a rebuild that dropped
	// even one doc must never overwrite a good previous generation with a
	// silently truncated one. This is the entire fix for that hazard — the
	// state Commit's precondition checks is `r.done || r.firstErr != nil`.
	firstErr error
}

// Put encodes doc and writes it into the pending generation at
// "<staging>/<vendor>/<stem>.json". Nothing under the live "sessions/" tree
// is touched; a failure here (a bad doc, a marshal error, an I/O error)
// leaves the previous generation completely untouched. The failure is also
// remembered: it makes every later Commit on this rebuild fail rather than
// swap in a generation missing this doc. Later Puts are unaffected — the
// caller may keep writing the rest of the batch — only Commit is blocked.
func (r *rebuild) Put(ctx context.Context, doc model.SessionDoc) error {
	if r.done {
		return ports.ErrRebuildFinished
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := r.put(doc); err != nil {
		r.recordErr(err)
		return err
	}
	return nil
}

func (r *rebuild) put(doc model.SessionDoc) error {
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

// recordErr remembers err as firstErr if nothing has failed on this
// rebuild yet. Only the first failure matters: it is the one that poisons
// Commit, and later failures don't change that outcome.
func (r *rebuild) recordErr(err error) {
	if r.firstErr == nil {
		r.firstErr = err
	}
}

// Commit performs the two-rename swap: the live "sessions" directory (if
// any) moves aside to a uniquely-named trash directory, then the staged
// generation moves into "sessions". If the second rename fails, Commit
// attempts to roll the first one back before returning, so a returned
// error never leaves the store without a live generation, unless the
// rollback rename itself also fails (see the relocation-to-orphan comment
// below). done is set only once both renames have succeeded, so a failed
// Commit leaves the rebuild unfinished and the caller's deferred Discard
// still cleans up the staging tree.
//
// Commit also refuses to run at all if any Put on this rebuild already
// failed (r.firstErr != nil): swapping in a generation known to be missing
// a doc would silently truncate the live store, which is worse than
// failing the sync run outright and leaving the previous generation in
// place.
func (r *rebuild) Commit(ctx context.Context) error {
	if r.done {
		return ports.ErrRebuildFinished
	}
	if r.firstErr != nil {
		return fmt.Errorf("jsonlstore: refusing to commit: an earlier write into this generation failed: %w", r.firstErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	live := filepath.Join(r.s.root, sessionsDir)
	trash := filepath.Join(r.s.root, trashPrefix+strings.TrimPrefix(filepath.Base(r.staging), stagingPrefix))

	haveLive, err := r.statLiveIsDir(live)
	if err != nil {
		r.recordErr(err)
		return err
	}

	if haveLive {
		if err := r.s.fs.Rename(live, trash); err != nil {
			err = fmt.Errorf("jsonlstore: moving previous generation aside: %w", err)
			r.recordErr(err)
			return err
		}
	}

	if err := r.s.fs.Rename(r.staging, live); err != nil {
		var rollbackErr error
		if haveLive {
			rollbackErr = r.s.fs.Rename(trash, live)
		}
		if rollbackErr != nil {
			joined := errors.Join(err, rollbackErr)
			wrapped := r.wrapRollbackFailure(trash, joined)
			r.recordErr(wrapped)
			return wrapped
		}
		wrapped := fmt.Errorf("jsonlstore: swap failed, rolled back to the previous generation: %w", err)
		r.recordErr(wrapped)
		return wrapped
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

// wrapRollbackFailure builds the error for the rollback-failed case: the
// swap rename failed, and rolling trash back to live then failed too, so
// "sessions/" is now missing entirely and the previous generation is
// sitting at trash. Left alone, that trash path is exactly what sweep()
// deletes on the very next BeginRebuild, so the recovery pointer would
// expire the moment anyone re-runs sync. wrapRollbackFailure best-effort
// renames trash to an ".orphan-" prefix — a name sweep never matches — and
// names whichever path survives in the returned error, so the message
// stays accurate about what re-running sync will and won't discard.
func (r *rebuild) wrapRollbackFailure(trash string, cause error) error {
	orphan := filepath.Join(r.s.root, orphanPrefix+strings.TrimPrefix(filepath.Base(trash), trashPrefix))
	if err := r.s.fs.Rename(trash, orphan); err != nil {
		r.s.log.Warn("jsonlstore: relocating the previous generation out of sweep's reach failed; it remains at the trash path and will be discarded by the next sync", "trash", trash, "orphan", orphan, "error", err)
		return fmt.Errorf("jsonlstore: swap failed and rollback failed; the previous generation is at %s, which the next sync's leftover sweep WILL discard: %w",
			trash, cause)
	}
	return fmt.Errorf("jsonlstore: swap failed and rollback failed; the previous generation was moved to %s, which sync's leftover sweep will NOT discard: %w",
		orphan, cause)
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
