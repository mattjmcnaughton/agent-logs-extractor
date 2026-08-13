package jsonlstore

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

func TestEscapeElementIsSingleElementAndInjective(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"plain", "abc-123_XYZ.txt"},
		{"traversal", "../../etc/passwd"},
		{"slash", "a/b"},
		{"colon", "a:b"},
		{"percent", "a%2Fb"},
		{"unicode", "héllo wörld"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeElement(tc.in)
			if strings.ContainsAny(got, "/\\") {
				t.Fatalf("escapeElement(%q) = %q contains a path separator", tc.in, got)
			}
			for i := 0; i < len(got); i++ {
				if !isUnreservedByte(got[i]) && got[i] != '%' {
					t.Fatalf("escapeElement(%q) = %q has byte %q outside [A-Za-z0-9._-%%]", tc.in, got, got[i])
				}
			}
		})
	}

	// Injectivity: "a/b" and "a%2Fb" must escape to different strings, or
	// two distinct session ids would collide on one filename.
	a := escapeElement("a/b")
	b := escapeElement("a%2Fb")
	if a == b {
		t.Fatalf("escapeElement(%q) == escapeElement(%q) == %q: not injective", "a/b", "a%2Fb", a)
	}
}

func TestRelPathSessionFileNaming(t *testing.T) {
	longRest := strings.Repeat("a", 250) // escapes to itself (all unreserved), so len > maxStem

	tests := []struct {
		name      string
		sess      model.Session
		wantErr   error
		checkPath func(t *testing.T, rel string)
	}{
		{
			name: "simple uuid",
			sess: model.Session{Vendor: model.VendorClaude, SessionID: "claude:1234-uuid"},
			checkPath: func(t *testing.T, rel string) {
				if rel != "claude/1234-uuid.json" {
					t.Errorf("rel = %q, want %q", rel, "claude/1234-uuid.json")
				}
			},
		},
		{
			name: "traversal id stays one escaped element",
			sess: model.Session{Vendor: model.VendorClaude, SessionID: "claude:../../etc/passwd"},
			checkPath: func(t *testing.T, rel string) {
				if !strings.HasPrefix(rel, "claude/") {
					t.Fatalf("rel = %q, want it to stay under claude/", rel)
				}
				stem := strings.TrimSuffix(strings.TrimPrefix(rel, "claude/"), ".json")
				// The safety property is "no path separator" — a literal
				// ".." substring (from the unescaped dots in "../../etc")
				// is harmless once there is no "/" around it to make it a
				// traversal segment.
				if strings.ContainsAny(stem, "/\\") {
					t.Errorf("stem %q is not a single safe path element", stem)
				}
			},
		},
		{
			name:    "empty vendor",
			sess:    model.Session{Vendor: "", SessionID: "claude:1234"},
			wantErr: ErrInvalidSessionDoc,
		},
		{
			name:    "empty session id",
			sess:    model.Session{Vendor: model.VendorClaude, SessionID: ""},
			wantErr: ErrInvalidSessionDoc,
		},
		{
			name:    "not vendor-namespaced",
			sess:    model.Session{Vendor: model.VendorClaude, SessionID: "1234-uuid"},
			wantErr: ErrInvalidSessionDoc,
		},
		{
			name:    "wrong vendor prefix",
			sess:    model.Session{Vendor: model.VendorClaude, SessionID: "codex:1234-uuid"},
			wantErr: ErrInvalidSessionDoc,
		},
		{
			name:    "vendor prefix with nothing after",
			sess:    model.Session{Vendor: model.VendorClaude, SessionID: "claude:"},
			wantErr: ErrInvalidSessionDoc,
		},
		{
			name: "over-length id hits the hash path",
			sess: model.Session{Vendor: model.VendorClaude, SessionID: "claude:" + longRest},
			checkPath: func(t *testing.T, rel string) {
				stem := strings.TrimSuffix(strings.TrimPrefix(rel, "claude/"), ".json")
				if len(stem) > maxStem+40 {
					t.Errorf("stem %q too long: %d bytes", stem, len(stem))
				}
				if stem == escapeElement(longRest) {
					t.Errorf("stem was not truncated/hashed for an over-length id")
				}
				if !strings.Contains(stem, "-") {
					t.Errorf("stem %q missing hash suffix separator", stem)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rel, err := relPath(tc.sess)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("relPath(%+v) error = %v, want errors.Is(_, %v)", tc.sess, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("relPath(%+v) unexpected error: %v", tc.sess, err)
			}
			tc.checkPath(t, rel)
		})
	}
}

func TestRelPathInjectivityAcrossSimilarIDs(t *testing.T) {
	sessA := model.Session{Vendor: model.VendorClaude, SessionID: "claude:a/b"}
	sessB := model.Session{Vendor: model.VendorClaude, SessionID: "claude:a%2Fb"}

	relA, err := relPath(sessA)
	if err != nil {
		t.Fatalf("relPath(sessA): %v", err)
	}
	relB, err := relPath(sessB)
	if err != nil {
		t.Fatalf("relPath(sessB): %v", err)
	}
	if relA == relB {
		t.Fatalf("distinct session ids %q and %q collided on path %q", sessA.SessionID, sessB.SessionID, relA)
	}
}

func TestRelPathRoundTripsBelowLengthCap(t *testing.T) {
	ids := []string{
		"claude:1234-uuid",
		"claude:has space",
		"claude:has:colon",
		"claude:../traversal",
		"claude:unicode-héllo",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			sess := model.Session{Vendor: model.VendorClaude, SessionID: id}
			rel, err := relPath(sess)
			if err != nil {
				t.Fatalf("relPath: %v", err)
			}
			stem := strings.TrimSuffix(strings.TrimPrefix(rel, "claude/"), ".json")
			recovered := "claude:" + mustUnescapeElement(t, stem)
			if recovered != id {
				t.Errorf("round trip: got %q, want %q", recovered, id)
			}
		})
	}
}

// mustUnescapeElement inverts escapeElement for test assertions only; the
// production code never needs to unescape a filename back to a session id.
func mustUnescapeElement(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) {
				t.Fatalf("malformed percent-escape in %q at %d", s, i)
			}
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				t.Fatalf("malformed percent-escape in %q: %v", s, err)
			}
			b.WriteByte(byte(v))
			i += 2
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
