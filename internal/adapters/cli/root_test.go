package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

func findCommand(cmds []*cobra.Command, name string) *cobra.Command {
	for _, c := range cmds {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func TestRootRegistersTheDocumentedCommandSurface(t *testing.T) {
	root := NewRoot(Deps{})

	for _, want := range []string{"version", "sync", "export"} {
		if findCommand(root.Commands(), want) == nil {
			t.Errorf("root is missing subcommand %q", want)
		}
	}

	syncCmd := findCommand(root.Commands(), "sync")
	if syncCmd == nil {
		t.Fatal("sync subcommand not found")
	}
	for _, flag := range []string{"vendor", "claude-path", "codex-path"} {
		if syncCmd.Flags().Lookup(flag) == nil {
			t.Errorf("sync is missing flag --%s", flag)
		}
	}

	exportCmd := findCommand(root.Commands(), "export")
	if exportCmd == nil {
		t.Fatal("export subcommand not found")
	}
	duckdbCmd := findCommand(exportCmd.Commands(), "duckdb")
	if duckdbCmd == nil {
		t.Fatal("export duckdb subcommand not found")
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
