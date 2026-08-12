package cli

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/version"
)

func NewRoot() *cobra.Command {
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

	viper.SetEnvPrefix("AGENT_LOGS_EXTRACTOR")
	viper.AutomaticEnv()

	root.AddCommand(
		newExampleCmd(),
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
