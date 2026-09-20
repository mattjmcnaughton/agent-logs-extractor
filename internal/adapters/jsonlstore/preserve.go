package jsonlstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// preserve copies bytes, not decoded documents, so even a source unavailable in
// this build survives a selected-vendor refresh. Symlinks/special files fail
// closed instead of following foreign paths or silently dropping stored data.
func (r *rebuild) preserve(ctx context.Context) error {
	live := filepath.Join(r.s.root, sessionsDir)
	selected := map[string]bool{}
	for v := range r.selected {
		selected[escapeElement(string(v))] = true
	}
	return afero.Walk(r.s.fs, live, func(p string, info os.FileInfo, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if err != nil {
			if p == live && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(live, p)
		if err != nil {
			return err
		}
		if rel == "." {
			if !info.IsDir() {
				return fmt.Errorf("jsonlstore: live generation is not a directory")
			}
			return nil
		}
		vendor := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if selected[vendor] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("jsonlstore: cannot preserve non-regular entry %s", p)
		}
		target := filepath.Join(r.staging, rel)
		if info.IsDir() {
			return r.s.fs.MkdirAll(target, dirMode)
		}
		data, err := afero.ReadFile(r.s.fs, p)
		if err != nil {
			return fmt.Errorf("jsonlstore: preserving %s: %w", p, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return afero.WriteFile(r.s.fs, target, data, fileMode)
	})
}
