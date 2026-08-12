// Package cli is the driving adapter: cobra commands as thin shims over the
// use cases in internal/core/. One file per subcommand.
package cli

import (
	"fmt"
	"log/slog"

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
	// Level backs the one logger wiring injects into the use cases
	// (sync.New, export.New). Wiring seeds it from
	// AGENT_LOGS_EXTRACTOR_LOG_LEVEL, else info; --log-level, if passed,
	// overrides it in PersistentPreRunE. There is exactly one logger and
	// one level var — the flag and the env var both resolve into it,
	// rather than each driving a logger of its own.
	Level *slog.LevelVar
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
			return applyLogLevel(deps.Level, logLevel)
		},
	}

	root.PersistentFlags().StringVar(
		&logLevel,
		"log-level",
		"",
		"Log level (debug, info, warn, error); overrides AGENT_LOGS_EXTRACTOR_LOG_LEVEL if set, defaults to info",
	)

	root.AddCommand(
		newVersionCmd(),
		newSyncCmd(deps),
		newExportCmd(deps),
	)

	return root
}

// ParseLevel parses s as a slog.Level ("debug", "info", "warn", "error",
// case-insensitive, matching slog.Level.UnmarshalText). It is the single
// level parser shared by both entry points that accept a level from the
// outside world — wiring's AGENT_LOGS_EXTRACTOR_LOG_LEVEL env var and the
// --log-level flag below — so the two can never drift into different
// parsing rules. The two entry points differ only in what an invalid s
// means to them: wiring treats a bad env var as non-fatal (warn and fall
// back to info), while applyLogLevel treats a bad flag as a hard error,
// which is correct for a value the user typed on this exact invocation.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(s))
	return l, err
}

// applyLogLevel parses s and sets it on level, when s is non-empty. An
// empty s (the flag was not passed) leaves level exactly as wiring seeded
// it from AGENT_LOGS_EXTRACTOR_LOG_LEVEL. A nil level (as in tests that
// construct Deps{} directly) is a safe no-op rather than a panic; an
// invalid s is a real error, not silently swallowed to info.
func applyLogLevel(level *slog.LevelVar, s string) error {
	if s == "" {
		return nil
	}
	l, err := ParseLevel(s)
	if err != nil {
		return fmt.Errorf("invalid --log-level %q: %w", s, err)
	}
	if level != nil {
		level.Set(l)
	}
	return nil
}
