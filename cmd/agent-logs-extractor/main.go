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
	log := newLogger()
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
	}

	if err := cli.NewRoot(deps).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agent-logs-extractor:", err)
		os.Exit(1)
	}
}

// newLogger builds the logger injected into adapters and use cases. The
// level comes from AGENT_LOGS_EXTRACTOR_LOG_LEVEL (the --log-level flag
// tunes the process-global default logger separately, in the CLI adapter).
func newLogger() *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(os.Getenv("AGENT_LOGS_EXTRACTOR_LOG_LEVEL"))); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// resolvedPaths are the tool's default on-disk locations, resolved from the
// environment (README "File layout" / "Sandboxing").
type resolvedPaths struct {
	vendorRoots map[model.Vendor]string
	// storeRoot is unused until #7 wires a CanonicalStore adapter.
	storeRoot string
	exportOut string
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
		storeRoot: filepath.Join(home, ".local", "share", "agent-logs-extractor", "store"),
		exportOut: filepath.Join(home, ".local", "share", "agent-logs-extractor", "export", "logs.duckdb"),
	}
}
