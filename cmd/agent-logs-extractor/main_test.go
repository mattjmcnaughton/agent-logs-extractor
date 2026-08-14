package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// TestDefaultPaths pins defaultPaths' path arithmetic across every
// AGENT_LOGS_EXTRACTOR_HOME / XDG_DATA_HOME combination (#9, D10).
func TestDefaultPaths(t *testing.T) {
	t.Run("AGENT_LOGS_EXTRACTOR_HOME set", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", dir)
		// XDG_DATA_HOME must be irrelevant here: AGENT_LOGS_EXTRACTOR_HOME
		// overrides it outright. Pinned to "" (not merely left alone) so
		// this case is deterministic regardless of the host environment.
		t.Setenv("XDG_DATA_HOME", "")

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
		t.Setenv("XDG_DATA_HOME", "")

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

	t.Run("AGENT_LOGS_EXTRACTOR_HOME set with an absolute XDG_DATA_HOME: HOME wins", func(t *testing.T) {
		homeDir := t.TempDir()
		xdgDir := t.TempDir()
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", homeDir)
		t.Setenv("XDG_DATA_HOME", xdgDir)

		got := defaultPaths()

		wantStore := filepath.Join(homeDir, ".local", "share", "agent-logs-extractor", "store")
		if got.storeRoot != wantStore {
			t.Errorf("storeRoot = %q, want %q (AGENT_LOGS_EXTRACTOR_HOME must override XDG_DATA_HOME entirely)", got.storeRoot, wantStore)
		}
		wantClaude := filepath.Join(homeDir, ".claude")
		if got.vendorRoots[model.VendorClaude] != wantClaude {
			t.Errorf("vendorRoots[claude] = %q, want %q", got.vendorRoots[model.VendorClaude], wantClaude)
		}
	})

	t.Run("no AGENT_LOGS_EXTRACTOR_HOME, absolute XDG_DATA_HOME: XDG wins for data paths", func(t *testing.T) {
		xdgDir := t.TempDir()
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", "")
		t.Setenv("XDG_DATA_HOME", xdgDir)

		got := defaultPaths()

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory resolvable in this environment: %v", err)
		}

		wantStore := filepath.Join(xdgDir, "agent-logs-extractor", "store")
		if got.storeRoot != wantStore {
			t.Errorf("storeRoot = %q, want %q", got.storeRoot, wantStore)
		}
		wantExport := filepath.Join(xdgDir, "agent-logs-extractor", "export", "logs.duckdb")
		if got.exportOut != wantExport {
			t.Errorf("exportOut = %q, want %q", got.exportOut, wantExport)
		}
		// vendorRoots always resolve from home, never from XDG_DATA_HOME.
		wantClaude := filepath.Join(home, ".claude")
		if got.vendorRoots[model.VendorClaude] != wantClaude {
			t.Errorf("vendorRoots[claude] = %q, want %q (must resolve from home, not XDG)", got.vendorRoots[model.VendorClaude], wantClaude)
		}
	})

	t.Run("no AGENT_LOGS_EXTRACTOR_HOME, relative XDG_DATA_HOME: falls back to ~/.local/share", func(t *testing.T) {
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", "")
		t.Setenv("XDG_DATA_HOME", filepath.Join("relative", "xdg", "path"))

		got := defaultPaths()

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory resolvable in this environment: %v", err)
		}

		wantStore := filepath.Join(home, ".local", "share", "agent-logs-extractor", "store")
		if got.storeRoot != wantStore {
			t.Errorf("storeRoot = %q, want %q (a relative XDG_DATA_HOME must not be honored)", got.storeRoot, wantStore)
		}
	})

	t.Run("no AGENT_LOGS_EXTRACTOR_HOME, empty XDG_DATA_HOME: falls back to ~/.local/share", func(t *testing.T) {
		t.Setenv("AGENT_LOGS_EXTRACTOR_HOME", "")
		t.Setenv("XDG_DATA_HOME", "")

		got := defaultPaths()

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory resolvable in this environment: %v", err)
		}

		wantStore := filepath.Join(home, ".local", "share", "agent-logs-extractor", "store")
		if got.storeRoot != wantStore {
			t.Errorf("storeRoot = %q, want %q", got.storeRoot, wantStore)
		}
	})
}
