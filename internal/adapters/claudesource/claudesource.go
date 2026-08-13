// Package claudesource implements ports.ConversationSource for Claude
// Code's ~/.claude/projects tree. Strictly read-only with respect to the
// vendor directory (TDD decision 9): the only files this package ever
// opens are read with os.Open/os.ReadFile/os.ReadDir, never written,
// created, or removed. Parsing is lenient and lossless (TDD decision 7):
// malformed lines and unrecognized record types are counted and skipped,
// never fatal.
package claudesource

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// Source implements ports.ConversationSource for Claude Code.
type Source struct {
	log *slog.Logger
}

// New returns a Source. A nil log is normalized to a discard logger, the
// same convention sync.New uses, so every later call site can log directly
// with no nil check of its own.
func New(log *slog.Logger) *Source {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Source{log: log}
}

// Vendor reports model.VendorClaude.
func (s *Source) Vendor() model.Vendor {
	return model.VendorClaude
}

// List enumerates root/projects/*/*.jsonl — top-level session transcripts
// only. It never recurses: a <session-uuid>/subagents/*.jsonl sidechain
// transcript carries the same sessionId as its parent, so listing it as a
// session in its own right would collide on the sessions primary key
// (§F1 of the ticket plan). Parse folds subagent transcripts into their
// parent's doc instead. A missing root/projects directory is not an error
// (US-7) — it reports (nil, nil), same as an empty one.
func (s *Source) List(ctx context.Context, root string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	projectsDir := filepath.Join(root, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	for _, entry := range entries {
		// Symlinks report ModeSymlink, not IsDir(), so they are skipped
		// here — keeps List off symlink loops and out of foreign trees.
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, entry.Name())
		projEntries, err := os.ReadDir(projDir)
		if err != nil {
			return nil, err
		}
		for _, e := range projEntries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
				out = append(out, filepath.Join(projDir, e.Name()))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// Parse folds path's parent transcript together with any
// <session-uuid>/subagents/*.jsonl sidechain transcripts into one
// model.SessionDoc, merged in chronological order (§C.4). A failure to
// open/read the parent file is the only error Parse returns; a per-subagent
// open failure is logged at warn and skipped — the parent session is still
// worth ingesting without it.
func (s *Source) Parse(ctx context.Context, path string) (model.SessionDoc, model.ParseStats, error) {
	if err := ctx.Err(); err != nil {
		return model.SessionDoc{}, model.ParseStats{}, err
	}

	parentRecs, skips, err := readRecords(ctx, path, 0)
	if err != nil {
		return model.SessionDoc{}, model.ParseStats{}, err
	}

	allRecs := parentRecs

	subPaths, err := subagentPaths(path)
	if err != nil {
		s.log.Warn("claudesource: listing subagent transcripts failed; continuing with parent session only", "path", path, "error", err)
		subPaths = nil
	}

	var links []sidechainLink
	for i, subPath := range subPaths {
		fileRank := i + 1

		subRecs, subSkips, err := readRecords(ctx, subPath, fileRank)
		if err != nil {
			s.log.Warn("claudesource: reading subagent transcript failed; parent session still ingested", "path", subPath, "error", err)
			continue
		}
		skips.merge(subSkips)
		allRecs = append(allRecs, subRecs...)

		links = append(links, s.resolveSidechainLink(subPath, fileRank, subRecs, parentRecs))
	}

	slices.SortStableFunc(allRecs, func(a, b rawRecord) int {
		if c := a.order.Compare(b.order); c != 0 {
			return c
		}
		if a.fileRank != b.fileRank {
			return a.fileRank - b.fileRank
		}
		return a.lineIndex - b.lineIndex
	})

	doc, normSkips := normalize(allRecs, path, links)
	skips.merge(normSkips)

	stats := model.ParseStats{Skipped: skips.counts()}
	if len(doc.Messages) == 0 && len(doc.ToolCalls) == 0 {
		return model.SessionDoc{}, stats, nil
	}
	return doc, stats, nil
}

// resolveSidechainLink computes one subagent transcript's link back to the
// parent's spawning Agent tool call: the .meta.json sidecar's toolUseId is
// tried first (primary), then the toolUseResult.agentId join against the
// parent's records (fallback); an unresolved link is logged at debug and
// left empty, never an error (§C.5, §D4).
func (s *Source) resolveSidechainLink(subPath string, fileRank int, subRecs, parentRecs []rawRecord) sidechainLink {
	toolUseID := metaToolUseID(subPath)
	if toolUseID == "" {
		agentID := agentIDOf(subRecs)
		toolUseID = agentIDToolUseID(parentRecs, agentID)
	}
	if toolUseID == "" {
		s.log.Debug("claudesource: could not resolve sidechain's parent link; parent_message_id will be empty", "path", subPath)
	}
	return sidechainLink{fileRank: fileRank, toolUseID: toolUseID}
}

// readRecords decodes path line by line into rawRecords, tallying skips by
// reason. It uses bufio.Reader.ReadString rather than bufio.Scanner: Claude
// tool-result records can embed arbitrarily large command output, and a
// Scanner's fixed token cap would turn one such line into a whole-file
// failure. A failure to open the file is the only error returned; anything
// wrong with an individual line is counted, never fatal.
func readRecords(ctx context.Context, path string, fileRank int) ([]rawRecord, skipTally, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var (
		recs      []rawRecord
		skips     skipTally
		lastOrder time.Time
		lineNum   int
	)

	r := bufio.NewReader(f)
	for {
		raw, readErr := r.ReadString('\n')
		if len(raw) > 0 {
			lineNum++
			if lineNum%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}

			// A blank line (after trimming its terminator) is skipped
			// silently and never counted.
			trimmed := strings.TrimRight(raw, "\r\n")
			if trimmed != "" {
				rec, ok := decodeLine([]byte(trimmed))
				if !ok {
					skips.add(model.SkipMalformedLine)
				} else {
					ts := parseTimestamp(rec.Timestamp)
					order := ts
					if order.IsZero() {
						// Carry the previous record's order forward so an
						// untimestamped record stays adjacent to its
						// neighbours in the merge sort, rather than
						// sorting to the front of the file.
						order = lastOrder
					} else {
						lastOrder = order
					}
					recs = append(recs, rawRecord{
						// Owned copy: trimmed aliases the reader's
						// internal buffer, which gets overwritten by the
						// next ReadString call.
						line:      append([]byte(nil), trimmed...),
						fileRank:  fileRank,
						lineIndex: lineNum - 1,
						rec:       rec,
						ts:        ts,
						order:     order,
					})
				}
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, nil, readErr
		}
	}

	return recs, skips, nil
}

// parseTimestamp parses a record's RFC3339Nano timestamp, returning the
// zero time on an empty or unparseable value rather than an error — a bad
// timestamp affects ordering only, never fails the record.
func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Interface conformance.
var _ ports.ConversationSource = (*Source)(nil)
