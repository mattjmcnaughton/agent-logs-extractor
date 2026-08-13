// Package main is the wiring layer: it reads the environment, resolves the
// tool's paths, constructs every concrete adapter, injects them into the
// use cases, and hands those to the cobra root. This is the only file that
// imports every concrete adapter.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/cli"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/jsonlstore"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/export"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

func main() {
	// One logger, one level, one owner: level starts from
	// AGENT_LOGS_EXTRACTOR_LOG_LEVEL (else info, slog.LevelVar's own zero
	// value), and cli's PersistentPreRunE mutates the same LevelVar from
	// --log-level when that flag is passed, so the flag and the env var
	// both drive the one handler every use case logs through. Both parse
	// through cli.ParseLevel, the one level parser shared by wiring and
	// the CLI, but with different error policies: an invalid env var is
	// not fatal (this is process startup, before any flag has even been
	// parsed) — it's warned about here and falls back to info — while an
	// invalid --log-level is a hard error, handled in applyLogLevel.
	level := new(slog.LevelVar)
	if s := os.Getenv("AGENT_LOGS_EXTRACTOR_LOG_LEVEL"); s != "" {
		if l, err := cli.ParseLevel(s); err != nil {
			fmt.Fprintf(os.Stderr, "agent-logs-extractor: warning: invalid AGENT_LOGS_EXTRACTOR_LOG_LEVEL %q: %v; using info\n", s, err)
		} else {
			level.Set(l)
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	paths := defaultPaths()

	// Driven adapters land here as their tickets close:
	//   sources: claudesource (landed, #6), codexsource (#10)
	//   store:   jsonlstore   (landed, #7)
	//   sinks:   duckdbcli    (#9)
	// Until #9 lands, Export is constructed with no sinks registered, so
	// `export <sink>` always reports "unknown sink" rather than
	// dereferencing a nil adapter.
	fsys := afero.NewOsFs()
	sources := []ports.ConversationSource{claudesource.New(log)}
	var store ports.CanonicalStore = jsonlstore.New(fsys, paths.storeRoot, log)
	var exporters []ports.Exporter

	deps := cli.Deps{
		Sync:             sync.New(sources, store, log),
		Export:           export.New(store, exporters, log),
		DefaultRoots:     paths.vendorRoots,
		DefaultExportOut: paths.exportOut,
		Level:            level,
	}

	// NotifyContext, not just signal.Notify: Ctrl-C during a sync must
	// unwind through Run's deferred r.Discard() rather than killing the
	// process mid-Put and leaving a ".staging-*" directory behind for the
	// next BeginRebuild's sweep to find. os/signal is infrastructure, so
	// this lives here in wiring, never in internal/core.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRoot(deps).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "agent-logs-extractor:", err)
		os.Exit(1)
	}
}

// resolvedPaths are the tool's default on-disk locations, resolved from the
// environment (README "File layout" / "Sandboxing").
type resolvedPaths struct {
	vendorRoots map[model.Vendor]string
	storeRoot   string
	exportOut   string
}

// defaultPaths resolves the tool's default paths from
// AGENT_LOGS_EXTRACTOR_HOME (falling back to the user's home directory),
// matching the README. It is pure path arithmetic — no directory creation,
// no other I/O — see main_test.go for its acceptance.
func defaultPaths() resolvedPaths {
	home := os.Getenv("AGENT_LOGS_EXTRACTOR_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			home = "."
		}
	}

	return resolvedPaths{
		vendorRoots: map[model.Vendor]string{
			model.VendorClaude: filepath.Join(home, ".claude"),
			model.VendorCodex:  filepath.Join(home, ".codex"),
		},
		storeRoot: filepath.Join(home, ".local", "share", "agent-logs-extractor", "store"),
		exportOut: filepath.Join(home, ".local", "share", "agent-logs-extractor", "export", "logs.duckdb"),
	}
}
