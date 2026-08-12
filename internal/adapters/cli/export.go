package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newExportCmd(deps Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Materialize the canonical store into a sink",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A bare `export` or an unrecognized sink both exit non-zero,
			// rather than cobra's default help-and-exit-0 for a group with
			// no RunE.
			if len(args) == 0 {
				return fmt.Errorf("export requires a sink: duckdb")
			}
			return fmt.Errorf("unknown export sink %q", args[0])
		},
	}

	cmd.AddCommand(newExportDuckDBCmd(deps))

	return cmd
}
