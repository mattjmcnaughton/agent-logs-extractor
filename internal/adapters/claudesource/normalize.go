package claudesource

import (
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// skipTally counts skipped records by reason. It shares model.SkipCounts's
// underlying map type so counts() is a plain conversion, never a copy loop.
// Every construction site uses make(skipTally) rather than a nil zero
// value, so add/merge can take value receivers: a map header copies by
// value but still points at the same underlying data, so mutating through
// it needs no pointer back to the caller's variable — only a nil map would.
type skipTally map[model.SkipReason]int

func (t skipTally) add(r model.SkipReason) {
	t[r]++
}

// merge adds every count in other into t. other may be nil (an empty range
// is a no-op).
func (t skipTally) merge(other skipTally) {
	for r, n := range other {
		t[r] += n
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
// SessionDoc. parentRecs is the same records restricted to the parent
// transcript alone, in file order — sessionMeta's preferred source. links
// maps a subagent transcript's fileRank to its resolved parent tool_use id
// (§C.5; "" or absent means unresolved). sourcePath backstops the session
// id when no record supplies one.
func normalize(recs, parentRecs []rawRecord, sourcePath string, links map[int]string) (model.SessionDoc, skipTally) {
	cwd, branch, version, sessionID := sessionMeta(parentRecs, recs)
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(sourcePath), ".jsonl")
	}

	b := &builder{
		byToolUse: make(map[string]int),
		byUUID:    make(map[string]int),
		skips:     make(skipTally),
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

	rootSeen := make(map[int]bool)
	for _, r := range recs {
		b.addRecord(r, links, rootSeen)
	}

	var start, end time.Time
	for _, m := range b.doc.Messages {
		// A missing/unparseable timestamp leaves CreatedAt at its zero
		// value (parseTimestamp's contract); folding that into start/end
		// would drag started_at to 0001-01-01 the moment any one message
		// in the session lacks a timestamp. Skip zero values entirely: if
		// every message is zero-valued, start/end stay zero too, same as
		// an empty session.
		if m.CreatedAt.IsZero() {
			continue
		}
		if start.IsZero() || m.CreatedAt.Before(start) {
			start = m.CreatedAt
		}
		if end.IsZero() || m.CreatedAt.After(end) {
			end = m.CreatedAt
		}
	}
	b.doc.Session.StartedAt = start
	b.doc.Session.EndedAt = end

	return b.doc, b.skips
}

// addRecord routes one record: bookkeeping and unknown types are counted
// and dropped. A record's message content (if any) is decoded exactly once
// here and the materialized blocks are threaded through every subsequent
// step, rather than each of addMessage/addToolUses/applyToolResults
// re-decoding the same bytes. Content carrying tool_result blocks is
// consumed to fill in existing tool_calls rows (never a message of its
// own); everything else is a candidate messages row, followed by any
// tool_use blocks it carries.
func (b *builder) addRecord(r rawRecord, links map[int]string, rootSeen map[int]bool) {
	switch classify(r.rec.Type) {
	case kindBookkeeping:
		b.skips.add(model.SkipBookkeeping)
		return
	case kindUnknown:
		b.skips.add(model.SkipUnknownRecordType)
		return
	}

	var blocks []block
	var isString bool
	var str string
	if r.rec.Message != nil {
		blocks, isString, str = blocksOf(r.rec.Message.Content)
	}

	// The sidechain root is the first message from a subagent transcript
	// (fileRank >= 1) whose own parentUuid is null/absent — flattened
	// subagent records otherwise carry their own parentUuid normally.
	// Computed and claimed (rootSeen set) before the tool-result early
	// return below, so the guard stays total even for a subagent
	// transcript whose very first record happens to carry tool_result
	// blocks and never becomes a message: without claiming rootSeen here,
	// a later parentUuid==null record in the same file would be mistaken
	// for the root instead.
	isRoot := r.fileRank >= 1 && !rootSeen[r.fileRank] &&
		(r.rec.ParentUUID == nil || *r.rec.ParentUUID == "")
	if isRoot {
		rootSeen[r.fileRank] = true
	}

	// A record mixing tool_result with text and/or tool_use blocks is not
	// observed in any fixture; if it ever occurs, treat it as a
	// tool-result carrier (join the results, drop the text and any
	// tool_use blocks) rather than double-counting it.
	if !isString && containsToolResult(blocks) {
		b.applyToolResults(blocks)
		return
	}

	msgID, ok := b.addMessage(r, blocks, isString, str)
	if !ok {
		return
	}

	if isRoot {
		if toolUseID := links[r.fileRank]; toolUseID != "" {
			if idx, found := b.byToolUse[nsID(toolUseID)]; found {
				b.doc.Messages[b.byUUID[msgID]].ParentMessageID = b.doc.ToolCalls[idx].MessageID
			}
		}
		// Link unresolved, or the resolved tool_use id was never seen
		// (should not happen: the spawning Agent tool_use always sorts
		// before the sidechain root — §C.5 — but a miss here just leaves
		// ParentMessageID empty rather than panicking).
	}

	b.addToolUses(r, msgID, blocks, isString)
}

// addMessage appends a messages row for r, or reports ok=false if the
// record cannot produce one: no uuid, no text source at all, or a uuid
// that collides with a message already added this session (message_id is
// the messages primary key; the first row wins). blocks/isString/str are
// r.rec.Message's content, already decoded by addRecord — zero-valued when
// r.rec.Message is nil.
func (b *builder) addMessage(r rawRecord, blocks []block, isString bool, str string) (msgID string, ok bool) {
	if r.rec.UUID == "" {
		b.skips.add(SkipMissingUUID)
		return "", false
	}

	msgID = nsID(r.rec.UUID)
	if _, dup := b.byUUID[msgID]; dup {
		b.skips.add(SkipDuplicateMessageID)
		return "", false
	}

	var text, modelName string
	switch {
	case r.rec.Message != nil:
		text = joinText(blocks, isString, str)
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
// order, attributed to the message msgID that carries them. A block with
// no id is dropped (SkipMissingToolUseID): indexing it under the literal
// key "claude:" would fabricate a join to any tool_result that is itself
// missing a tool_use_id. A block whose id collides with a tool_use already
// added this session is dropped too (SkipDuplicateToolUseID): tool_call_id
// is the tool_calls primary key, and only the first row wins.
func (b *builder) addToolUses(r rawRecord, msgID string, blocks []block, isString bool) {
	if isString {
		return
	}
	for _, blk := range blocks {
		if blk.Type != "tool_use" {
			continue
		}
		if blk.ID == "" {
			b.skips.add(SkipMissingToolUseID)
			continue
		}
		toolCallID := nsID(blk.ID)
		if _, dup := b.byToolUse[toolCallID]; dup {
			b.skips.add(SkipDuplicateToolUseID)
			continue
		}
		tc := model.ToolCall{
			ToolCallID: toolCallID,
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
		b.byToolUse[toolCallID] = len(b.doc.ToolCalls) - 1
	}
}

// applyToolResults joins each tool_result block in blocks back to its
// tool_use row by tool_use_id, filling in Status and Output. A block with
// no tool_use_id, or whose tool_use_id has no earlier matching tool_use, is
// counted as SkipOrphanToolResult and dropped: synthesizing a row would
// need a message_id FK this adapter does not have.
func (b *builder) applyToolResults(blocks []block) {
	for _, blk := range blocks {
		if blk.Type != "tool_result" {
			continue
		}
		if blk.ToolUseID == "" {
			b.skips.add(SkipOrphanToolResult)
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
		b.doc.ToolCalls[idx].Output = textOf(blk.Content)
	}
}

// containsToolResult reports whether blocks holds at least one
// type=="tool_result" block.
func containsToolResult(blocks []block) bool {
	for _, blk := range blocks {
		if blk.Type == "tool_result" {
			return true
		}
	}
	return false
}

// sessionMeta scans parentRecs (already in file order — Parse builds it
// that way, one readRecords call over a single file) for the first
// non-empty cwd / gitBranch / version / sessionId, falling back to allRecs
// (the full chronological merge, which may include subagent-only
// metadata) only for a field still empty after scanning the parent alone.
// This prefers the parent transcript over any subagent transcript
// regardless of chronological order — a subagent record can sort before
// some parent records (§C.4), but session-level metadata should still come
// from the parent file first. Every committed fixture opens with a
// bookkeeping queue-operation record carrying none of these fields, so
// "the first record" would yield nothing; this is why the rule is
// per-field first-non-empty rather than "read the first record".
func sessionMeta(parentRecs, allRecs []rawRecord) (cwd, branch, version, sessionID string) {
	scan := func(recs []rawRecord) {
		for _, r := range recs {
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
				return
			}
		}
	}
	scan(parentRecs)
	if cwd == "" || branch == "" || version == "" || sessionID == "" {
		scan(allRecs)
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
