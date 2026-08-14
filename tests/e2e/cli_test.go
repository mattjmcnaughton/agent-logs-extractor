//go:build e2e

package e2e

import (
	"os"
	"testing"
)

// AC-CLI-01
func TestAC_CLI_01_VersionPrints(t *testing.T) {
	s := newSandbox(t)
	res := s.run("version")
	wantCode(t, res, 0)
	if res.stdout == "" {
		t.Errorf("stdout is empty, want a non-empty version string")
	}
}

// AC-CLI-02
func TestAC_CLI_02_NoArgsPrintsHelp(t *testing.T) {
	s := newSandbox(t)
	res := s.run()
	wantCode(t, res, 0)
	if res.stdout == "" {
		t.Errorf("stdout is empty, want help text (R2: bare invocation prints help to stdout, exits 0)")
	}
}

// AC-CLI-03
func TestAC_CLI_03_UnknownSubcommand(t *testing.T) {
	s := newSandbox(t)
	res := s.run("frobnicate")
	wantCode(t, res, 1)
	wantStderrContains(t, res, `unknown command "frobnicate"`)
}

// AC-CLI-04
func TestAC_CLI_04_ExportRequiresASink(t *testing.T) {
	s := newSandbox(t)
	res := s.run("export")
	wantCode(t, res, 1)
	wantStderrContains(t, res, "export requires a sink: duckdb")
}

// AC-CLI-05
func TestAC_CLI_05_UnknownExportSink(t *testing.T) {
	s := newSandbox(t)
	res := s.run("export", "bogus")
	wantCode(t, res, 1)
	wantStderrContains(t, res, `unknown export sink "bogus"`)
}

// AC-CLI-06
func TestAC_CLI_06_InvalidLogLevel(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--log-level", "bogus")
	wantCode(t, res, 1)
	wantStderrContains(t, res, `invalid --log-level "bogus"`)

	if _, err := os.Stat(s.storeRoot()); err == nil {
		t.Errorf("store root %s exists; an invalid --log-level must fail before any work starts", s.storeRoot())
	}
}
