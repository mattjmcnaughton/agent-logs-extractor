//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// claudeScratchpadProject is the single-turn fixture's project directory
// name under logfixture.ClaudeProjectsDir() — logfixture.go exports named
// constants for the sidechain and tool-error projects
// (ClaudeSidechainProject / ClaudeToolErrorProject) but not this one, so
// it is named here instead of repeating the literal across test files.
const claudeScratchpadProject = "-tmp-claude-0--home-user-94ba8eae-3476-51cf-a4d4-b0b5339db735-scratchpad-fixture-project"

// sandbox is the per-scenario clean slate docs/acceptance.md §1.4
// describes: an isolated home directory standing in for both $HOME and
// (by default) $AGENT_LOGS_EXTRACTOR_HOME, so every scenario starts from
// nothing and can never see the host's, or another test's, files.
type sandbox struct {
	t *testing.T
	// home is this scenario's isolated home directory. It backs both HOME
	// and, unless noHomeVar is set, AGENT_LOGS_EXTRACTOR_HOME — never the
	// real host HOME (D10: every e2e run sets both).
	home string
	// noHomeVar, when true, omits AGENT_LOGS_EXTRACTOR_HOME from the child
	// process environment entirely (as opposed to setting it to home).
	// AC-SANDBOX-03's one deliberate use: HOME still guards the run (D10),
	// but AGENT_LOGS_EXTRACTOR_HOME's absence is exactly the scenario under
	// test.
	noHomeVar bool
	// extraEnv layers additional KEY=VALUE pairs onto every run from this
	// point on, overriding HOME/AGENT_LOGS_EXTRACTOR_HOME/PATH/TMPDIR if a
	// test sets those keys explicitly.
	extraEnv map[string]string
}

// newSandbox returns a sandbox rooted at a fresh t.TempDir(), with
// AGENT_LOGS_EXTRACTOR_HOME defaulted on (D10's hermetic guard).
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	return &sandbox{t: t, home: t.TempDir(), extraEnv: map[string]string{}}
}

// result captures one binary invocation.
type result struct {
	stdout string
	stderr string
	code   int
}

// run invokes the binary under test with args, in this sandbox's
// environment (see env()).
func (s *sandbox) run(args ...string) result {
	return s.runWithEnv(nil, args...)
}

// runWithEnv is run, with overrides layered on top of this sandbox's own
// environment for this one invocation only (never persisted onto the
// sandbox itself) — e.g. AC-EXPORT-04's PATH scrub.
func (s *sandbox) runWithEnv(overrides map[string]string, args ...string) result {
	s.t.Helper()
	cmd := exec.Command(alxbin, args...)
	cmd.Dir = s.home
	cmd.Env = s.buildEnv(overrides)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			s.t.Fatalf("running %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// buildEnv assembles the child environment from scratch: PATH and TMPDIR
// only from the host (enough for the binary and any subprocess it shells
// out to — duckdb — to function), plus HOME=s.home and, unless
// s.noHomeVar, AGENT_LOGS_EXTRACTOR_HOME=s.home. s.extraEnv is layered on
// top, then overrides for this one call. Built as a map, not an appended
// slice, so a later override actually replaces an earlier value (a
// duplicate-key environ slice's winner is libc-dependent) rather than
// merely shadowing it.
func (s *sandbox) buildEnv(overrides map[string]string) []string {
	env := map[string]string{}
	if v, ok := os.LookupEnv("PATH"); ok {
		env["PATH"] = v
	}
	if v, ok := os.LookupEnv("TMPDIR"); ok {
		env["TMPDIR"] = v
	}
	env["HOME"] = s.home
	if !s.noHomeVar {
		env["AGENT_LOGS_EXTRACTOR_HOME"] = s.home
	}
	for k, v := range s.extraEnv {
		env[k] = v
	}
	for k, v := range overrides {
		env[k] = v
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// setEnv adds or overrides one KEY=VALUE pair on every subsequent run from
// this sandbox.
func (s *sandbox) setEnv(key, value string) {
	s.extraEnv[key] = value
}

// unsetAgentLogsExtractorHome makes every subsequent run omit
// AGENT_LOGS_EXTRACTOR_HOME entirely, while HOME still points at this
// sandbox's isolated directory (D10) — AC-SANDBOX-03's scenario.
func (s *sandbox) unsetAgentLogsExtractorHome() {
	s.noHomeVar = true
}

// claudeProjectsDir is where this sandbox's ~/.claude/projects lives —
// always under s.home, regardless of noHomeVar, since HOME (which governs
// vendor-root resolution once AGENT_LOGS_EXTRACTOR_HOME is unset) always
// points here.
func (s *sandbox) claudeProjectsDir() string {
	return filepath.Join(s.home, ".claude", "projects")
}

// seedClaude copies each named fixture project directory (from
// logfixture.ClaudeProjectsDir()) into this sandbox's ~/.claude/projects/,
// so a sync against the sandbox's default (or --claude-path-overridden)
// root sees exactly those sessions.
func (s *sandbox) seedClaude(projects ...string) {
	s.t.Helper()
	for _, p := range projects {
		src := filepath.Join(logfixture.ClaudeProjectsDir(), p)
		dst := filepath.Join(s.claudeProjectsDir(), p)
		copyTree(s.t, src, dst)
	}
}

// seedFullClaudeTree seeds every project directory in the committed Claude
// fixture (the same tree sync_integration_test.go and duckdbcli's
// integration tier read directly): 3 sessions, 15 messages, 4 tool calls,
// 23 records skipped (docs/acceptance.md §1.3 pins these numbers).
func (s *sandbox) seedFullClaudeTree() {
	s.t.Helper()
	entries, err := os.ReadDir(logfixture.ClaudeProjectsDir())
	if err != nil {
		s.t.Fatalf("reading %s: %v", logfixture.ClaudeProjectsDir(), err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	s.seedClaude(names...)
}

// dataHome mirrors main.go's defaultPaths() precedence for the data
// directory exactly, given this sandbox's own current env (noHomeVar,
// extraEnv's XDG_DATA_HOME if set): AGENT_LOGS_EXTRACTOR_HOME wins outright
// when present; otherwise an absolute XDG_DATA_HOME wins; otherwise
// <home>/.local/share.
func (s *sandbox) dataHome() string {
	envHome := ""
	if !s.noHomeVar {
		envHome = s.home
	}
	home := envHome
	if home == "" {
		home = s.home // HOME is always s.home in this sandbox (D10)
	}
	dataHome := filepath.Join(home, ".local", "share")
	if envHome == "" {
		if xdg, ok := s.extraEnv["XDG_DATA_HOME"]; ok && filepath.IsAbs(xdg) {
			dataHome = xdg
		}
	}
	return dataHome
}

// storeRoot is where this sandbox's sync writes the canonical store, given
// its current env.
func (s *sandbox) storeRoot() string {
	return filepath.Join(s.dataHome(), "agent-logs-extractor", "store")
}

// defaultExportPath is where a bare `export duckdb` (no --out) writes, given
// this sandbox's current env.
func (s *sandbox) defaultExportPath() string {
	return filepath.Join(s.dataHome(), "agent-logs-extractor", "export", "logs.duckdb")
}

// pathWithoutDuckDB rebuilds PATH from the host's own PATH with every
// directory that contains a `duckdb` executable removed — scrubbed
// unconditionally, so AC-EXPORT-04 proves the missing-binary path
// regardless of whether the host (or the container image) actually has
// duckdb installed.
func pathWithoutDuckDB() string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	kept := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if hasExecutable(d, "duckdb") {
			continue
		}
		kept = append(kept, d)
	}
	return joinPathList(kept)
}

func hasExecutable(dir, name string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !fi.IsDir()
}

func joinPathList(dirs []string) string {
	out := ""
	for i, d := range dirs {
		if i > 0 {
			out += string(os.PathListSeparator)
		}
		out += d
	}
	return out
}

// copyTree recursively copies src to dst, so a test can seed a mutable
// working copy of a committed fixture without ever writing into
// internal/testing/logfixture/ itself.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying %s to %s: %v", src, dst, err)
	}
}
