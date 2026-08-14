// Package duckdbcli implements ports.Exporter as a subprocess adapter over
// the `duckdb` CLI (TDD core decision 4): the full SQL engine without CGO,
// at the cost of a runtime binary dependency for `export` only.
package duckdbcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

const (
	// SinkName is this exporter's Name() and the `export <sink>`
	// subcommand it matches.
	SinkName = "duckdb"
	// DefaultBinary is the executable name resolved via exec.LookPath when
	// no Option overrides it.
	DefaultBinary = "duckdb"
	// MinVersion names the DuckDB CLI version this adapter is developed and
	// tested against in CI (currently pinned there via DUCKDB_VERSION). It
	// is documentation only, not a tested contract across a version range —
	// deliberately not probed at runtime (see Export's doc for why), and no
	// claim is made that anything older actually works.
	MinVersion = "1.4"
)

// ErrBinaryNotFound is returned (wrapped) by Export when the duckdb binary
// cannot be resolved on PATH. Only `export` needs the binary; `sync` never
// does. The message carries no "duckdbcli: " prefix of its own — core
// export.Run already wraps every sink error as "export: <sink>: %w", and a
// second static prefix here would just stutter ("export: duckdb: duckdbcli:
// duckdb binary not found: ...").
var ErrBinaryNotFound = errors.New("duckdb binary not found")

// ErrNoStore is returned (wrapped) by Export when req.StoreRoot does not
// exist on disk at all — as opposed to existing but holding no session
// docs yet, which is a legitimate empty export (see storeHasDocs).
var ErrNoStore = errors.New("duckdbcli: no canonical store")

// Option configures an Adapter at construction time.
type Option func(*Adapter)

// WithBinary overrides the duckdb executable name or path resolved by
// exec.LookPath. Tests use this to point at a substitute executable
// without ever putting anything named "duckdb" on PATH.
func WithBinary(bin string) Option {
	return func(a *Adapter) { a.bin = bin }
}

// Adapter implements ports.Exporter by shelling out to the duckdb CLI.
type Adapter struct {
	bin string
	log *slog.Logger
}

// New returns an Adapter resolving DefaultBinary via PATH, as modified by
// opts. A nil log is normalized to a discard logger, the same convention
// every other adapter/use case in this module follows.
func New(log *slog.Logger, opts ...Option) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := &Adapter{bin: DefaultBinary, log: log}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Name returns SinkName.
func (a *Adapter) Name() string {
	return SinkName
}

// Export materializes req.StoreRoot into a snapshot DuckDB database file at
// req.Out.
//
// Order of checks, and why: req.StoreRoot's existence is checked first
// (storeHasDocs) — it is a cheap, local, Go-side check with no subprocess
// involved, and the most likely real-world failure (a fresh install that
// has never run `sync`) deserves a message naming `sync`, not a duckdb
// parse error. The duckdb binary is resolved second. Neither check touches
// req.Out, so a previous export at that path survives untouched if either
// fails.
//
// No proactive version probe (D5): a second subprocess on every export
// would guard a failure that a bad version already produces a precise
// parse/syntax error for. `-init /dev/null` neutralizes the user's
// ~/.duckdbrc so it can never perturb the generated script.
//
// The generated SQL is never exposed via a flag (D3): it is logged at
// debug (`--log-level debug` is already the documented diagnostic
// channel), and a failure's error message points there.
//
// A ".export-*" temp directory (below) can survive on disk if the process
// is killed (SIGKILL) between its creation and the deferred os.RemoveAll —
// accepted for the MVP rather than building a sweeper for it; jsonlstore's
// own ".staging-"/".trash-" sweep (BeginRebuild) has no equivalent here.
func (a *Adapter) Export(ctx context.Context, req ports.ExportRequest) error {
	hasDocs, err := storeHasDocs(req.StoreRoot)
	if err != nil {
		return err
	}

	bin, err := exec.LookPath(a.bin)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%w: install it from https://duckdb.org/docs/installation/ (developed against duckdb %s; older versions may not support the generated SQL); only `export` requires it, not `sync`", ErrBinaryNotFound, MinVersion)
		}
		return fmt.Errorf("duckdbcli: resolving %q: %w", a.bin, err)
	}

	if req.Out == "" {
		return fmt.Errorf("duckdbcli: empty output path")
	}

	// B1: resolve Out to an absolute path once, here, and use `out`
	// (never req.Out) for everything below. Without this, a relative Out
	// leaves outDir/tmpDir/snapshot relative too; os.MkdirTemp resolves a
	// relative dir against *this* Go process's cwd, but the resulting path
	// is then handed as a CLI argument to a subprocess whose cwd is
	// cmd.Dir (req.StoreRoot, set below per D2) — if the process's cwd
	// isn't also req.StoreRoot (the common case: `--out ./logs.duckdb` run
	// from wherever the user's shell happens to be), duckdb resolves that
	// same relative snapshot path against the wrong directory and fails to
	// create it. filepath.Abs resolves against the process's cwd, i.e. the
	// user's shell cwd — exactly the semantics `--out ./logs.duckdb`
	// documents in README.md.
	out, err := filepath.Abs(req.Out)
	if err != nil {
		return fmt.Errorf("duckdbcli: resolving output path %s: %w", req.Out, err)
	}

	script := Script(hasDocs)
	a.log.Debug("duckdbcli: generated export script", "sql", script)

	// D6: Out's directory creation belongs here, not wiring or the core —
	// wiring's defaultPaths() is pure path arithmetic, and --out can point
	// anywhere the core knows nothing about. Built from `out`, not req.Out,
	// so this (and everything derived from it below) is immune to the
	// relative-path/cmd.Dir mismatch described above.
	outDir := filepath.Dir(out)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("duckdbcli: creating output directory %s: %w", outDir, err)
	}

	// D4: a temp *directory*, not a temp file. DuckDB writes a sibling
	// *.wal next to the database file, so a temp file would leave a
	// "logs.duckdb.tmp1234.wal" litter on every failed run; a temp
	// directory can simply be removed whole. defer runs immediately after
	// the directory is confirmed created, before any later fallible call.
	tmpDir, err := os.MkdirTemp(outDir, ".export-")
	if err != nil {
		return fmt.Errorf("duckdbcli: creating temp export directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshot := filepath.Join(tmpDir, "snapshot.duckdb")

	// D2: the script is delivered on stdin, with cmd.Dir set to the store
	// root, so the store root itself never appears as a literal in SQL —
	// no quote-escaping story, and no risk of DuckDB glob-expanding a store
	// root that happens to contain '*', '?', or '['.
	cmd := exec.CommandContext(ctx, bin, "-init", os.DevNull, "-batch", "-bail", snapshot)
	cmd.Dir = req.StoreRoot
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"duckdbcli: %s exited with an error (developed against duckdb %s; older versions may not support the generated SQL; re-run with --log-level debug to print the generated SQL): %w\nstderr (last lines):\n%s",
			bin, MinVersion, err, lastLines(stderr.String(), 3),
		)
	}

	// The export holds the same verbatim prompts/code/output that
	// jsonlstore's canonical store protects with 0600 (see fileMode in
	// jsonlstore.go), but duckdb creates snapshot under the ambient umask
	// (typically 0644) — tighten it explicitly before it becomes visible at
	// Out.
	if err := os.Chmod(snapshot, 0o600); err != nil {
		return fmt.Errorf("duckdbcli: restricting permissions on %s: %w", snapshot, err)
	}

	// Out is untouched until this final rename — every earlier failure
	// path above leaves any previous output exactly as it was.
	if err := os.Rename(snapshot, out); err != nil {
		return fmt.Errorf("duckdbcli: moving snapshot into place at %s: %w", out, err)
	}

	return nil
}

// storeHasDocs reports whether root's "sessions/" tree holds at least one
// vendor session file (C.3). root not existing at all is ErrNoStore, naming
// `sync` — the common real-world cause (`export` run before any `sync`
// ever succeeded). "sessions/" absent, or present but holding no *.json
// under any vendor subdirectory, is `false, nil`: a fresh or all-empty
// store is a legitimate empty export, not an error (jsonlstore's #7 note,
// and A4).
//
// Known divergence: the ve.IsDir() check below is lstat-based (os.ReadDir
// never follows symlinks), so a vendor directory under "sessions/" that is
// itself a symlink is treated as absent here even if it resolves to a
// directory full of *.json files — storeHasDocs would report false while
// the read_json glob in script.go (which does follow symlinks) would still
// match those files. jsonlstore never creates such a symlink itself, so
// this only matters for a hand-assembled or externally-modified store; not
// resolved here, only documented.
func storeHasDocs(root string) (bool, error) {
	if root == "" {
		return false, fmt.Errorf("%w: empty store root", ErrNoStore)
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("%w: %s does not exist; run `sync` first", ErrNoStore, root)
		}
		return false, fmt.Errorf("duckdbcli: checking store root %s: %w", root, err)
	}

	sessionsDir := filepath.Join(root, "sessions")
	vendorEntries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("duckdbcli: reading %s: %w", sessionsDir, err)
	}

	for _, ve := range vendorEntries {
		if !ve.IsDir() {
			continue
		}
		vendorDir := filepath.Join(sessionsDir, ve.Name())
		files, err := os.ReadDir(vendorDir)
		if err != nil {
			return false, fmt.Errorf("duckdbcli: reading %s: %w", vendorDir, err)
		}
		for _, f := range files {
			if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
				return true, nil
			}
		}
	}
	return false, nil
}

// lastLines trims trailing newlines from s, then returns at most its last n
// lines, so a duckdb failure message stays readable instead of dumping its
// entire stderr.
func lastLines(s string, n int) string {
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == "" {
		return "(no stderr output)"
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Interface conformance.
var _ ports.Exporter = (*Adapter)(nil)
