package cli

import (
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
			resolved := out
			if resolved == "" {
				resolved = deps.DefaultExportOut
			}
			return deps.Export.Run(cmd.Context(), export.Request{Sink: "duckdb", Out: resolved})
		},
	}

	cmd.Flags().StringVar(&out, "out", "", "Destination path for the DuckDB file")

	return cmd
}
