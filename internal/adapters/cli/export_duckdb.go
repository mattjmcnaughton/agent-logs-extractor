package cli

import (
	"cmp"

	"github.com/spf13/cobra"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/export"
)

func newExportDuckDBCmd(deps Deps) *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "duckdb",
		Args:  cobra.NoArgs,
		Short: "Materialize the canonical store as a DuckDB database file",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := cmp.Or(out, deps.DefaultExportOut)
			// Sink derives from the command's own name, matching Use
			// below, rather than repeating "duckdb" as a second literal
			// that export.New's Name()-keyed lookup must also match.
			return deps.Export.Run(cmd.Context(), export.Request{Sink: cmd.Name(), Out: resolved})
		},
	}

	cmd.Flags().StringVar(&out, "out", "", "Destination path for the DuckDB file")

	return cmd
}
