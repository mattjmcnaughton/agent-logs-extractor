package cli

import (
	"log/slog"

	"github.com/spf13/cobra"
)

func newExampleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "example [name]",
		Short: "Example command demonstrating the command pattern",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runExample,
	}
}

// runExample is the CLI entry point. It is a thin I/O wrapper: parse args,
// call business logic, emit output.
func runExample(cmd *cobra.Command, args []string) error {
	name := "world"
	if len(args) > 0 {
		name = args[0]
	}

	message := doExample(name)
	slog.Info("example complete", "message", message)
	return nil
}

// doExample contains the business logic for the example command.
// Move real logic to an internal/services package as the project grows.
func doExample(name string) string {
	return "Hello, " + name + "!"
}
