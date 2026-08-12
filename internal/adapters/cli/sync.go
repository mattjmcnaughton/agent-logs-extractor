package cli

import (
	"cmp"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
)

func newSyncCmd(deps Deps) *cobra.Command {
	var vendor string
	var claudePath string
	var codexPath string

	cmd := &cobra.Command{
		Use:   "sync",
		Args:  cobra.NoArgs,
		Short: "Parse vendor logs into the canonical store",
		RunE: func(cmd *cobra.Command, args []string) error {
			vendors, err := selectedVendors(vendor)
			if err != nil {
				return err
			}

			overrides := map[model.Vendor]string{
				model.VendorClaude: claudePath,
				model.VendorCodex:  codexPath,
			}
			req := syncRequest(vendors, overrides, deps.DefaultRoots)

			// TODO(#8): format the ingest summary.
			_, err = deps.Sync.Run(cmd.Context(), req)
			return err
		},
	}

	cmd.Flags().StringVar(&vendor, "vendor", "", "Vendor to sync (claude, codex); default is all vendors")
	cmd.Flags().StringVar(&claudePath, "claude-path", "", "Override the Claude log directory")
	cmd.Flags().StringVar(&codexPath, "codex-path", "", "Override the Codex log directory")

	return cmd
}

// selectedVendors resolves the --vendor flag to the set of vendors to
// sync: both when empty, or the single named vendor.
func selectedVendors(vendor string) ([]model.Vendor, error) {
	switch vendor {
	case "":
		return []model.Vendor{model.VendorClaude, model.VendorCodex}, nil
	case string(model.VendorClaude):
		return []model.Vendor{model.VendorClaude}, nil
	case string(model.VendorCodex):
		return []model.Vendor{model.VendorCodex}, nil
	default:
		return nil, fmt.Errorf("unknown vendor %q: accepted values are %q, %q", vendor, model.VendorClaude, model.VendorCodex)
	}
}

// syncRequest maps vendors (the set --vendor selected), overrides
// (--claude-path / --codex-path, empty when not passed), and defaults
// (wiring's per-vendor roots) onto a sync.Request: one SourceRequest per
// vendor, each rooted at its override if one was given, else its default.
// A vendor not in vendors contributes nothing, so an override for a vendor
// --vendor excluded is silently ignored, matching the flag's documented
// scope.
func syncRequest(vendors []model.Vendor, overrides, defaults map[model.Vendor]string) sync.Request {
	req := sync.Request{}
	for _, v := range vendors {
		root := cmp.Or(overrides[v], defaults[v])
		req.Sources = append(req.Sources, sync.SourceRequest{Vendor: v, Root: root})
	}
	return req
}
