//go:build contract

package codexsource_test

import (
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/adapters/codexsource"
	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/contracts"
	"log/slog"
	"testing"
)

func TestCodexSourceContractAgainstLiveLogs(t *testing.T) {
	contracts.RunLocal(t, codexsource.New(slog.New(slog.DiscardHandler)), "ALX_CONTRACT_CODEX_PATH")
}
