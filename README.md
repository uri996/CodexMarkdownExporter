# codex-exporter

Export Codex session rollout files (`rollout-*.jsonl`) to Markdown.

## Features

- Scans the Codex sessions directory for exportable sessions.
- By default, only lists and exports sessions matching the current working directory (`cwd`); use `-all` to disable this filter.
- Opens the interactive session picker by default; use `-last` or `-session-id` to skip it.
- By default, exports only user messages and the assistant final reply (`final` / `final_answer`).
- Use `-full` to export the complete conversation (including non-final assistant phases) and include full metadata at the top of the Markdown file.
- Default output file is `./codex-session-<session-id>.md`; existing files require `-force` to overwrite.

## Requirements

- Go `1.24.2` or later (as defined in `go.mod`).

## Install and Run

### Option 1: Install to local Go bin

```bash
go install github.com/faceair/codex-exporter@latest
codex-exporter
```

### Option 2: Run directly

```bash
go run .
```

### Option 3: Build then run

```bash
go build -o codex-exporter .
./codex-exporter
```

## CLI Options

The following options are aligned with `go run . -h`:

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `-all` | bool | `false` | Show all sessions (disable current-cwd filter) |
| `-force` | bool | `false` | Overwrite an existing output file |
| `-full` | bool | `false` | Export full conversation (include non-final assistant phases) |
| `-last` | bool | `false` | Export latest session without picker |
| `-output` | string | empty | Output Markdown file path |
| `-session-id` | string | empty | Export by session ID without picker |
| `-sessions-dir` | string | `~/.codex/sessions` | Codex sessions directory |

## Usage Examples

```bash
# Interactively select a session for the current directory and export it
go run .

# Show sessions from all directories, then select one
go run . -all

# Export the latest session directly
go run . -last

# Export a specific session ID to a fixed file path and allow overwrite
go run . -session-id 11111111-1111-1111-1111-111111111111 -output ./session.md -force

# Export the full conversation (including non-final assistant phases)
go run . -last -full
```

## Testing

```bash
go test ./...
```

## Release CI

Release builds are triggered by pushing tags that match `v*`.

## License

MIT. See [LICENSE](./LICENSE).
