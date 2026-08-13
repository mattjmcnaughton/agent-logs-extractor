package claudesource

import "github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"

// kind is the adapter's own record-type taxonomy, used to route a decoded
// record before any field-level normalization runs.
type kind int

const (
	kindConversational kind = iota
	kindBookkeeping
	kindUnknown
)

// bookkeepingTypes are the vendor record types observed in the committed
// fixtures, plus the two the TDD names but no fixture exercises (mode,
// summary). None of these ever produce a messages row.
var bookkeepingTypes = map[string]bool{
	"queue-operation": true,
	"attachment":      true,
	"ai-title":        true,
	"last-prompt":     true,
	"mode":            true,
	"summary":         true,
}

// classify routes a record's vendor type: user/assistant/system are
// conversational (candidates for a messages row or a tool-result join);
// bookkeepingTypes are skipped and counted; anything else is unknown and
// skipped and counted separately, so a genuinely new vendor record type
// shows up distinctly in ParseStats rather than silently joining the
// bookkeeping tally.
func classify(t string) kind {
	switch {
	case t == "user" || t == "assistant" || t == "system":
		return kindConversational
	case bookkeepingTypes[t]:
		return kindBookkeeping
	default:
		return kindUnknown
	}
}

// Adapter-local skip reasons. model.SkipReason is documented as an open
// set (internal/core/model/model.go); these surface in model.ParseStats and
// therefore in sync's printed summary (#8), so they are part of this
// package's exported surface even though everything else here is
// unexported.
const (
	// SkipMissingMessage marks a conversational record carrying neither a
	// message object (user/assistant) nor a top-level content field
	// (system) — no extractable text at all, regardless of whether either
	// field is merely empty.
	SkipMissingMessage model.SkipReason = "missing_message"
	// SkipMissingUUID marks a conversational record with an empty uuid — it
	// cannot become a Message without a MessageID.
	SkipMissingUUID model.SkipReason = "missing_uuid"
	// SkipOrphanToolResult marks a tool_result block whose tool_use_id is
	// empty, or has no earlier matching tool_use in this session.
	SkipOrphanToolResult model.SkipReason = "orphan_tool_result"
	// SkipMissingToolUseID marks a tool_use block with an empty id. It is
	// dropped rather than indexed under the literal key "claude:" — the
	// nsID of an empty string — which would let it join to any tool_result
	// that is itself missing a tool_use_id, fabricating a connection
	// between two unrelated blocks.
	SkipMissingToolUseID model.SkipReason = "missing_tool_use_id"
	// SkipDuplicateMessageID marks a conversational record whose uuid
	// collides with one already turned into a Message this session.
	// message_id is the messages primary key (docs/technical/tdd-mvp.md);
	// the first row wins and every later duplicate is dropped rather than
	// emitted as a colliding row.
	SkipDuplicateMessageID model.SkipReason = "duplicate_message_id"
	// SkipDuplicateToolUseID marks a tool_use block whose id collides with
	// one already turned into a ToolCall this session. tool_call_id is the
	// tool_calls primary key; the first row wins, the same policy as
	// SkipDuplicateMessageID.
	SkipDuplicateToolUseID model.SkipReason = "duplicate_tool_use_id"
)
