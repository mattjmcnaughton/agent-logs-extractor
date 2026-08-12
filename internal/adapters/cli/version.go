package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the agent-logs-extractor version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// cobra's Println falls back to stderr; version belongs on stdout.
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.Version)
			return err
		},
	}
}
