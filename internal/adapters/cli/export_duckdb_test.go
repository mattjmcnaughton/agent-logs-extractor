package cli

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/duckdbcli"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/export"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/fakes"
)

// TestNameMatchesTheSubcommand pins duckdbcli.SinkName against the `export
// duckdb` subcommand's own Use string. export.Run's sink lookup is keyed by
// cmd.Name() (export_duckdb.go), so these two must never drift apart: a
// mismatch here means every real invocation of `export duckdb` would 404 on
// export.ErrUnknownSink despite duckdbcli being registered.
func TestNameMatchesTheSubcommand(t *testing.T) {
	cmd := newExportDuckDBCmd(Deps{})
	if cmd.Use != duckdbcli.SinkName {
		t.Errorf("export duckdb subcommand Use = %q, want it to equal duckdbcli.SinkName %q", cmd.Use, duckdbcli.SinkName)
	}
}

// TestExportDuckDBCmdResolvesOutAndPrintsIt drives the real cobra command
// over a fake exporter end to end: --out overrides DefaultExportOut, the
// resolved path reaches the exporter's ExportRequest, and a successful run
// prints "wrote <path>" to stdout.
func TestExportDuckDBCmdResolvesOutAndPrintsIt(t *testing.T) {
	t.Run("--out overrides the default", func(t *testing.T) {
		store := fakes.NewCanonicalStore()
		exp := fakes.NewExporter("duckdb")
		deps := Deps{
			Export:           export.New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler)),
			DefaultExportOut: "/default/logs.duckdb",
		}

		root := NewRoot(deps)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"export", "duckdb", "--out", "/custom/logs.duckdb"})
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		if want := "wrote /custom/logs.duckdb\n"; out.String() != want {
			t.Errorf("stdout = %q, want %q", out.String(), want)
		}
		if len(exp.Requests) != 1 {
			t.Fatalf("exporter recorded %d requests, want 1", len(exp.Requests))
		}
		if got := exp.Requests[0].Out; got != "/custom/logs.duckdb" {
			t.Errorf("ExportRequest.Out = %q, want %q", got, "/custom/logs.duckdb")
		}
	})

	t.Run("no --out falls back to DefaultExportOut", func(t *testing.T) {
		store := fakes.NewCanonicalStore()
		exp := fakes.NewExporter("duckdb")
		deps := Deps{
			Export:           export.New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler)),
			DefaultExportOut: "/default/logs.duckdb",
		}

		root := NewRoot(deps)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"export", "duckdb"})
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		if want := "wrote /default/logs.duckdb\n"; out.String() != want {
			t.Errorf("stdout = %q, want %q", out.String(), want)
		}
	})
}

// TestExportDuckDBCmdPrintsNothingOnFailure pins that a failing export
// never prints "wrote <path>" — the message would be a lie if the sink
// itself failed.
func TestExportDuckDBCmdPrintsNothingOnFailure(t *testing.T) {
	store := fakes.NewCanonicalStore()
	exp := fakes.NewExporter("duckdb")
	exp.Err = errors.New("boom")
	deps := Deps{
		Export:           export.New(store, []ports.Exporter{exp}, slog.New(slog.DiscardHandler)),
		DefaultExportOut: "/default/logs.duckdb",
	}

	root := NewRoot(deps)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"export", "duckdb"})
	if err := root.Execute(); err == nil {
		t.Fatal("Execute: want error, got nil")
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty on a failed export", out.String())
	}
}
