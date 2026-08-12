// Package model is the unified data model every vendor normalizes into:
// sessions, messages, and tool calls (docs/technical/tdd-mvp.md). It is a
// pure leaf package — it imports nothing from this module, so both the core
// use cases and the ports can speak it.
//
// The JSON tags are load-bearing: a SessionDoc marshaled with them is
// exactly the canonical store's on-disk format, and the DuckDB export's
// generated SQL reads those column names back out via read_json. Changing a
// tag changes the store format and the export schema together.
//
// #9 note: DuckDB's read_json auto-detects columns from the data it reads,
// so a column that is omitempty'd out of every record in the store would
// not exist at all — export SQL selecting that column would then fail.
// #9 must pass an explicit columns={...} schema to read_json rather than
// relying on auto-detection.
package model

import (
	"encoding/json"
	"time"
)

// Vendor identifies the coding agent that wrote a log. IDs are
// vendor-namespaced ("claude:<uuid>") so they are globally unique.
type Vendor string

const (
	VendorClaude Vendor = "claude"
	VendorCodex  Vendor = "codex"
)

// Role is a message's conversational role. Tool results are attached to
// their ToolCall, not emitted as messages, so Role stays a clean
// user/assistant/system signal for querying.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// ToolCallStatus is the outcome of a tool invocation. StatusPending means
// the log never contained a result for the call.
type ToolCallStatus string

const (
	StatusOK      ToolCallStatus = "ok"
	StatusError   ToolCallStatus = "error"
	StatusPending ToolCallStatus = "pending"
)

// SessionDoc is one conversation: the session row plus every message and
// tool call belonging to it. It is the unit a ConversationSource produces
// and a CanonicalStore persists — one JSON document per session file.
type SessionDoc struct {
	Session   Session    `json:"session"`
	Messages  []Message  `json:"messages"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

// Session is one row of the `sessions` relation.
type Session struct {
	SessionID     string    `json:"session_id"`
	Vendor        Vendor    `json:"vendor"`
	ProjectPath   string    `json:"project_path"`
	ProjectName   string    `json:"project_name"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	GitBranch     string    `json:"git_branch,omitempty"`
	VendorVersion string    `json:"vendor_version"`
	SourcePath    string    `json:"source_path"`
}

// Message is one row of the `messages` relation: one conversational turn.
type Message struct {
	MessageID       string    `json:"message_id"`
	SessionID       string    `json:"session_id"`
	Seq             int       `json:"seq"`
	ParentMessageID string    `json:"parent_message_id,omitempty"`
	Role            Role      `json:"role"`
	CreatedAt       time.Time `json:"created_at"`
	Text            string    `json:"text"`
	Model           string    `json:"model,omitempty"`
	// Raw is the vendor's original record, verbatim, for fields the
	// unified model does not capture. Source adapters must set it to
	// valid JSON or leave it nil — never to a non-nil, zero-length
	// json.RawMessage: that fails to marshal at all (json: unexpected end
	// of JSON input), which would take down the whole store write for one
	// bad record, contradicting TDD decision 7's "never fatal". Note also
	// that a nil Raw round-trips through JSON as json.RawMessage("null"),
	// not back to nil.
	Raw json.RawMessage `json:"raw"`
}

// ToolCall is one row of the `tool_calls` relation: one tool invocation,
// joined back to the assistant message that issued it.
type ToolCall struct {
	ToolCallID string `json:"tool_call_id"`
	SessionID  string `json:"session_id"`
	MessageID  string `json:"message_id"`
	Seq        int    `json:"seq"`
	ToolName   string `json:"tool_name"`
	// Arguments is the tool call's raw argument payload. Same contract as
	// Message.Raw: source adapters must emit valid JSON or nil, never a
	// non-nil empty json.RawMessage.
	Arguments json.RawMessage `json:"arguments"`
	Output    string          `json:"output,omitempty"`
	Status    ToolCallStatus  `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
}

// SkipReason classifies a vendor record the normalizer did not turn into a
// row. Parsing is lenient and lossless: skips are counted and reported,
// never fatal. Source adapters may add reasons; the set is open.
type SkipReason string

const (
	SkipMalformedLine     SkipReason = "malformed_line"
	SkipUnknownRecordType SkipReason = "unknown_record_type"
	SkipBookkeeping       SkipReason = "bookkeeping_record"
)

// SkipCounts tallies skipped records by reason; nil means none. Both
// ParseStats (one source file's accounting) and sync.VendorSummary (one
// vendor's accounting across every file it read) use this same shape, so
// accumulating from the former into the latter never needs a bespoke
// map-merge type.
type SkipCounts map[SkipReason]int

// ParseStats accounts for what one source file skipped while producing its
// SessionDoc. A zero ParseStats means nothing was skipped.
type ParseStats struct {
	Skipped SkipCounts
}
