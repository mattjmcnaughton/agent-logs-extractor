// Package codexsource reads Codex rollout files without modifying their source.
package codexsource

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

type Source struct{ log *slog.Logger }

func New(log *slog.Logger) *Source {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Source{log: log}
}
func (*Source) Vendor() model.Vendor { return model.VendorCodex }

// List sorts archives before active files, so an active duplicate wins the
// core's last-document-wins rule. It never follows source symlinks.
func (s *Source) List(ctx context.Context, root string) ([]string, error) {
	var out []string
	for _, tree := range []string{"archived_sessions", "sessions"} {
		base := filepath.Join(root, tree)
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if err != nil {
				if p == base && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				if p == base {
					return err
				}
				s.log.Warn("codexsource: unreadable directory; skipping", "path", p, "error", err)
				return nil
			}
			if d.Type().IsRegular() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(out)
	return out, nil
}

type record struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Ordinal   *int64          `json:"ordinal"`
	Payload   json.RawMessage `json:"payload"`
	raw       json.RawMessage
	line      int
}

func (s *Source) Parse(ctx context.Context, path string) (model.SessionDoc, model.ParseStats, error) {
	stats := model.ParseStats{Skipped: model.SkipCounts{}}
	if err := ctx.Err(); err != nil {
		return model.SessionDoc{}, stats, err
	}
	f, err := os.Open(path)
	if err != nil {
		return model.SessionDoc{}, stats, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var records []record
	for n := 1; ; n++ {
		if err := ctx.Err(); err != nil {
			return model.SessionDoc{}, stats, err
		}
		raw, err := reader.ReadBytes('\n')
		raw = bytes.TrimSuffix(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\r'})
		if len(bytes.TrimSpace(raw)) != 0 {
			var r record
			if json.Unmarshal(raw, &r) != nil || r.Type == "" || len(r.Payload) == 0 || bytes.TrimSpace(r.Payload)[0] != '{' || !json.Valid(r.Payload) {
				stats.Skipped[model.SkipMalformedLine]++
			} else {
				r.raw = raw
				r.line = n
				records = append(records, r)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return model.SessionDoc{}, stats, err
			}
			break
		}
	}
	return normalize(ctx, path, records, stats)
}

var _ ports.ConversationSource = (*Source)(nil)
