package cli

import (
	"fmt"
	"strings"

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
				return fmt.Errorf("export requires a sink: %s", strings.Join(sinkNames(cmd), ", "))
			}
			return fmt.Errorf("unknown export sink %q", args[0])
		},
	}

	cmd.AddCommand(newExportDuckDBCmd(deps))

	return cmd
}

// sinkNames lists the export command's registered sink subcommands, so the
// "export requires a sink" message derives from what is actually
// registered rather than a literal that would go stale the moment a
// second sink (parquet, sqlite) lands.
func sinkNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	return names
}
