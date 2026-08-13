package jsonlstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// ErrInvalidSessionDoc is returned by Put for a doc whose session cannot be
// named on disk: an empty session id, an empty vendor, or a session id that
// is not vendor-namespaced as "<vendor>:<rest>" with a non-empty rest.
var ErrInvalidSessionDoc = errors.New("jsonlstore: session doc has no usable session id")

// maxStem caps the escaped stem; beyond it the stem becomes the first 160
// bytes of the escaped form plus a hash suffix, so every filename stays a
// reasonable length regardless of how long a vendor's session id gets.
const maxStem = 200

// relPath maps sess to "<vendor>/<stem>.json" beneath the sessions root.
// The session id must be exactly "<vendor>:<rest>" with rest non-empty;
// anything else is ErrInvalidSessionDoc (TDD decision, #7 plan §D2).
func relPath(sess model.Session) (string, error) {
	vendor := string(sess.Vendor)
	id := sess.SessionID
	if vendor == "" || id == "" {
		return "", fmt.Errorf("%w: vendor=%q session_id=%q", ErrInvalidSessionDoc, vendor, id)
	}

	prefix := vendor + ":"
	if !strings.HasPrefix(id, prefix) {
		return "", fmt.Errorf("%w: session_id %q is not namespaced as %q", ErrInvalidSessionDoc, id, prefix)
	}

	rest := strings.TrimPrefix(id, prefix)
	if rest == "" {
		return "", fmt.Errorf("%w: session_id %q has no id after the vendor prefix", ErrInvalidSessionDoc, id)
	}

	return filepath.Join(escapeElement(vendor), stemFor(id, rest)+".json"), nil
}

// stemFor returns the filename stem for a session whose id is fullID
// ("<vendor>:<rest>") and whose post-prefix portion is rest. Below the
// length cap it is simply the escaped rest, which round-trips
// (vendor + ":" + unescape(stem) recovers fullID exactly). Beyond the cap
// it truncates and appends a hash of the full id instead: still injective
// in practice, no longer round-trippable, which is acceptable because
// nothing reads the session id back off the filename.
func stemFor(fullID, rest string) string {
	escaped := escapeElement(rest)
	if len(escaped) <= maxStem {
		return escaped
	}
	sum := sha256.Sum256([]byte(fullID))
	return escaped[:160] + "-" + hex.EncodeToString(sum[:])[:16]
}

// escapeElement percent-escapes every byte outside [A-Za-z0-9._-] as %XX
// (uppercase hex), yielding exactly one path element: no separator, no
// colon, no traversal, portable to Windows. Injective by construction, so
// distinct inputs always produce distinct outputs.
func escapeElement(s string) string {
	var needsEscape bool
	for i := 0; i < len(s); i++ {
		if !isUnreservedByte(s[i]) {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreservedByte(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func isUnreservedByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '.' || c == '_' || c == '-':
		return true
	default:
		return false
	}
}

// encodeDoc marshals doc as one compact JSON object terminated by a
// trailing newline, with HTML-escaping disabled (TDD decision, #7 plan
// §D2) so '<', '>', '&' and json.RawMessage payloads survive verbatim. The
// result is, incidentally, a valid single-record newline-delimited-JSON
// file.
func encodeDoc(doc model.SessionDoc) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
