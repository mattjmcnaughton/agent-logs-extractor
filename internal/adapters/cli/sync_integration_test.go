//go:build integration

package cli_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/cli"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/jsonlstore"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/export"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/sync"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

// TestSyncCommandOverTheClaudeFixtureTree is the ticket's literal
// invocation: the real cobra root, wired to the real claudesource and
// jsonlstore adapters on a real filesystem, run against the committed
// Claude fixture tree.
func TestSyncCommandOverTheClaudeFixtureTree(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	storeRoot := t.TempDir()

	store := jsonlstore.New(afero.NewOsFs(), storeRoot, log)
	src := claudesource.New(log)
	s := sync.New([]ports.ConversationSource{src}, store, log)

	deps := cli.Deps{
		Sync:   s,
		Export: export.New(store, nil, log),
	}

	root := cli.NewRoot(deps)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"sync", "--claude-path", logfixture.ClaudeRoot()})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v (stderr: %s)", err, errBuf.String())
	}

	want := "claude: 3 sessions, 15 messages, 4 tool calls, 23 records skipped\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}

	files := sessionsFiles(t, storeRoot)
	if len(files) != 3 {
		t.Errorf("store holds %d session files, want 3: %v", len(files), files)
	}
}

func sessionsFiles(t *testing.T, storeRoot string) []string {
	t.Helper()
	dir := filepath.Join(storeRoot, "sessions")
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}
