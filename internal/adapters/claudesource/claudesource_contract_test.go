//go:build contract

package claudesource_test

import (
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/claudesource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/contracts"
	"log/slog"
	"testing"
)

func TestClaudeSourceContractAgainstLiveLogs(t *testing.T) {
	contracts.RunLocal(t, claudesource.New(slog.New(slog.DiscardHandler)), "ALX_CONTRACT_CLAUDE_PATH")
}
