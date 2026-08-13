package claudesource

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// subagentPaths returns the *.jsonl files under <path-without-.jsonl>/subagents/,
// sorted. A missing subagents directory is not an error — most sessions
// never spawn one — so it reports (nil, nil).
func subagentPaths(path string) ([]string, error) {
	dir := strings.TrimSuffix(path, ".jsonl")
	subDir := filepath.Join(dir, "subagents")

	entries, err := os.ReadDir(subDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			out = append(out, filepath.Join(subDir, e.Name()))
		}
	}
	slices.Sort(out)
	return out, nil
}

// metaToolUseID reads <transcript>.meta.json (the sidecar sitting alongside
// a subagent transcript) and returns its toolUseId — a direct pointer to
// the parent's spawning Agent tool_use id. A missing, unreadable, or
// malformed sidecar returns "" with no error: this is the primary link
// (§C.5), not a required one.
func metaToolUseID(transcriptPath string) string {
	metaPath := strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"

	b, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta struct {
		ToolUseID string `json:"toolUseId"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return ""
	}
	return meta.ToolUseID
}

// agentIDToolUseID is the transcript-only fallback link: it finds the
// parent record whose toolUseResult decodes as an object with a matching
// agentId, and returns that record's tool_result block's tool_use_id.
// toolUseResult is polymorphic (object | string | absent — TDD mapping
// notes); decoding it here is opportunistic, and a failed unmarshal (the
// string or absent shapes) is simply ignored, never an error.
func agentIDToolUseID(parentRecs []rawRecord, agentID string) string {
	if agentID == "" {
		return ""
	}
	for _, r := range parentRecs {
		if len(r.rec.ToolUseResult) == 0 {
			continue
		}
		var tur struct {
			AgentID string `json:"agentId"`
		}
		if err := json.Unmarshal(r.rec.ToolUseResult, &tur); err != nil {
			continue
		}
		if tur.AgentID != agentID {
			continue
		}
		if r.rec.Message == nil {
			continue
		}
		blocks, isString, _ := blocksOf(r.rec.Message.Content)
		if isString {
			continue
		}
		for _, blk := range blocks {
			if blk.Type == "tool_result" {
				return blk.ToolUseID
			}
		}
	}
	return ""
}

// agentIDOf returns the first non-empty top-level agentId across recs — a
// subagent transcript's own agentId (also mirrored in its filename), used
// as the join key for the agentIDToolUseID fallback.
func agentIDOf(recs []rawRecord) string {
	for _, r := range recs {
		if r.rec.AgentID != "" {
			return r.rec.AgentID
		}
	}
	return ""
}
