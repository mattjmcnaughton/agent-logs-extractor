//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
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

// AC-CLI-07. Uses --log-level debug's own observable side effect (the
// debug-only skip-breakdown lines AC-SKIP-02 already pins) as a proxy for
// "which level actually took effect", since the level itself has no other
// externally visible signal.
func TestAC_CLI_07_LogLevelEnvVar(t *testing.T) {
	// --log-level wins over AGENT_LOGS_EXTRACTOR_LOG_LEVEL when both are
	// set: the env var alone would not produce the debug breakdown.
	flagWins := newSandbox(t)
	flagWins.setEnv("AGENT_LOGS_EXTRACTOR_LOG_LEVEL", "warn")
	res := flagWins.run("sync", "--claude-path", logfixture.ClaudeRoot(), "--log-level", "debug")
	wantCode(t, res, 0)
	wantStderrContains(t, res, `msg="sync: skipped records"`)

	// AGENT_LOGS_EXTRACTOR_LOG_LEVEL alone (no flag) governs the level too.
	envOnly := newSandbox(t)
	envOnly.setEnv("AGENT_LOGS_EXTRACTOR_LOG_LEVEL", "debug")
	res = envOnly.run("sync", "--claude-path", logfixture.ClaudeRoot())
	wantCode(t, res, 0)
	wantStderrContains(t, res, `msg="sync: skipped records"`)

	// An invalid env var value, unlike an invalid --log-level (AC-CLI-06),
	// is a warning, not a hard error: exit 0, falls back to info.
	invalid := newSandbox(t)
	invalid.setEnv("AGENT_LOGS_EXTRACTOR_LOG_LEVEL", "bogus")
	res = invalid.run("sync", "--claude-path", logfixture.ClaudeRoot())
	wantCode(t, res, 0)
	wantStderrContains(t, res, `invalid AGENT_LOGS_EXTRACTOR_LOG_LEVEL "bogus"`)
	if strings.Contains(res.stderr, "reason=") {
		t.Errorf("stderr = %q, want no debug-only skip breakdown after an invalid env var falls back to info", res.stderr)
	}
}
