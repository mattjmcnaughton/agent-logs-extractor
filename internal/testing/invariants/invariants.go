// Package invariants defines the structural contract a model.SessionDoc
// must satisfy regardless of vendor, plus a companion set of "drift"
// observations that never fail but flag vendor-format changes worth a
// human look (docs/technical/tdd-mvp.md core decision 8).
//
// It is a pure leaf package: no build tag, no I/O, imports nothing but the
// stdlib, internal/core/model, and internal/ports. That is what makes it
// provable by a plain `go test` — see invariants_test.go — even though its
// real customer, the opt-in contract tier
// (internal/adapters/claudesource/claudesource_contract_test.go), reads a
// developer's live ~/.claude and cannot itself run in every environment.
//
// The split mirrors sync's own three-way severity split
// (docs/technical/tdd-mvp.md, "Settled by internal/core/sync (#8)"): Check
// reports hard failures — a SessionDoc a conforming source adapter must
// never produce — while Observe reports signals that are merely
// interesting, never wrong on their own (a session's cwd changing
// mid-conversation is a real vendor behavior, not a bug).
package invariants

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// maxExamples caps how many example strings an Observation carries — a
// whole-tree contract run can produce thousands of occurrences of one
// Kind, and a report that printed all of them would defeat its own
// purpose.
const maxExamples = 3

// Violation Kinds. Every Check caller should treat any non-empty
// []Violation as a hard failure — these are outcomes a conforming source
// adapter must never produce, not judgment calls.
const (
	KindSeqGap                    = "seq_gap"
	KindSessionIDMismatch         = "session_id_mismatch"
	KindDuplicateMessageID        = "duplicate_message_id"
	KindDuplicateToolCallID       = "duplicate_tool_call_id"
	KindDanglingToolCallMessageID = "dangling_tool_call_message_id"
	KindInvalidSessionDoc         = "invalid_session_doc"
	KindInvalidRole               = "invalid_role"
	KindInvalidStatus             = "invalid_status"
	KindInvalidRawJSON            = "invalid_raw_json"
	KindInvalidArgumentsJSON      = "invalid_arguments_json"
)

// Observation Kinds Observe itself produces. A contract-tier caller that
// also wants to report skip-reason drift (unknown_record_type,
// malformed_line — model.SkipReason values from ports.ConversationSource's
// own ParseStats) or cross-file duplicate session ids should build those
// as plain Observation values itself (using KindDuplicateSessionID below
// for the latter) and fold them in with MergeObservations — a single
// SessionDoc cannot see across files or carry parse-level skip counts on
// its own.
const (
	KindDanglingParentMessageID = "dangling_parent_message_id"
	KindZeroTimestamp           = "zero_timestamp"
	KindCreatedAtNonMonotonic   = "created_at_non_monotonic"
	KindEmptyProjectPath        = "empty_project_path"
	KindEmptyVendorVersion      = "empty_vendor_version"
	KindCwdDrift                = "cwd_drift"
	// KindDuplicateSessionID is not produced by Observe (no single doc can
	// see another file's session id) but is exported so every caller
	// tallying it across files uses the same Kind string.
	KindDuplicateSessionID = "duplicate_session_id"
)

// Violation is one structural invariant a SessionDoc broke.
type Violation struct {
	Kind   string
	Detail string
}

// Observation is one drift signal, possibly accumulated across many
// SessionDocs (see MergeObservations). Count is the number of occurrences;
// Examples holds up to maxExamples illustrative details, never every
// occurrence.
type Observation struct {
	Kind     string
	Count    int
	Examples []string
}

// Check reports every structural invariant doc breaks. An empty result
// means doc is a conforming SessionDoc. Nothing here is vendor-specific:
// it holds for any ports.ConversationSource implementation, present or
// future.
func Check(doc model.SessionDoc) []Violation {
	var vs []Violation

	seenMsg := make(map[string]bool, len(doc.Messages))
	for i, m := range doc.Messages {
		if m.Seq != i {
			vs = append(vs, Violation{
				Kind:   KindSeqGap,
				Detail: fmt.Sprintf("messages[%d]: seq=%d, want %d (message_id=%s)", i, m.Seq, i, m.MessageID),
			})
		}
		if m.SessionID != doc.Session.SessionID {
			vs = append(vs, Violation{
				Kind:   KindSessionIDMismatch,
				Detail: fmt.Sprintf("messages[%d]: session_id=%q, want %q (message_id=%s)", i, m.SessionID, doc.Session.SessionID, m.MessageID),
			})
		}
		if !validRole(m.Role) {
			vs = append(vs, Violation{
				Kind:   KindInvalidRole,
				Detail: fmt.Sprintf("message_id=%s: role=%q", m.MessageID, m.Role),
			})
		}
		if !validRawOrNil(m.Raw) {
			vs = append(vs, Violation{
				Kind:   KindInvalidRawJSON,
				Detail: fmt.Sprintf("message_id=%s: raw is non-nil, zero-length, or invalid JSON", m.MessageID),
			})
		}
		if m.MessageID != "" {
			if seenMsg[m.MessageID] {
				vs = append(vs, Violation{Kind: KindDuplicateMessageID, Detail: m.MessageID})
			}
			seenMsg[m.MessageID] = true
		}
	}

	seenTC := make(map[string]bool, len(doc.ToolCalls))
	for i, tc := range doc.ToolCalls {
		if tc.Seq != i {
			vs = append(vs, Violation{
				Kind:   KindSeqGap,
				Detail: fmt.Sprintf("tool_calls[%d]: seq=%d, want %d (tool_call_id=%s)", i, tc.Seq, i, tc.ToolCallID),
			})
		}
		if tc.SessionID != doc.Session.SessionID {
			vs = append(vs, Violation{
				Kind:   KindSessionIDMismatch,
				Detail: fmt.Sprintf("tool_calls[%d]: session_id=%q, want %q (tool_call_id=%s)", i, tc.SessionID, doc.Session.SessionID, tc.ToolCallID),
			})
		}
		if !validStatus(tc.Status) {
			vs = append(vs, Violation{
				Kind:   KindInvalidStatus,
				Detail: fmt.Sprintf("tool_call_id=%s: status=%q", tc.ToolCallID, tc.Status),
			})
		}
		if !validRawOrNil(tc.Arguments) {
			vs = append(vs, Violation{
				Kind:   KindInvalidArgumentsJSON,
				Detail: fmt.Sprintf("tool_call_id=%s: arguments is non-nil, zero-length, or invalid JSON", tc.ToolCallID),
			})
		}
		if !seenMsg[tc.MessageID] {
			vs = append(vs, Violation{
				Kind:   KindDanglingToolCallMessageID,
				Detail: fmt.Sprintf("tool_call_id=%s: message_id=%q resolves to no message in this doc", tc.ToolCallID, tc.MessageID),
			})
		}
		if tc.ToolCallID != "" {
			if seenTC[tc.ToolCallID] {
				vs = append(vs, Violation{Kind: KindDuplicateToolCallID, Detail: tc.ToolCallID})
			}
			seenTC[tc.ToolCallID] = true
		}
	}

	if (len(doc.Messages) > 0 || len(doc.ToolCalls) > 0) && !ports.ValidSessionDoc(doc.Session) {
		vs = append(vs, Violation{
			Kind:   KindInvalidSessionDoc,
			Detail: fmt.Sprintf("session_id=%q vendor=%q carries %d messages, %d tool calls", doc.Session.SessionID, doc.Session.Vendor, len(doc.Messages), len(doc.ToolCalls)),
		})
	}

	return vs
}

// Observe reports drift signals derivable from a single SessionDoc. Never a
// failure — see the package doc for why these are observational, not
// invariants.
func Observe(doc model.SessionDoc) []Observation {
	byID := make(map[string]bool, len(doc.Messages))
	for _, m := range doc.Messages {
		byID[m.MessageID] = true
	}

	acc := newAccumulator()

	var lastNonZero time.Time
	haveLast := false
	for _, m := range doc.Messages {
		if m.ParentMessageID != "" && !byID[m.ParentMessageID] {
			acc.add(KindDanglingParentMessageID, fmt.Sprintf("session=%s message=%s parent=%s", doc.Session.SessionID, m.MessageID, m.ParentMessageID))
		}
		if m.CreatedAt.IsZero() {
			acc.add(KindZeroTimestamp, fmt.Sprintf("session=%s message=%s", doc.Session.SessionID, m.MessageID))
		} else {
			if haveLast && m.CreatedAt.Before(lastNonZero) {
				acc.add(KindCreatedAtNonMonotonic, fmt.Sprintf("session=%s message=%s seq=%d", doc.Session.SessionID, m.MessageID, m.Seq))
			}
			lastNonZero = m.CreatedAt
			haveLast = true
		}
	}

	if doc.Session.SessionID != "" {
		if doc.Session.ProjectPath == "" {
			acc.add(KindEmptyProjectPath, doc.Session.SessionID)
		}
		if doc.Session.VendorVersion == "" {
			acc.add(KindEmptyVendorVersion, doc.Session.SessionID)
		}
	}

	cwds := make(map[string]bool)
	for _, m := range doc.Messages {
		if cwd, ok := rawCWD(m.Raw); ok && cwd != "" {
			cwds[cwd] = true
		}
	}
	if len(cwds) > 1 {
		examples := make([]string, 0, len(cwds))
		for c := range cwds {
			examples = append(examples, c)
		}
		sort.Strings(examples)
		acc.set(KindCwdDrift, len(cwds), examples)
	}

	return acc.result()
}

// MergeObservations combines observations from many SessionDocs (or from a
// caller's own hand-built Observations for signals no single doc can see,
// e.g. KindDuplicateSessionID) into one report per Kind: Count summed,
// Examples capped at maxExamples and drawn first-seen across every input
// set, sorted by Kind for a deterministic report.
func MergeObservations(sets ...[]Observation) []Observation {
	acc := newAccumulator()
	for _, set := range sets {
		for _, o := range set {
			acc.merge(o)
		}
	}
	return acc.result()
}

// accumulator is the shared by-Kind tally both Observe and MergeObservations
// build their result from, so "cap examples at maxExamples, sort by Kind"
// is written exactly once.
type accumulator struct {
	byKind map[string]*Observation
	order  []string
}

func newAccumulator() *accumulator {
	return &accumulator{byKind: make(map[string]*Observation)}
}

func (a *accumulator) entry(kind string) *Observation {
	o, ok := a.byKind[kind]
	if !ok {
		o = &Observation{Kind: kind}
		a.byKind[kind] = o
		a.order = append(a.order, kind)
	}
	return o
}

func (a *accumulator) add(kind, example string) {
	o := a.entry(kind)
	o.Count++
	if len(o.Examples) < maxExamples {
		o.Examples = append(o.Examples, example)
	}
}

// set overwrites kind's count/examples outright (capped), for a signal
// (like KindCwdDrift) whose natural unit is "distinct values seen", not
// "occurrences added one at a time".
func (a *accumulator) set(kind string, count int, examples []string) {
	o := a.entry(kind)
	o.Count = count
	if len(examples) > maxExamples {
		examples = examples[:maxExamples]
	}
	o.Examples = examples
}

func (a *accumulator) merge(o Observation) {
	dst := a.entry(o.Kind)
	dst.Count += o.Count
	for _, ex := range o.Examples {
		if len(dst.Examples) < maxExamples {
			dst.Examples = append(dst.Examples, ex)
		}
	}
}

func (a *accumulator) result() []Observation {
	sort.Strings(a.order)
	out := make([]Observation, 0, len(a.order))
	for _, k := range a.order {
		out = append(out, *a.byKind[k])
	}
	return out
}

func validRole(r model.Role) bool {
	switch r {
	case model.RoleUser, model.RoleAssistant, model.RoleSystem:
		return true
	default:
		return false
	}
}

func validStatus(s model.ToolCallStatus) bool {
	switch s {
	case model.StatusOK, model.StatusError, model.StatusPending:
		return true
	default:
		return false
	}
}

// validRawOrNil enforces model.Message.Raw / model.ToolCall.Arguments's own
// documented contract: nil, or valid JSON — never a non-nil, zero-length
// json.RawMessage (that fails to marshal at all).
func validRawOrNil(raw json.RawMessage) bool {
	if raw == nil {
		return true
	}
	if len(raw) == 0 {
		return false
	}
	return json.Valid(raw)
}

// rawCWD extracts the top-level "cwd" string field from a Claude record's
// raw JSON line, if present. false means absent, unreadable, or not a
// string — never a hard error, since Raw is vendor-owned and this is an
// observational signal only.
func rawCWD(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", false
	}
	cwdRaw, ok := fields["cwd"]
	if !ok {
		return "", false
	}
	var cwd string
	if err := json.Unmarshal(cwdRaw, &cwd); err != nil {
		return "", false
	}
	return cwd, true
}
