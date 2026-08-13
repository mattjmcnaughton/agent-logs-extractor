package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// TestDefaultPaths pins defaultPaths' path arithmetic under both an
// explicit AGENT_LOGS_EXTRACTOR_HOME and the unset (falls back to
// os.UserHomeDir) case.
func TestDefaultPaths(t *testing.T) {
	t.Run("AGENT_LOGS_EXTRACTOR_HOME set", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", dir)

		got := defaultPaths()

		wantClaude := filepath.Join(dir, ".claude")
		if got.vendorRoots[model.VendorClaude] != wantClaude {
			t.Errorf("vendorRoots[claude] = %q, want %q", got.vendorRoots[model.VendorClaude], wantClaude)
		}
		wantCodex := filepath.Join(dir, ".codex")
		if got.vendorRoots[model.VendorCodex] != wantCodex {
			t.Errorf("vendorRoots[codex] = %q, want %q", got.vendorRoots[model.VendorCodex], wantCodex)
		}
		wantStore := filepath.Join(dir, ".local", "share", "agent-logs-extractor", "store")
		if got.storeRoot != wantStore {
			t.Errorf("storeRoot = %q, want %q", got.storeRoot, wantStore)
		}
		wantExport := filepath.Join(dir, ".local", "share", "agent-logs-extractor", "export", "logs.duckdb")
		if got.exportOut != wantExport {
			t.Errorf("exportOut = %q, want %q", got.exportOut, wantExport)
		}
	})

	t.Run("AGENT_LOGS_EXTRACTOR_HOME unset falls back to the user's home directory", func(t *testing.T) {
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", "")

		got := defaultPaths()

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory resolvable in this environment: %v", err)
		}

		wantClaude := filepath.Join(home, ".claude")
		if got.vendorRoots[model.VendorClaude] != wantClaude {
			t.Errorf("vendorRoots[claude] = %q, want %q", got.vendorRoots[model.VendorClaude], wantClaude)
		}
		wantStore := filepath.Join(home, ".local", "share", "agent-logs-extractor", "store")
		if got.storeRoot != wantStore {
			t.Errorf("storeRoot = %q, want %q", got.storeRoot, wantStore)
		}
	})
}
