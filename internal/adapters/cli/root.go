// Package cli is the driving adapter: cobra commands as thin shims over the
// use cases in internal/core/. One file per subcommand.
package cli

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/export"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/version"
)

// Deps holds the use cases and wiring-resolved defaults the CLI shims need.
// Flags override the defaults; nothing here is read from a config file —
// the MVP has none.
type Deps struct {
	Sync   *sync.Sync
	Export *export.Export
	// DefaultRoots are the per-vendor log roots resolved in wiring
	// (AGENT_LOGS_EXTRACTOR_HOME, else the user's home directory);
	// --claude-path / --codex-path override them.
	DefaultRoots map[model.Vendor]string
	// DefaultExportOut is the default `export duckdb` destination resolved
	// in wiring; --out overrides it.
	DefaultExportOut string
}

func NewRoot(deps Deps) *cobra.Command {
	var logLevel string

	root := &cobra.Command{
		Use:           "agent-logs-extractor",
		Short:         "Parse AI coding agent conversation logs (Claude Code, Codex) into a unified, queryable data model",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return setupLogging(logLevel)
		},
	}

	root.PersistentFlags().StringVar(
		&logLevel,
		"log-level",
		"info",
		"Log level (debug, info, warn, error)",
	)

	root.AddCommand(
		newVersionCmd(),
		newSyncCmd(deps),
		newExportCmd(deps),
	)

	return root
}

func setupLogging(level string) error {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})
	slog.SetDefault(slog.New(handler))
	return nil
}
