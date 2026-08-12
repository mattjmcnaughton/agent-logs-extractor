package ports

import "context"

// ExportRequest is one materialization of the canonical store into a sink.
type ExportRequest struct {
	// StoreRoot is the canonical store to read.
	StoreRoot string
	// Out is the destination path for the sink's output.
	Out string
}

// Exporter materializes the canonical store into one sink. Implementations
// write to a temp path and rename over Out, so a failed export never leaves
// a half-written file and always leaves any previous output intact.
type Exporter interface {
	// Name is the sink's name, matching its `export <name>` subcommand.
	Name() string

	// Export materializes the store at req.StoreRoot into req.Out.
	Export(ctx context.Context, req ExportRequest) error
}
