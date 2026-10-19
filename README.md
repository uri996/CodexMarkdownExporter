# codex-exporter

Export Codex session rollout files (`rollout-*.jsonl`) to clean Markdown.

This fork of [faceair/codex-exporter](https://github.com/faceair/codex-exporter) retains the upstream MIT license and attribution.

## Features

- Lists sessions matching the current working directory by default; use `-all` to list every session.
- Opens the existing interactive picker; `-last` and `-session-id` skip the picker.
- By default, exports only real user prompts and assistant final replies (`final` / `final_answer`) in conversation order.
- Writes a session title followed by `# 🧑 User` and `# 💻 Assistant` sections, with a `---` separator after every message, including the last one. No exporter markers, speaker fields, phase fields, or session metadata are added.
- Preserves message Markdown, code blocks, comments, and ordinary XML. Internal assistant memory-citation blocks continue to be excluded.
- Expands Codex pasted-text attachments, including multiple attachments and surrounding user text.
- Names files `YYYYMMDD_<Session Title>.md` using the session creation date.
- Uses your explicitly chosen, saved output root for both export modes. No automatic fallback to a system drive or the source checkout is made. Use `-output-dir` for a temporary override.
- Offers an independent project batch mode, with one Markdown per interactive session and persistent skip/retry bookkeeping outside the repository.

## Requirements and usage

Go `1.24.2` or later. Run this fork from its local checkout:

```powershell
go run . -set-output-dir "F:\Codex-Exports"
go run . -all
```

`-set-output-dir` saves or changes the default directory and exits without exporting. Settings live in `os.UserConfigDir()/codex-exporter/settings.json` (on Windows, `%APPDATA%\codex-exporter\settings.json`), outside this checkout. The small JSON file stores only the chosen output directory, never conversation content. Existing exports are never moved or deleted when changing this setting.

If no directory has been saved, an interactive terminal asks for a directory and explicitly asks whether to save it. Answering no uses the choice for this run only. A noninteractive invocation without a saved directory fails with instructions; supply `-output-dir`, or `-output` for a single file, to run without saving settings. Invalid or unreadable settings also produce an error rather than choosing a fallback. Explicit destinations remain usable even when the settings file needs repair.

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
| `-batch` | `false` | Choose a project and export its interactive sessions; independent of the Session Picker |
| `-force` | `false` | Overwrite an explicitly specified output file |
| `-full` | `false` | Existing opt-in mode including non-final assistant phases; no exporter metadata is added |
| `-last` | `false` | Export the latest session without the picker |
| `-output` | empty | Custom Markdown output path, used exactly as provided |
| `-output-dir` | saved user choice | Temporary export root; never changes the saved default; supports absolute, relative, Chinese and space-containing paths |
| `-set-output-dir` | empty | Save or change the default export root, then exit; do not combine with export flags |
| `-project` | empty | Project directory for batch mode; skips the project picker |
| `-session-id` | empty | Select a session ID without the picker |
| `-sessions-dir` | `~/.codex/sessions` | Codex sessions directory |

```powershell
# Export the latest session as Markdown
go run . -all -last

# Explicit destination; overwrite only with -force
go run . -all -session-id 11111111-1111-1111-1111-111111111111 -output ./session.md -force

# Single export to a custom root, without selecting a project
go run . -output-dir "F:\Codex-Exports"

# Select a project and export all its eligible sessions
go run . -batch

# Select the project directly
go run . -batch -project "F:\GitHub_Projects\Example" -output-dir "F:\Codex-Exports"
```

`-output` and explicit `-output-dir` are mutually exclusive. `-project` requires `-batch`. Batch mode rejects single-mode flags `-output`, `-last`, `-session-id`, `-all`, `-full`, and `-force`, including explicitly supplied false boolean flags. Batch mode always exports all available turns with the V1 User/final-answer filter; it never loops through the Session Picker. Single mode retains its existing selection flags and opt-in `-full` behavior and does not read or initialize any batch index.

Single export reports incomplete discovery but remains usable when the requested session is available. Batch mode requires an explicitly complete result and returns nonzero for discovery issues after processing the usable sessions.

## Project batch exports

The layout under your chosen root is:

```text
<chosen export root>/
    YYYYMMDD_Single-session.md
    Project-name/
        YYYYMMDD_Session-A.md
        YYYYMMDD_Session-B.md
    .codex-exporter/
        index/
            <project-fingerprint>.json
```

Discovery scans the configured sessions directory and its sibling `archived_sessions` when present. Missing optional archives are normal. Only `cli` and `vscode` sources qualify; subagents remain excluded. No sqlite3 installation is required. Scan and summary-read errors are reported, and an incomplete scan returns a failure status even if some files were exported. The CLI distinguishes discovered rollout files, interactive records, project records, exact duplicates, conflicting same-ID records, successful files, existing skips, read/write/index failures, and attachment warnings.

Projects use normalized complete paths, including Windows case and separator normalization and safely resolved links. The project picker shows the name, full selected path, and session-record count. Automatic grouping uses the nearest Git root, recognizing `.git` directories and worktree files. Non-Git sessions use their exact cwd. An explicitly selected root includes descendant directories but excludes independently nested Git repositories; a same-prefix sibling is never included. Missing historical cwd directories are grouped only by their exact recorded path and marked historical. Missing descendant directories cannot be reliably assigned to an explicit project and are reported instead of guessed.

Project directory names preserve Chinese and are sanitized for Windows, including reserved device names. Different projects with the same sanitized folder name get a short stable path fingerprint. All path-length checks use the actual destination directory, not the shell's current directory.

### Repeat runs, conflicts and recovery

Batch source identity hashes the metadata session ID and normalized rollout path. Appending messages at the same path does not create a new identity. Raw source SHA-256 fingerprints identify exact duplicate files, including archive copies; different contents with the same metadata ID are retained and counted as conflicts. Different physical paths with changed content are conservatively distinct, so moved-and-updated sources may require a fresh output root to reconcile manually.

An existing successful output is skipped, including when the source gains turns or the Markdown was edited by hand. Use a new `-output-dir` to export updated snapshots. Different sessions with the same title receive `_2`, `_3`, etc. Unregistered existing Markdown files are never overwritten. Indexes contain only hashes, relative filenames, project-folder names and pending/done status; they do not contain prompts, answers or absolute source paths.

Indexes are written using same-directory temporary files and replacement. A filename and expected output hash are recorded as pending before exclusive file creation. If interrupted after the file was written, the next run verifies its hash and completes bookkeeping. A modified or partial pending file is preserved and reported; remove that failed target or use a new root to retry. Missing output files can be recreated. Corrupt indexes or missing indexes alongside existing project directories cause an explicit failure instead of guessing ownership or producing repeated copies. Use a fresh output root in that case.

Batch writes in one root are protected by `.codex-exporter/batch.lock`. A crash can leave this lock behind; only remove it after confirming no batch export is running. This lock never affects single exports. Per-session failures do not stop the other sessions; the final exit status is nonzero after processing if actual failures or incomplete discovery occurred. A successfully written Markdown containing an unavailable-attachment diagnostic is counted as exported with an attachment warning.

## File names

Default filenames use the session creation date and title, preserving Chinese, English, digits, hyphens, and underscores. Windows-forbidden characters and control characters are replaced with hyphens; whitespace is collapsed to a hyphen and trailing spaces and periods are removed. Titles are limited to 100 UTF-16 units, reduced further when necessary to keep the absolute generated path within 240 units. A directory that leaves insufficient room produces an error asking for a shorter output directory or explicit file path. Parent directories are created on write.

Missing or empty sanitized titles use `YYYYMMDD_codex-session_<8-character-id>.md`. An unavailable creation date is represented by `00000000`, never the export date. Existing default filenames get `_2`, `_3`, and so on, using exclusive file creation; even `-force` does not overwrite a default filename. Explicit `-output` destinations are never renamed and require `-force` to overwrite.

## User context filtering

Desktop rollouts label individual content parts in `internal_chat_message_metadata_passthrough.content_item_kinds`. Known injected kinds for plugin recommendations, repository instructions, environment context, open-page context, automation heartbeats, and selected-skill instructions are removed before message text is joined and before attachments are expanded. Explicit `user.text` and `user.image` parts are retained.

Older records without these labels use conservative complete-wrapper checks for the observed plugin recommendation introduction and repository-instructions wrapper. Known environment blocks are removed at line boundaries: either cwd/shell context, or the verified current-date/timezone/filesystem/workspace-roots/permission-profile structure. Surrounding prompt text is retained. Legacy IDE preambles beginning with `# Context from my IDE setup:` and containing the observed IDE sections and `## Open tabs:` are removed only through the matching `## My request for Codex:` boundary; the actual request after it is preserved. CRLF and repeated preambles are supported. Parts explicitly labeled `user.text`, unknown kinds, mismatched label arrays, ordinary XML, fenced/indented code, inline examples, and ambiguous content are preserved. Filtering happens on original content parts, never recursively on expanded attachment contents. Pure injected user parts are discarded before Markdown generation.

## Pasted-text attachments

Recognized `Files pasted by the user:` wrappers with quoted preview entries are expanded. Both the standard `Pasted text contains the user's request.` ending and the observed historical form ending directly at `## My request:` are supported. The request text following that boundary remains in order, and wrapper examples inside Markdown code fences are preserved. Paths must identify a regular UTF-8 file named `Pasted text.txt`, `pasted-text.txt`, or the observed Desktop localized name `已粘贴的文本.txt` under `<attachment root>/<uuid>/`. The root is `$CODEX_HOME/attachments` when configured, otherwise `~/.codex/attachments`. Native absolute and `~/` paths are supported; paths from another computer are not remapped.

Traversal and symlink escapes are rejected, including during file opening. Unavailable attachments produce a diagnostic distinguishing missing files/directories, rejected paths, unrecognized path structure/filenames, non-regular files, invalid UTF-8, permissions, and other open/read failures. Raw errors and attachment paths are not included in these diagnostics. Unknown wrapper schemas remain unchanged. Ordinary paths never trigger file reads; attachment contents are not recursively interpreted as wrappers.

## Testing

```powershell
gofmt -w *.go
go test -count=1 ./...
```

Fixtures are synthetic and contain no private conversations. Windows tests also cover junction escapes and files unavailable due to exclusive handles. Symbolic-link tests skip when the operating system does not permit creating them.

Directory, batch, project-boundary, index/recovery and archive tests also use synthetic data and isolated temporary output roots. Real local acceptance exports must be kept outside the checkout and never added to Git. Archived-directory behavior is covered synthetically; it must not be claimed as verified against real archived sessions when none are available locally.

## Release CI

Release builds are triggered by tags matching `v*`.

## License

MIT. See [LICENSE](./LICENSE).
