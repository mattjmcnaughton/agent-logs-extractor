package cli

import (
	"reflect"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
)

func TestSelectedVendors(t *testing.T) {
	cases := []struct {
		name    string
		vendor  string
		want    []model.Vendor
		wantErr bool
	}{
		{name: "empty selects both vendors", vendor: "", want: []model.Vendor{model.VendorClaude, model.VendorCodex}},
		{name: "claude selects only claude", vendor: "claude", want: []model.Vendor{model.VendorClaude}},
		{name: "codex selects only codex", vendor: "codex", want: []model.Vendor{model.VendorCodex}},
		{name: "unknown vendor errors", vendor: "bogus", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectedVendors(tc.vendor)
			if tc.wantErr {
				if err == nil {
					t.Fatal("selectedVendors: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("selectedVendors: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selectedVendors(%q) = %v, want %v", tc.vendor, got, tc.want)
			}
		})
	}
}

func TestSyncRequest(t *testing.T) {
	defaults := map[model.Vendor]string{
		model.VendorClaude: "/default/claude",
		model.VendorCodex:  "/default/codex",
	}

	cases := []struct {
		name      string
		vendors   []model.Vendor
		overrides map[model.Vendor]string
		want      sync.Request
	}{
		{
			name:      "neither path given falls back to defaults for both vendors",
			vendors:   []model.Vendor{model.VendorClaude, model.VendorCodex},
			overrides: map[model.Vendor]string{},
			want: sync.Request{Sources: []sync.SourceRequest{
				{Vendor: model.VendorClaude, Root: "/default/claude"},
				{Vendor: model.VendorCodex, Root: "/default/codex"},
			}},
		},
		{
			name:    "only --claude-path overrides claude; codex keeps its default",
			vendors: []model.Vendor{model.VendorClaude, model.VendorCodex},
			overrides: map[model.Vendor]string{
				model.VendorClaude: "/override/claude",
			},
			want: sync.Request{Sources: []sync.SourceRequest{
				{Vendor: model.VendorClaude, Root: "/override/claude"},
				{Vendor: model.VendorCodex, Root: "/default/codex"},
			}},
		},
		{
			name:    "both --claude-path and --codex-path override their defaults",
			vendors: []model.Vendor{model.VendorClaude, model.VendorCodex},
			overrides: map[model.Vendor]string{
				model.VendorClaude: "/override/claude",
				model.VendorCodex:  "/override/codex",
			},
			want: sync.Request{Sources: []sync.SourceRequest{
				{Vendor: model.VendorClaude, Root: "/override/claude"},
				{Vendor: model.VendorCodex, Root: "/override/codex"},
			}},
		},
		{
			name:    "--vendor codex ignores a --claude-path override entirely",
			vendors: []model.Vendor{model.VendorCodex},
			overrides: map[model.Vendor]string{
				model.VendorClaude: "/override/claude",
			},
			want: sync.Request{Sources: []sync.SourceRequest{
				{Vendor: model.VendorCodex, Root: "/default/codex"},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := syncRequest(tc.vendors, tc.overrides, defaults)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("syncRequest(%v, %v, defaults) = %+v, want %+v", tc.vendors, tc.overrides, got, tc.want)
			}
		})
	}
}
