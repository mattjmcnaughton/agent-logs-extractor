package ports

import (
	"testing"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/core/model"
)

// TestValidSessionDoc pins the shared predicate's exact rejection set: this
// is the single source of truth jsonlstore.relPath, FakeStoreRebuild.Put,
// and #8's pre-Put filter all defer to, so a gap here is a gap in all three.
func TestValidSessionDoc(t *testing.T) {
	cases := []struct {
		name string
		sess model.Session
		want bool
	}{
		{"zero value", model.Session{}, false},
		{"empty vendor, non-empty id", model.Session{SessionID: "claude:abc"}, false},
		{"empty id", model.Session{Vendor: model.VendorClaude}, false},
		{"not namespaced", model.Session{Vendor: model.VendorClaude, SessionID: "abc"}, false},
		{"bare prefix, no rest", model.Session{Vendor: model.VendorClaude, SessionID: "claude:"}, false},
		{"wrong vendor prefix", model.Session{Vendor: model.VendorClaude, SessionID: "codex:abc"}, false},
		{"traversal vendor dot", model.Session{Vendor: ".", SessionID: ".:abc"}, false},
		{"traversal vendor dotdot", model.Session{Vendor: "..", SessionID: "..:abc"}, false},
		{"valid claude", model.Session{Vendor: model.VendorClaude, SessionID: "claude:abc"}, true},
		{"valid codex", model.Session{Vendor: model.VendorCodex, SessionID: "codex:xyz"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidSessionDoc(tc.sess); got != tc.want {
				t.Errorf("ValidSessionDoc(%+v) = %v, want %v", tc.sess, got, tc.want)
			}
		})
	}
}
