package jsonlstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/ports"
)

// ErrInvalidSessionDoc is returned by Put for a doc whose session cannot be
// named on disk: an empty session id, an empty vendor, or a session id that
// is not vendor-namespaced as "<vendor>:<rest>" with a non-empty rest. It
// wraps ports.ErrInvalidSessionDoc so callers can errors.Is against either
// the adapter-specific or the port-level sentinel.
var ErrInvalidSessionDoc = fmt.Errorf("jsonlstore: session doc has no usable session id: %w", ports.ErrInvalidSessionDoc)

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

	vendorElem := escapeElement(vendor)
	// '.' is unreserved, so escapeElement passes a vendor of "." or ".."
	// through unchanged, and relPath uses it as a directory element:
	// unrejected, that turns Put into a traversal write outside the
	// staging tree (e.g. Vendor=".." lands the file as a sibling of
	// staging, inside the store root but outside "<staging>/"). Nothing in
	// ports.CanonicalStore constrains Vendor — it happens to be a source
	// adapter constant today — so this rejects the escaped form directly
	// rather than relying on that being true forever.
	if vendorElem == "." || vendorElem == ".." {
		return "", fmt.Errorf("%w: vendor %q escapes to the traversal element %q", ErrInvalidSessionDoc, vendor, vendorElem)
	}

	return filepath.Join(vendorElem, stemFor(id, rest)+".json"), nil
}

// hashSep separates the truncated escaped prefix from the hash suffix in
// stemFor's over-length path. escapeElement can never emit "%2D": '-' is
// unreserved, so a literal '-' always passes through as itself, never as
// an escape; the only way "%2D" could appear in escapeElement's output
// would be a literal "%2D" in the input, but '%' is not unreserved, so
// that "%" itself gets escaped to "%25" first, yielding "%252D", not
// "%2D". That makes hashSep an unforgeable separator: no escaped rest can
// ever contain it, so the hashed form can never collide with a shorter
// id's verbatim escaped stem (see stemFor's doc for the concrete
// collision this closes).
const hashSep = "%2D"

// stemFor returns the filename stem for a session whose id is fullID
// ("<vendor>:<rest>") and whose post-prefix portion is rest. Below the
// length cap it is simply the escaped rest, which round-trips
// (vendor + ":" + unescape(stem) recovers fullID exactly). Beyond the cap
// it truncates and appends a hash of the full id instead, joined by
// hashSep rather than a literal '-': with a literal '-' (unreserved, so
// escapeElement never escapes it), a short id can pass through verbatim
// and land exactly on a long id's truncated+hashed stem — concretely,
// "claude:" + 250×'a' hashes to "aaa…a(160)-8a9d854c5de25ce2", which is
// also the literal (round-trippable) escaped stem of
// "claude:" + 160×'a' + "-8a9d854c5de25ce2": two distinct sessions would
// silently overwrite each other. hashSep can never appear in an escaped
// stem (see its doc), so the hashed form is now unreachable by any
// pass-through short id.
func stemFor(fullID, rest string) string {
	escaped := escapeElement(rest)
	if len(escaped) <= maxStem {
		return escaped
	}
	sum := sha256.Sum256([]byte(fullID))
	return escaped[:160] + hashSep + hex.EncodeToString(sum[:])[:16]
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
