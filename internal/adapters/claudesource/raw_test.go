package claudesource_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/testlogs"
)

func mustList(t *testing.T, src *claudesource.Source, root string) []string {
	t.Helper()
	paths, err := src.List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestRawIsVerbatim(t *testing.T) {
	root := testlogs.ClaudeRoot(t)
	var original []byte
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		b, e := os.ReadFile(p)
		original = append(original, b...)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	src := claudesource.New(nil)
	for _, p := range mustList(t, src, root) {
		doc, _, err := src.Parse(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Messages) == 0 {
			t.Fatal("vacuous raw check")
		}
		for _, m := range doc.Messages {
			if !bytes.Contains(original, append(append([]byte{}, m.Raw...), '\n')) {
				t.Fatal("raw message changed")
			}
		}
		again, _, err := src.Parse(context.Background(), p)
		if err != nil || len(again.Messages) != len(doc.Messages) {
			t.Fatal("repeat parse changed")
		}
	}
}
