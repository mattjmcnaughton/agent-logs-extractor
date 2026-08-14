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
	// MinVersion is the lowest DuckDB CLI version this adapter targets. It
	// is documentation only — deliberately not probed at runtime (see
	// Export's doc for why).
	MinVersion = "1.0"
)

// ErrBinaryNotFound is returned (wrapped) by Export when the duckdb binary
// cannot be resolved on PATH. Only `export` needs the binary; `sync` never
// does.
var ErrBinaryNotFound = errors.New("duckdbcli: duckdb binary not found")

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
func (a *Adapter) Export(ctx context.Context, req ports.ExportRequest) error {
	hasDocs, err := storeHasDocs(req.StoreRoot)
	if err != nil {
		return err
	}

	bin, err := exec.LookPath(a.bin)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%w: install it from https://duckdb.org/docs/installation/ (>= %s); only `export` requires it, not `sync`", ErrBinaryNotFound, MinVersion)
		}
		return fmt.Errorf("duckdbcli: resolving %q: %w", a.bin, err)
	}

	if req.Out == "" {
		return fmt.Errorf("duckdbcli: empty output path")
	}

	script := Script(hasDocs)
	a.log.Debug("duckdbcli: generated export script", "sql", script)

	// D6: Out's directory creation belongs here, not wiring or the core —
	// wiring's defaultPaths() is pure path arithmetic, and --out can point
	// anywhere the core knows nothing about.
	outDir := filepath.Dir(req.Out)
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
			"duckdbcli: %s exited with an error (require duckdb >= %s; re-run with --log-level debug to print the generated SQL): %w\nstderr (last lines):\n%s",
			bin, MinVersion, err, lastLines(stderr.String(), 3),
		)
	}

	// Out is untouched until this final rename — every earlier failure
	// path above leaves any previous output exactly as it was.
	if err := os.Rename(snapshot, req.Out); err != nil {
		return fmt.Errorf("duckdbcli: moving snapshot into place at %s: %w", req.Out, err)
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

// lastLines returns at most n trailing non-empty lines of s, trimmed of
// leading/trailing blank lines, so a duckdb failure message stays readable
// instead of dumping its entire stderr.
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
