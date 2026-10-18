# codex-exporter

Export Codex session rollout files (`rollout-*.jsonl`) to clean Markdown.

This fork of [faceair/codex-exporter](https://github.com/faceair/codex-exporter) retains the upstream MIT license and attribution.

## Features

- Lists sessions matching the current working directory by default; use `-all` to list every session.
- Opens the existing interactive picker; `-last` and `-session-id` skip the picker.
- By default, exports only real user prompts and assistant final replies (`final` / `final_answer`) in conversation order.
- Writes a session title followed by `# 👤 User` and `# 💻 Assistant` sections, with a `---` separator after every message, including the last one. No exporter markers, speaker fields, phase fields, or session metadata are added.
- Preserves message Markdown, code blocks, comments, and ordinary XML. Internal assistant memory-citation blocks continue to be excluded.
- Expands Codex pasted-text attachments, including multiple attachments and surrounding user text.
- Names files `YYYYMMDD_<Session Title>.md` using the session creation date.

## Requirements and usage

Go `1.24.2` or later. Run this fork from its local checkout:

```powershell
go run . -all
```

Or build and run:

```powershell
go build -o codex-exporter.exe .
.\codex-exporter.exe -all
```

## CLI options

These options match `go run . -help`:

| Flag | Default | Description |
| --- | --- | --- |
| `-all` | `false` | Show all sessions instead of filtering by current directory |
| `-force` | `false` | Overwrite an explicitly specified output file |
| `-full` | `false` | Existing opt-in mode including non-final assistant phases; no exporter metadata is added |
| `-last` | `false` | Export the latest session without the picker |
| `-output` | empty | Custom Markdown output path, used exactly as provided |
| `-session-id` | empty | Select a session ID without the picker |
| `-sessions-dir` | `~/.codex/sessions` | Codex sessions directory |

```powershell
# Export the latest session as Markdown
go run . -all -last

# Explicit destination; overwrite only with -force
go run . -all -session-id 11111111-1111-1111-1111-111111111111 -output ./session.md -force
```

## File names

Default filenames use the session creation date and title, preserving Chinese, English, digits, hyphens, and underscores. Windows-forbidden characters and control characters are replaced with hyphens; whitespace is collapsed to a hyphen and trailing spaces and periods are removed. Titles are limited to 100 UTF-16 units, reduced further when necessary to keep the absolute default path within 240 units. A directory that leaves insufficient room produces an error asking for a shorter explicit output path.

Missing or empty sanitized titles use `YYYYMMDD_codex-session_<8-character-id>.md`. An unavailable creation date is represented by `00000000`, never the export date. Existing default filenames get `_2`, `_3`, and so on, using exclusive file creation; even `-force` does not overwrite a default filename. Explicit `-output` destinations are never renamed and require `-force` to overwrite.

## User context filtering

Desktop rollouts label individual content parts in `internal_chat_message_metadata_passthrough.content_item_kinds`. Known injected kinds for plugin recommendations, repository instructions, environment context, open-page context, automation heartbeats, and selected-skill instructions are removed before message text is joined and before attachments are expanded. Explicit `user.text` and `user.image` parts are retained.

Older records without these labels use conservative complete-wrapper checks for the observed plugin recommendation introduction and environment context, plus the complete repository-instructions wrapper. Unknown kinds, mismatched label arrays, ordinary XML, inline examples, and ambiguous content are preserved. Filtering never runs recursively on attachment contents.

## Pasted-text attachments

Only complete `Files pasted by the user:` / `Pasted text contains the user's request.` wrappers with quoted preview entries are expanded. Paths must identify a regular UTF-8 file named `Pasted text.txt`, `pasted-text.txt`, or the observed Desktop localized name `已粘贴的文本.txt` under `<attachment root>/<uuid>/`. The root is `$CODEX_HOME/attachments` when configured, otherwise `~/.codex/attachments`. Native absolute and `~/` paths are supported; paths from another computer are not remapped.

Traversal and symlink escapes are rejected, including during file opening. Unavailable attachments produce a diagnostic distinguishing missing files/directories, rejected paths, unrecognized path structure/filenames, non-regular files, invalid UTF-8, permissions, and other open/read failures. Raw errors and attachment paths are not included in these diagnostics. Unknown wrapper schemas remain unchanged. Ordinary paths never trigger file reads; attachment contents are not recursively interpreted as wrappers.

## Testing

```powershell
gofmt -w *.go
go test -count=1 ./...
```

Fixtures are synthetic and contain no private conversations. Windows tests also cover junction escapes and files unavailable due to exclusive handles. Symbolic-link tests skip when the operating system does not permit creating them.

## Release CI

Release builds are triggered by tags matching `v*`.

## License

MIT. See [LICENSE](./LICENSE).
