package claudesource

import (
	"encoding/json"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// skipTally counts skipped records by reason. It shares model.SkipCounts's
// underlying map type so counts() is a plain conversion, never a copy loop.
type skipTally map[model.SkipReason]int

func (t *skipTally) add(r model.SkipReason) {
	if *t == nil {
		*t = make(skipTally)
	}
	(*t)[r]++
}

// merge adds every count in other into t.
func (t *skipTally) merge(other skipTally) {
	for r, n := range other {
		if *t == nil {
			*t = make(skipTally)
		}
		(*t)[r] += n
	}
}

// counts converts to model.SkipCounts, nil when nothing was skipped — the
// same "nil means none" contract model.ParseStats documents.
func (t skipTally) counts() model.SkipCounts {
	if len(t) == 0 {
		return nil
	}
	return model.SkipCounts(t)
}

// builder accumulates one SessionDoc across a chronologically-ordered
// stream of records.
type builder struct {
	doc        model.SessionDoc
	byToolUse  map[string]int // "claude:<tool_use_id>" -> index into doc.ToolCalls
	byUUID     map[string]int // "claude:<uuid>"        -> index into doc.Messages
	skips      skipTally
	nextMsgSeq int
	nextTCSeq  int
}

// normalize folds a merge-ordered, already-chronological record stream
// (parent transcript plus any flattened subagent transcripts) into one
// SessionDoc. links resolves each subagent transcript's root message back to
// the parent's spawning Agent tool call (§C.5); sourcePath backstops the
// session id when no record supplies one.
func normalize(recs []rawRecord, sourcePath string, links []sidechainLink) (model.SessionDoc, skipTally) {
	cwd, branch, version, sessionID := sessionMeta(recs)
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(sourcePath), ".jsonl")
	}

	b := &builder{
		byToolUse: make(map[string]int),
		byUUID:    make(map[string]int),
	}
	b.doc.Session = model.Session{
		SessionID:     nsID(sessionID),
		Vendor:        model.VendorClaude,
		ProjectPath:   cwd,
		ProjectName:   projectName(cwd),
		GitBranch:     branch,
		VendorVersion: version,
		SourcePath:    sourcePath,
	}

	linkByRank := make(map[int]sidechainLink, len(links))
	for _, l := range links {
		linkByRank[l.fileRank] = l
	}
	rootSeen := make(map[int]bool)

	for _, r := range recs {
		b.addRecord(r, linkByRank, rootSeen)
	}

	if len(b.doc.Messages) > 0 {
		start := b.doc.Messages[0].CreatedAt
		end := start
		for _, m := range b.doc.Messages {
			if m.CreatedAt.Before(start) {
				start = m.CreatedAt
			}
			if m.CreatedAt.After(end) {
				end = m.CreatedAt
			}
		}
		b.doc.Session.StartedAt = start
		b.doc.Session.EndedAt = end
	}

	return b.doc, b.skips
}

// addRecord routes one record: bookkeeping and unknown types are counted
// and dropped; a record carrying tool_result blocks is consumed to fill in
// existing tool_calls rows (never a message of its own); everything else is
// a candidate messages row, followed by any tool_use blocks it carries.
func (b *builder) addRecord(r rawRecord, links map[int]sidechainLink, rootSeen map[int]bool) {
	switch classify(r.rec.Type) {
	case kindBookkeeping:
		b.skips.add(model.SkipBookkeeping)
		return
	case kindUnknown:
		b.skips.add(model.SkipUnknownRecordType)
		return
	}

	// A record mixing tool_result and text blocks is not observed in any
	// fixture; if it ever occurs, treat it as a tool-result carrier (join
	// the results, drop the text) rather than double-counting it.
	if hasToolResultBlocks(r) {
		b.applyToolResults(r)
		return
	}

	// The sidechain root is the first message from a subagent transcript
	// (fileRank >= 1) whose own parentUuid is null/absent — flattened
	// subagent records otherwise carry their own parentUuid normally.
	isRoot := r.fileRank >= 1 && !rootSeen[r.fileRank] &&
		(r.rec.ParentUUID == nil || *r.rec.ParentUUID == "")

	msgID, ok := b.addMessage(r)
	if !ok {
		return
	}

	if isRoot {
		rootSeen[r.fileRank] = true
		if link, exists := links[r.fileRank]; exists && link.toolUseID != "" {
			if idx, found := b.byToolUse[nsID(link.toolUseID)]; found {
				b.doc.Messages[len(b.doc.Messages)-1].ParentMessageID = b.doc.ToolCalls[idx].MessageID
			}
		}
		// Link unresolved, or the resolved tool_use id was never seen
		// (should not happen: the spawning Agent tool_use always sorts
		// before the sidechain root — §C.5 — but a miss here just leaves
		// ParentMessageID empty rather than panicking).
	}

	b.addToolUses(r, msgID)
}

// addMessage appends a messages row for r, or reports ok=false if the
// record cannot produce one (no uuid, or no text source at all).
func (b *builder) addMessage(r rawRecord) (msgID string, ok bool) {
	if r.rec.UUID == "" {
		b.skips.add(SkipMissingUUID)
		return "", false
	}

	var text, modelName string
	switch {
	case r.rec.Message != nil:
		text = textOf(r.rec.Message.Content)
		if r.rec.Type == "assistant" {
			modelName = r.rec.Message.Model
		}
	case len(r.rec.Content) > 0:
		// type=="system" records carry text under the top-level "content"
		// field rather than under "message" (fixture-unverified: no
		// committed fixture contains a system record).
		text = textOf(r.rec.Content)
	default:
		b.skips.add(SkipMissingMessage)
		return "", false
	}

	msgID = nsID(r.rec.UUID)
	var parentID string
	if r.rec.ParentUUID != nil && *r.rec.ParentUUID != "" {
		// Not validated against byUUID: a dangling parentUuid is preserved
		// verbatim, because it is what the vendor recorded.
		parentID = nsID(*r.rec.ParentUUID)
	}

	msg := model.Message{
		MessageID:       msgID,
		SessionID:       b.doc.Session.SessionID,
		Seq:             b.nextMsgSeq,
		ParentMessageID: parentID,
		Role:            model.Role(r.rec.Type),
		CreatedAt:       r.ts,
		Text:            text,
		Model:           modelName,
		Raw:             r.line,
	}
	b.nextMsgSeq++
	b.doc.Messages = append(b.doc.Messages, msg)
	b.byUUID[msgID] = len(b.doc.Messages) - 1
	return msgID, true
}

// addToolUses appends one tool_calls row per tool_use block in r, in block
// order, attributed to the message msgID that carries them.
func (b *builder) addToolUses(r rawRecord, msgID string) {
	if r.rec.Message == nil {
		return
	}
	blocks, isString, _ := blocksOf(r.rec.Message.Content)
	if isString {
		return
	}
	for _, blk := range blocks {
		if blk.Type != "tool_use" {
			continue
		}
		tc := model.ToolCall{
			ToolCallID: nsID(blk.ID),
			SessionID:  b.doc.Session.SessionID,
			MessageID:  msgID,
			Seq:        b.nextTCSeq,
			ToolName:   blk.Name,
			Arguments:  blk.Input,
			Status:     model.StatusPending,
			CreatedAt:  r.ts,
		}
		b.nextTCSeq++
		b.doc.ToolCalls = append(b.doc.ToolCalls, tc)
		b.byToolUse[nsID(blk.ID)] = len(b.doc.ToolCalls) - 1
	}
}

// applyToolResults joins each tool_result block in r back to its tool_use
// row by tool_use_id, filling in Status and Output. A block whose
// tool_use_id has no earlier matching tool_use is counted as
// SkipOrphanToolResult and dropped: synthesizing a row would need a
// message_id FK this adapter does not have.
func (b *builder) applyToolResults(r rawRecord) {
	if r.rec.Message == nil {
		return
	}
	blocks, isString, _ := blocksOf(r.rec.Message.Content)
	if isString {
		return
	}
	for _, blk := range blocks {
		if blk.Type != "tool_result" {
			continue
		}
		idx, found := b.byToolUse[nsID(blk.ToolUseID)]
		if !found {
			b.skips.add(SkipOrphanToolResult)
			continue
		}
		status := model.StatusOK
		if blk.IsError {
			status = model.StatusError
		}
		b.doc.ToolCalls[idx].Status = status
		b.doc.ToolCalls[idx].Output = resultOutput(blk.Content)
	}
}

// hasToolResultBlocks reports whether r's message content contains at least
// one type=="tool_result" block. A record with a string message.content
// never carries blocks at all.
func hasToolResultBlocks(r rawRecord) bool {
	if r.rec.Message == nil {
		return false
	}
	blocks, isString, _ := blocksOf(r.rec.Message.Content)
	if isString {
		return false
	}
	for _, blk := range blocks {
		if blk.Type == "tool_result" {
			return true
		}
	}
	return false
}

// resultOutput extracts a tool_result block's output text. Same rule as
// textOf: a string content is returned as-is; a content-block array joins
// the .text of every type=="text" block with "\n"; anything else (null,
// absent, an unrecognized shape) is "".
func resultOutput(c json.RawMessage) string {
	return textOf(c)
}

// sessionMeta scans recs for the first non-empty cwd / gitBranch / version /
// sessionId, always preferring the parent transcript (fileRank 0) over any
// subagent transcript, regardless of chronological order — a subagent
// record can sort before some parent records (§C.4), but session-level
// metadata should still come from the parent file first. Every committed
// fixture opens with a bookkeeping queue-operation record carrying none of
// these fields, so "the first record" would yield nothing; this is why the
// rule is per-field first-non-empty rather than "read the first record".
func sessionMeta(recs []rawRecord) (cwd, branch, version, sessionID string) {
	ordered := slices.Clone(recs)
	slices.SortStableFunc(ordered, func(a, b rawRecord) int {
		if a.fileRank != b.fileRank {
			return a.fileRank - b.fileRank
		}
		return a.lineIndex - b.lineIndex
	})
	for _, r := range ordered {
		if cwd == "" && r.rec.CWD != "" {
			cwd = r.rec.CWD
		}
		if branch == "" && r.rec.GitBranch != "" {
			branch = r.rec.GitBranch
		}
		if version == "" && r.rec.Version != "" {
			version = r.rec.Version
		}
		if sessionID == "" && r.rec.SessionID != "" {
			sessionID = r.rec.SessionID
		}
		if cwd != "" && branch != "" && version != "" && sessionID != "" {
			break
		}
	}
	return cwd, branch, version, sessionID
}

// nsID vendor-namespaces a bare Claude id.
func nsID(s string) string {
	return "claude:" + s
}

// projectName is path.Base, not filepath.Base: the vendor writes
// slash-separated POSIX cwd paths regardless of the host reading them. An
// empty ProjectPath yields an empty name, never ".".
func projectName(p string) string {
	if p == "" {
		return ""
	}
	return path.Base(p)
}
