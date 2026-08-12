package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/version"
)

func executeRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRoot(Deps{})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestRootRegistersTheDocumentedCommandSurface(t *testing.T) {
	root := NewRoot(Deps{})

	for _, path := range [][]string{{"version"}, {"sync"}, {"export"}, {"export", "duckdb"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Errorf("root.Find(%v): %v", path, err)
			continue
		}
		if cmd.Name() != path[len(path)-1] {
			t.Errorf("root.Find(%v) resolved to %q, want %q", path, cmd.Name(), path[len(path)-1])
		}
	}

	syncCmd, _, err := root.Find([]string{"sync"})
	if err != nil {
		t.Fatalf("root.Find([sync]): %v", err)
	}
	for _, flag := range []string{"vendor", "claude-path", "codex-path"} {
		if syncCmd.Flags().Lookup(flag) == nil {
			t.Errorf("sync is missing flag --%s", flag)
		}
	}

	duckdbCmd, _, err := root.Find([]string{"export", "duckdb"})
	if err != nil {
		t.Fatalf("root.Find([export duckdb]): %v", err)
	}
	if duckdbCmd.Flags().Lookup("out") == nil {
		t.Error("export duckdb is missing flag --out")
	}
}

func TestVersionSubcommandPrintsVersion(t *testing.T) {
	stdout, _, err := executeRoot(t, "version")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !strings.Contains(stdout, version.Version) {
		t.Errorf("stdout %q does not contain version %q", stdout, version.Version)
	}
}

func TestExecuteRootExportWithNoSinkErrors(t *testing.T) {
	_, _, err := executeRoot(t, "export")
	if err == nil {
		t.Fatal("export: want error, got nil")
	}
	if !strings.Contains(err.Error(), "duckdb") {
		t.Errorf("export err = %q, want it to name duckdb", err.Error())
	}
}

func TestExecuteRootExportWithUnknownSinkErrors(t *testing.T) {
	_, _, err := executeRoot(t, "export", "bogus")
	if err == nil {
		t.Fatal("export bogus: want error, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("export bogus err = %q, want it to name the bad sink", err.Error())
	}
}

func TestExecuteRootSyncWithUnknownVendorErrors(t *testing.T) {
	_, _, err := executeRoot(t, "sync", "--vendor", "bogus")
	if err == nil {
		t.Fatal("sync --vendor bogus: want error, got nil")
	}
	for _, want := range []string{"claude", "codex", "bogus"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("sync --vendor bogus err = %q, want it to mention %q", err.Error(), want)
		}
	}
}

func TestExecuteRootWithInvalidLogLevelErrors(t *testing.T) {
	_, _, err := executeRoot(t, "--log-level", "bogus", "version")
	if err == nil {
		t.Fatal("--log-level bogus: want error, got nil")
	}
}
