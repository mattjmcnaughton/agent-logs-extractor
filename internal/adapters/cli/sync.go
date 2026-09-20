package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

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
			vendors, err := selectedVendors(vendor, deps.Sync.Vendors())
			if err != nil {
				return err
			}

			overrides := map[model.Vendor]string{
				model.VendorClaude: claudePath,
				model.VendorCodex:  codexPath,
			}
			req := syncRequest(vendors, overrides, deps.DefaultRoots)

			summary, err := deps.Sync.Run(cmd.Context(), req)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), formatSyncSummary(summary))
			return err
		},
	}

	cmd.Flags().StringVar(&vendor, "vendor", "", fmt.Sprintf("Vendor to sync (%v); default is all registered vendors", deps.Sync.Vendors()))
	cmd.Flags().StringVar(&claudePath, "claude-path", "", "Override the Claude log directory")
	cmd.Flags().StringVar(&codexPath, "codex-path", "", "Override the Codex log directory")

	return cmd
}

// selectedVendors uses the registered source list for explicit selection and
// default fan-out, so adding another adapter needs no vendor switch here.
func selectedVendors(vendor string, available []model.Vendor) ([]model.Vendor, error) {
	if vendor == "" {
		if len(available) == 0 {
			return nil, fmt.Errorf("no vendor sources are available in this build")
		}
		return slices.Clone(available), nil
	}
	v := model.Vendor(vendor)
	if !slices.Contains(available, v) {
		return nil, fmt.Errorf("unknown vendor %q; available: %q", vendor, available)
	}
	return []model.Vendor{v}, nil
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

// formatSyncSummary renders one line per vendor, in the order Run reported
// them (request order): "<vendor>: N sessions, N messages, N tool calls, N
// records skipped", with ", N file(s) unreadable" appended only when
// FilesUnreadable is non-zero, so the common case stays a clean four-field
// line. An empty Summary (no vendors — Run only returns one for a Request
// with at least one source) formats to "". The full by-reason skip
// breakdown is not printed here; the use case already logs it at debug
// level (sync.Run's doc).
func formatSyncSummary(s sync.Summary) string {
	var b strings.Builder
	for _, v := range s.Vendors {
		fmt.Fprintf(&b, "%s: %s, %s, %s, %s skipped",
			v.Vendor,
			countNoun(v.Sessions, "session"),
			countNoun(v.Messages, "message"),
			countNoun(v.ToolCalls, "tool call"),
			countNoun(v.TotalSkipped(), "record"),
		)
		if v.FilesUnreadable > 0 {
			fmt.Fprintf(&b, ", %s unreadable", countNoun(v.FilesUnreadable, "file"))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// countNoun formats n alongside noun, pluralized with a trailing "s"
// unless n is exactly 1. Every noun this package passes in pluralizes
// regularly, so no irregular-plural table is needed.
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
