package cli

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/fakes"
)

func TestSelectedVendors(t *testing.T) {
	both := []model.Vendor{model.VendorClaude, model.VendorCodex}
	claudeOnly := []model.Vendor{model.VendorClaude}

	cases := []struct {
		name            string
		vendor          string
		available       []model.Vendor
		want            []model.Vendor
		wantErr         bool
		wantErrContains []string
	}{
		{name: "empty with both available selects both", vendor: "", available: both, want: both},
		{name: "empty with only claude available selects only claude", vendor: "", available: claudeOnly, want: claudeOnly},
		{name: "empty with nothing available errors", vendor: "", available: nil, wantErr: true},
		{name: "claude selects only claude", vendor: "claude", available: both, want: claudeOnly},
		{
			name:   "codex with only claude available errors naming both",
			vendor: "codex", available: claudeOnly, wantErr: true,
			wantErrContains: []string{"codex", "claude"},
		},
		{
			name:   "unknown vendor errors regardless of availability",
			vendor: "bogus", available: nil, wantErr: true,
			wantErrContains: []string{"bogus"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectedVendors(tc.vendor, tc.available)
			if tc.wantErr {
				if err == nil {
					t.Fatal("selectedVendors: want error, got nil")
				}
				for _, want := range tc.wantErrContains {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("selectedVendors err = %q, want it to mention %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("selectedVendors: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selectedVendors(%q, %v) = %v, want %v", tc.vendor, tc.available, got, tc.want)
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

func TestFormatSyncSummary(t *testing.T) {
	cases := []struct {
		name string
		in   sync.Summary
		want string
	}{
		{name: "empty summary formats to empty string", in: sync.Summary{}, want: ""},
		{
			name: "typical multi-record vendor",
			in: sync.Summary{Vendors: []sync.VendorSummary{{
				Vendor: model.VendorClaude, Sessions: 3, Messages: 15, ToolCalls: 4,
				Skipped: model.SkipCounts{model.SkipBookkeeping: 23},
			}}},
			want: "claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped\n",
		},
		{
			name: "singular counts",
			in: sync.Summary{Vendors: []sync.VendorSummary{{
				Vendor: model.VendorClaude, Sessions: 1, Messages: 1, ToolCalls: 1,
				Skipped: model.SkipCounts{model.SkipMalformedLine: 1},
			}}},
			want: "claude: 1 session, 1 message, 1 tool call, 1 record skipped\n",
		},
		{
			name: "zero counts, no skips",
			in: sync.Summary{Vendors: []sync.VendorSummary{{
				Vendor: model.VendorCodex,
			}}},
			want: "codex: 0 sessions, 0 messages, 0 tool calls, 0 records skipped\n",
		},
		{
			name: "files unreadable appended only when non-zero",
			in: sync.Summary{Vendors: []sync.VendorSummary{{
				Vendor: model.VendorClaude, Sessions: 2, Messages: 8, ToolCalls: 1,
				Skipped:         model.SkipCounts{model.SkipMalformedLine: 3},
				FilesUnreadable: 1,
			}}},
			want: "claude: 2 sessions, 8 messages, 1 tool call, 3 records skipped, 1 file unreadable\n",
		},
		{
			name: "multiple files unreadable pluralizes",
			in: sync.Summary{Vendors: []sync.VendorSummary{{
				Vendor: model.VendorClaude, FilesUnreadable: 2,
			}}},
			want: "claude: 0 sessions, 0 messages, 0 tool calls, 0 records skipped, 2 files unreadable\n",
		},
		{
			name: "multiple vendors print one line each in order",
			in: sync.Summary{Vendors: []sync.VendorSummary{
				{Vendor: model.VendorClaude, Sessions: 1, Messages: 1},
				{Vendor: model.VendorCodex, Sessions: 2, Messages: 2},
			}},
			want: "claude: 1 session, 1 message, 0 tool calls, 0 records skipped\n" +
				"codex: 2 sessions, 2 messages, 0 tool calls, 0 records skipped\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSyncSummary(tc.in)
			if got != tc.want {
				t.Errorf("formatSyncSummary(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSyncCommandPrintsTheSummary drives the real cobra command over
// fakes end to end: flags parsed, sync.Sync.Run actually called, and the
// formatted summary lands on stdout exactly as formatSyncSummary would
// produce it.
func TestSyncCommandPrintsTheSummary(t *testing.T) {
	src := fakes.NewConversationSource(model.VendorClaude)
	src.Seed("/claude", "/claude/a.jsonl", model.SessionDoc{
		Session:  model.Session{Vendor: model.VendorClaude, SessionID: "claude:a"},
		Messages: []model.Message{{}, {}},
	}, model.ParseStats{})

	s := sync.New([]ports.ConversationSource{src}, fakes.NewCanonicalStore(), slog.New(slog.DiscardHandler))
	deps := Deps{
		Sync:         s,
		DefaultRoots: map[model.Vendor]string{model.VendorClaude: "/claude"},
	}

	root := NewRoot(deps)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"sync", "--vendor", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := "claude: 1 session, 2 messages, 0 tool calls, 0 records skipped\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

// TestSyncCommandWithAnUnavailableVendorErrors drives `sync --vendor
// codex` end to end against a Sync that only has a claude source
// registered, proving selectedVendors' availability check is actually
// wired to deps.Sync.Vendors() at the command level, not just unit-tested
// in isolation.
func TestSyncCommandWithAnUnavailableVendorErrors(t *testing.T) {
	src := fakes.NewConversationSource(model.VendorClaude)
	s := sync.New([]ports.ConversationSource{src}, fakes.NewCanonicalStore(), slog.New(slog.DiscardHandler))
	deps := Deps{Sync: s}

	root := NewRoot(deps)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"sync", "--vendor", "codex"})
	err := root.Execute()
	if err == nil {
		t.Fatal("Execute: want error, got nil")
	}
	for _, want := range []string{"codex", "claude"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %q", err.Error(), want)
		}
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty (no summary on a rejected vendor)", out.String())
	}
}
