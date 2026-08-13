package claudesource

import (
	"encoding/json"
	"strings"
	"time"
)

// rawRecord is one decoded log line, plus where it came from. fileRank and
// lineIndex feed the merge-order tie-break (§C.4 of the ticket plan); ts is
// the record's own parsed timestamp (used for Message.CreatedAt); order is
// the value actually sorted on, carrying the previous record's timestamp
// forward when this one has none, so an untimestamped record stays adjacent
// to its neighbours instead of sorting to the front.
type rawRecord struct {
	line      json.RawMessage // verbatim bytes of the source line (owned copy)
	fileRank  int             // 0 = parent transcript, 1..n = subagent transcripts (sorted by path)
	lineIndex int             // 0-based line number within its file
	rec       record
	ts        time.Time // parsed rec.Timestamp; zero if absent/unparseable
	order     time.Time // ts, or carried-forward previous ts in the same file (ordering only)
}

// record is Claude Code's vendor wire shape for one JSONL line. Every field
// is optional in practice — bookkeeping record types (queue-operation,
// attachment, ai-title, last-prompt, mode, summary, …) carry only a subset.
type record struct {
	Type          string          `json:"type"`
	UUID          string          `json:"uuid"`
	ParentUUID    *string         `json:"parentUuid"`
	SessionID     string          `json:"sessionId"`
	Timestamp     string          `json:"timestamp"`
	CWD           string          `json:"cwd"`
	GitBranch     string          `json:"gitBranch"`
	Version       string          `json:"version"`
	IsSidechain   bool            `json:"isSidechain"`
	AgentID       string          `json:"agentId"`
	Message       *apiMessage     `json:"message"`
	Content       json.RawMessage `json:"content"`       // system records carry text here, not under message
	ToolUseResult json.RawMessage `json:"toolUseResult"` // POLYMORPHIC: object | string | absent — kept raw
}

// apiMessage is the API-shaped message object nested under a user/assistant
// record.
type apiMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // string | []block
}

// block is one content block, covering the fields used by text, tool_use,
// and tool_result blocks alike (unused fields are simply left zero for any
// given block type).
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	IsError   bool            `json:"is_error"`    // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result: string | []block
}

// decodeLine decodes one JSONL line into a record. It first proves the line
// is a JSON object (a malformed or non-object line is the caller's
// SkipMalformedLine case), then unmarshals into the typed record; a decode
// failure at either step reports false.
func decodeLine(line []byte) (record, bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(line, &probe); err != nil {
		return record{}, false
	}
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return record{}, false
	}
	return r, true
}

// blocksOf classifies a message/tool_result content payload: a JSON string
// reports isString=true and str; anything else is opportunistically decoded
// as a content-block array (a decode failure just yields no blocks, never an
// error — content shapes this normalizer doesn't recognize are simply
// textless, never fatal).
func blocksOf(c json.RawMessage) (blocks []block, isString bool, str string) {
	if len(c) == 0 {
		return nil, false, ""
	}
	if err := json.Unmarshal(c, &str); err == nil {
		return nil, true, str
	}
	_ = json.Unmarshal(c, &blocks)
	return blocks, false, ""
}

// textOf extracts human-readable text from a content payload: a JSON string
// is returned as-is; a content-block array joins the .Text of every
// type=="text" block with "\n" (thinking blocks contribute nothing — they
// stay in Raw only); anything else (null, absent, unrecognized shape) is "".
func textOf(c json.RawMessage) string {
	blocks, isString, str := blocksOf(c)
	if isString {
		return str
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}
