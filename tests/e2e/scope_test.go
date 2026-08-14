//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// AC-SCOPE-01
func TestAC_SCOPE_01_SourcesAreReadOnly(t *testing.T) {
	s := newSandbox(t)
	src := t.TempDir()
	copyTree(t, logfixture.ClaudeRoot(), src)

	before := hashTree(t, src)
	res := s.run("sync", "--claude-path", src)
	wantCode(t, res, 0)
	after := hashTree(t, src)

	if before != after {
		t.Errorf("source tree hash changed across a sync: %s -> %s (sync must never write to a vendor source directory)", before, after)
	}
}

// AC-SCOPE-02
func TestAC_SCOPE_02_NoConfigFile(t *testing.T) {
	s := newSandbox(t)
	configPath := filepath.Join(s.home, ".config", "agent-logs-extractor", "config.yaml")
	garbage := "this is not valid config and must never be parsed\n: : : not: yaml: either"
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(garbage), 0o644); err != nil {
		t.Fatalf("writing %s: %v", configPath, err)
	}

	res := s.run("sync")
	wantCode(t, res, 0)

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading %s: %v", configPath, err)
	}
	if string(got) != garbage {
		t.Errorf("%s changed after sync: %q, want unchanged %q (this tool has no config file at all — MVP has none)", configPath, got, garbage)
	}
}
