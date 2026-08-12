package version

// Version is the current version of agent-logs-extractor.
// Override at build time with:
//
//	go build -ldflags "-X github.com/mattjmcnaughton/agent-logs-extractor/internal/version.Version=x.y.z" ./cmd/agent-logs-extractor
var Version = "dev"
