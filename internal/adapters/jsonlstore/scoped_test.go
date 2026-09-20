package jsonlstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/fakes"
	"github.com/spf13/afero"
)

func TestScopedRebuildPreservesEveryUnselectedVendor(t *testing.T) {
	for _, kind := range []string{"real", "fake"} {
		t.Run(kind, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			fake := fakes.NewCanonicalStore()
			var store ports.CanonicalStore = New(fs, "/store", nil)
			read := func() map[string]model.SessionDoc {
				out := map[string]model.SessionDoc{}
				for _, b := range snapshotBytes(t, fs, "/store/sessions") {
					var d model.SessionDoc
					if err := json.Unmarshal(b, &d); err != nil {
						t.Fatal(err)
					}
					out[d.Session.SessionID] = d
				}
				return out
			}
			if kind == "fake" {
				store = fake
				read = func() map[string]model.SessionDoc { return fake.Committed }
			}
			ctx := context.Background()
			seed, err := store.BeginRebuild(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range []model.Vendor{"claude", "codex", "testvendor", "vendor/escaped"} {
				if err := seed.Put(ctx, doc(v, "old", "untouched")); err != nil {
					t.Fatal(err)
				}
			}
			if err := seed.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			for _, selected := range []model.Vendor{"codex", "claude", "testvendor"} {
				r, err := store.BeginRebuild(ctx, []model.Vendor{selected})
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Put(ctx, doc(selected, "new", "refreshed")); err != nil {
					t.Fatal(err)
				}
				if err := r.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				got := read()
				if len(got) != 4 {
					t.Fatalf("lost other vendors: %v", got)
				}
				if _, ok := got[string(selected)+":old"]; ok {
					t.Fatal("selected stale session retained")
				}
				if got["vendor/escaped:old"].Messages[0].Text != "untouched" {
					t.Fatal("unknown vendor changed")
				}
			}
			// Refreshing an empty selected source removes its old sessions only.
			r, err := store.BeginRebuild(ctx, []model.Vendor{"codex"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if len(read()) != 3 {
				t.Fatal("empty selected source did not replace only itself")
			}
			// An out-of-scope write poisons the whole generation.
			r, err = store.BeginRebuild(ctx, []model.Vendor{"claude"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Put(ctx, doc("testvendor", "bad", "bad")); !errors.Is(err, ports.ErrVendorOutsideRebuild) {
				t.Fatalf("scope error=%v", err)
			}
			if err := r.Commit(ctx); err == nil {
				t.Fatal("committed after invalid write")
			}
			_ = r.Discard()
			if len(read()) != 3 {
				t.Fatal("failed refresh changed live store")
			}
			for _, v := range []model.Vendor{"", ".", ".."} {
				if _, err := store.BeginRebuild(ctx, []model.Vendor{v}); err == nil {
					t.Fatalf("accepted scope %q", v)
				}
			}
		})
	}
}

func TestScopedCopyFailureLeavesLiveBytesAndCleansStaging(t *testing.T) {
	fs := afero.NewMemMapFs()
	ctx := context.Background()
	store := New(fs, "/store", nil)
	r, err := store.BeginRebuild(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Put(ctx, doc("claude", "a", "retained")); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := snapshotBytes(t, fs, "/store/sessions")
	broken := New(&failFs{Fs: fs, OpenFileErr: func(name string, flag int, perm os.FileMode) error {
		if strings.Contains(name, ".staging-") && strings.HasSuffix(name, "a.json") {
			return errors.New("copy write failure")
		}
		return nil
	}}, "/store", nil)
	if _, err := broken.BeginRebuild(ctx, []model.Vendor{"codex"}); err == nil {
		t.Fatal("read-only refresh succeeded")
	}
	assertSnapshotsEqual(t, snapshotBytes(t, fs, "/store/sessions"), before)
	entries, _ := afero.ReadDir(fs, "/store")
	for _, e := range entries {
		if e.Name() != filepath.Base("/store/sessions") {
			t.Fatal("leftover", e.Name())
		}
	}
}
