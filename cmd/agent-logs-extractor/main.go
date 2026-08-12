// Package main is the wiring layer: it reads the environment, resolves the
// tool's paths, constructs every concrete adapter, injects them into the
// use cases, and hands those to the cobra root. This is the only file that
// imports every concrete adapter.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/cli"
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
	//   sources: claudesource (#6), codexsource (#10)
	//   store:   jsonlstore   (#7)
	//   sinks:   duckdbcli    (#9)
	// Until then the use cases are constructed with no adapters; their Run
	// methods return ErrNotImplemented without dereferencing them.
	var sources []ports.ConversationSource
	var store ports.CanonicalStore
	var exporters []ports.Exporter

	deps := cli.Deps{
		Sync:             sync.New(sources, store, log),
		Export:           export.New(store, exporters, log),
		DefaultRoots:     paths.vendorRoots,
		DefaultExportOut: paths.exportOut,
		Level:            level,
	}

	if err := cli.NewRoot(deps).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agent-logs-extractor:", err)
		os.Exit(1)
	}
}

// resolvedPaths are the tool's default on-disk locations, resolved from the
// environment (README "File layout" / "Sandboxing").
type resolvedPaths struct {
	vendorRoots map[model.Vendor]string
	exportOut   string
}

// defaultPaths resolves the tool's default paths from
// AGENT_LOGS_EXTRACTOR_HOME (falling back to the user's home directory),
// matching the README. It is pure path arithmetic — no directory creation,
// no other I/O — and unexercised by tests here; #8 owns its acceptance.
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
		exportOut: filepath.Join(home, ".local", "share", "agent-logs-extractor", "export", "logs.duckdb"),
	}
}
