//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
)

// allZeroSummary is the exact stdout AC-VENDOR-01/02 pin.
const allZeroSummary = "claude: 0 sessions, 0 messages, 0 tool calls, 0 records skipped\n"

// AC-VENDOR-01
func TestAC_VENDOR_01_MissingVendorDirIsFine(t *testing.T) {
	s := newSandbox(t) // ~/.claude is never seeded, so it does not exist
	res := s.run("sync")
	wantCode(t, res, 0)
	wantStdout(t, res, allZeroSummary)
}

// AC-VENDOR-02
func TestAC_VENDOR_02_MissingOverridePathIsFine(t *testing.T) {
	s := newSandbox(t)
	res := s.run("sync", "--claude-path", filepath.Join(s.home, "does-not-exist"))
	wantCode(t, res, 0)
	wantStdout(t, res, allZeroSummary)
}
