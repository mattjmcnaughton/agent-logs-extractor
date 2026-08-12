# agent-logs-extractor

Parse AI coding agent conversation logs (Claude Code, Codex) into a unified, queryable data model

## Installation

```sh
go install github.com/mattjmcnaughton/agent-logs-extractor/cmd/agent-logs-extractor@latest
```

Or build from source:

```sh
git clone <repo>
cd agent-logs-extractor
go mod tidy
just build
```

## Usage

```sh
agent-logs-extractor --help
agent-logs-extractor example [name]
```

### Environment Variables

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `AGENT_LOGS_EXTRACTOR_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |

## Development

See [docs/development.md](docs/development.md) for setup instructions and common tasks.

## License

MIT — see [LICENSE](LICENSE).

