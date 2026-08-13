package claudesource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture"
)

var update = flag.Bool("update", false, "update golden files in testdata/")

// mustList calls src.List and fails the test on error — every test here
// treats a broken List over the committed fixture tree as a setup bug, not
// a thing under test.
func mustList(t *testing.T, src *claudesource.Source, root string) []string {
	t.Helper()
	paths, err := src.List(context.Background(), root)
	if err != nil {
		t.Fatalf("List(%s): %v", root, err)
	}
	return paths
}

// goldenName maps a session file path to its testdata/*.golden.json stem:
// the project directory name with its leading "-" trimmed, plus
// "-<file-stem>" only when the project holds more than one top-level
// *.jsonl (as the four pathological files, sharing one synthetic project
// directory, do). Real fixture projects hold exactly one (logfixture.
// SessionFile's contract), so they get stable names untouched by this
// suffix.
func goldenName(t *testing.T, path string) string {
	t.Helper()
	projDir := filepath.Dir(path)
	proj := strings.TrimPrefix(filepath.Base(projDir), "-")

	entries, err := os.ReadDir(projDir)
	if err != nil {
		t.Fatalf("reading %s: %v", projDir, err)
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			count++
		}
	}
	if count <= 1 {
		return proj
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return proj + "-" + stem
}

// golden is the shape written to testdata/*.golden.json: everything Parse
// returns for one fixture, besides the error (which every fixture in this
// suite is asserted to be nil for separately).
type golden struct {
	Stats model.ParseStats `json:"stats"`
	Doc   model.SessionDoc `json:"doc"`
}

// normalizeSourcePath rewrites doc.Session.SourcePath to be relative to
// logfixture.Dir(), so a golden file compares equal on any machine and in
// CI, not just on the one it was generated on (§F5 of the ticket plan).
func normalizeSourcePath(doc model.SessionDoc) model.SessionDoc {
	doc.Session.SourcePath = strings.TrimPrefix(doc.Session.SourcePath, logfixture.Dir()+string(filepath.Separator))
	return doc
}

// TestGoldenParse parses every session file List finds under the real and
// pathological Claude fixture roots and compares the result — stats plus
// the full SessionDoc — against a committed golden file. Run with -update
// to write/refresh the goldens after reviewing the diff.
func TestGoldenParse(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	var paths []string
	paths = append(paths, mustList(t, src, logfixture.ClaudeRoot())...)
	paths = append(paths, mustList(t, src, logfixture.PathologicalClaudeRoot())...)

	for _, p := range paths {
		name := goldenName(t, p)
		t.Run(name, func(t *testing.T) {
			doc, stats, err := src.Parse(ctx, p)
			if err != nil {
				t.Fatalf("Parse(%s): %v", p, err)
			}
			doc = normalizeSourcePath(doc)

			gotBytes, err := json.MarshalIndent(golden{Stats: stats, Doc: doc}, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			gotBytes = append(gotBytes, '\n')

			goldenPath := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(goldenPath, gotBytes, 0o644); err != nil {
					t.Fatalf("writing %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("reading %s: %v (run `go test ./internal/adapters/claudesource -run TestGoldenParse -update` to create it)", goldenPath, err)
			}
			if !bytes.Equal(gotBytes, want) {
				t.Errorf("golden mismatch for %s (-update to refresh)\n got:\n%s\nwant:\n%s", p, gotBytes, want)
			}
		})
	}
}

// TestGoldenSetMatchesFixtures is the anti-rot guard for TestGoldenParse: a
// fixture with no golden file fails here rather than being silently
// unverified, and a golden file with no matching fixture (left behind by a
// fixture removal or rename) fails here rather than rotting forever.
func TestGoldenSetMatchesFixtures(t *testing.T) {
	src := claudesource.New(nil)

	want := map[string]bool{}
	for _, root := range []string{logfixture.ClaudeRoot(), logfixture.PathologicalClaudeRoot()} {
		for _, p := range mustList(t, src, root) {
			want[goldenName(t, p)+".golden.json"] = true
		}
	}

	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("reading testdata: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".golden.json") {
			got[e.Name()] = true
		}
	}

	for name := range want {
		if !got[name] {
			t.Errorf("missing golden file for a fixture List(...) reported: testdata/%s", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("stale golden file with no matching fixture: testdata/%s", name)
		}
	}
}

// rawLinesByUUID reads path's non-empty lines and indexes them by the
// record's uuid field, so a test can look up the exact source bytes a
// message's Raw ought to byte-equal. A line with no (or unparseable) uuid
// is skipped: malformed lines never become messages, so they never need to
// satisfy this check.
func rawLinesByUUID(t *testing.T, path string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	out := make(map[string][]byte)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		var probe struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil || probe.UUID == "" {
			continue
		}
		out[probe.UUID] = []byte(trimmed)
	}
	return out
}

// subagentFilesOf returns the *.jsonl files under path's own subagents/
// directory (mirroring what Source.Parse itself looks for), or nil if
// there is none.
func subagentFilesOf(t *testing.T, path string) []string {
	t.Helper()
	subDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			out = append(out, filepath.Join(subDir, e.Name()))
		}
	}
	return out
}

// TestRawIsVerbatim proves losslessness independently of the golden files:
// every emitted message's Raw must byte-equal the exact source line (from
// the parent transcript or a flattened subagent transcript) it came from.
func TestRawIsVerbatim(t *testing.T) {
	src := claudesource.New(nil)
	ctx := context.Background()

	var paths []string
	paths = append(paths, mustList(t, src, logfixture.ClaudeRoot())...)
	paths = append(paths, mustList(t, src, logfixture.PathologicalClaudeRoot())...)

	for _, p := range paths {
		t.Run(goldenName(t, p), func(t *testing.T) {
			doc, _, err := src.Parse(ctx, p)
			if err != nil {
				t.Fatalf("Parse(%s): %v", p, err)
			}

			raws := rawLinesByUUID(t, p)
			for _, sub := range subagentFilesOf(t, p) {
				maps.Copy(raws, rawLinesByUUID(t, sub))
			}

			for _, m := range doc.Messages {
				uuid := strings.TrimPrefix(m.MessageID, "claude:")
				want, ok := raws[uuid]
				if !ok {
					t.Errorf("message %s: no source line found for uuid %q", m.MessageID, uuid)
					continue
				}
				if !bytes.Equal(m.Raw, want) {
					t.Errorf("message %s: Raw does not byte-equal its source line\n got:  %s\nwant: %s", m.MessageID, m.Raw, want)
				}
			}
		})
	}
}
