package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yuin/goldmark"
)

const (
	defaultSessionsDir = "~/.codex/sessions"
	scannerBufferSize  = 16 * 1024 * 1024
)

var rolloutNamePattern = regexp.MustCompile(`^rollout-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-.+\.jsonl$`)
var stateDBPattern = regexp.MustCompile(`^state_(\d+)\.sqlite$`)
var memoryCitationBlockPattern = regexp.MustCompile(`(?s)\n*<oai-mem-citation>\s*.*?</oai-mem-citation>\s*`)
var excessiveBlankLinesPattern = regexp.MustCompile(`\n{3,}`)

type exportFormat string

const (
	formatMarkdown exportFormat = "markdown"
	formatHTML     exportFormat = "html"
)

type options struct {
	sessionsDir string
	showAll     bool
	output      string
	format      string
	last        bool
	sessionID   string
	force       bool
	full        bool
}

type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID            string          `json:"id"`
	Timestamp     string          `json:"timestamp"`
	Cwd           string          `json:"cwd"`
	SourceRaw     json.RawMessage `json:"source"`
	ModelProvider string          `json:"model_provider"`
}

type sessionIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

type responseItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role,omitempty"`
	Phase   string        `json:"phase,omitempty"`
	Content []contentItem `json:"content,omitempty"`
}

type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type sessionSummary struct {
	Path          string
	SessionID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Cwd           string
	Source        string
	ModelProvider string
	Title         string
}

type exportMessage struct {
	Role  string
	Phase string
	Body  string
}

type conversationExporter struct {
	summary     sessionSummary
	meta        sessionMetaPayload
	messages    []exportMessage
	includeMeta bool
}

type htmlMetaRow struct {
	Label string
	Value string
}

type htmlMessageView struct {
	Index     int
	Role      string
	RoleClass string
	RoleTitle string
	Phase     string
	Body      template.HTML
}

type htmlPageData struct {
	Title               string
	Heading             string
	IncludeMeta         bool
	ConversationHeading string
	MetaRows            []htmlMetaRow
	Messages            []htmlMessageView
	ExportedAt          string
}

const sessionHTMLTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="generator" content="codex-exporter">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: light dark; --bg: #f7f3ec; --panel: #fffaf2; --muted: #746d64; --border: #ded3c2; --accent: #4c6fff; }
    @media (prefers-color-scheme: dark) { :root { --bg: #151515; --panel: #202020; --muted: #aaa197; --border: #3b3833; --accent: #91a4ff; } }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--bg); font: 16px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    main { width: min(980px, calc(100% - 32px)); margin: 40px auto 72px; }
    h1 { margin: 0 0 8px; font-size: clamp(2rem, 5vw, 4rem); line-height: 1; letter-spacing: -0.04em; }
    .page-header { margin-bottom: 28px; padding-bottom: 18px; border-bottom: 1px solid var(--border); }
    .export-meta, .transcript-title, .message-header, .metadata dt { color: var(--muted); }
    .metadata { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 6px 14px; margin: 0 0 28px; padding: 16px; border: 1px solid var(--border); border-radius: 14px; }
    .metadata dd { margin: 0; overflow-wrap: anywhere; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .9rem; }
    .transcript-title { margin: 0 0 14px; font-size: .86rem; text-transform: uppercase; letter-spacing: .12em; }
    .message { margin: 0 0 18px; overflow: hidden; background: var(--panel); border: 1px solid var(--border); border-radius: 16px; }
    .message-header { display: flex; gap: 10px; padding: 9px 14px; border-bottom: 1px solid var(--border); font-size: .84rem; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; }
    .message-header .phase, a { color: var(--accent); text-transform: none; letter-spacing: 0; }
    .message-body { padding: 16px 18px 20px; overflow-wrap: anywhere; }
    .message-body > :first-child { margin-top: 0; } .message-body > :last-child { margin-bottom: 0; }
    pre { overflow: auto; padding: 12px; border-radius: 10px; background: rgba(127,127,127,.13); }
    code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .92em; }
    blockquote { margin: 1em 0; padding-left: 1em; color: var(--muted); border-left: 3px solid var(--border); }
  </style>
</head>
<body>
  <main data-codex-exporter-format="html">
    <header class="page-header">
      <h1>{{.Heading}}</h1>
      <p class="export-meta">Exported by <a href="https://github.com/faceair/codex-exporter" rel="noopener noreferrer">codex-exporter</a> at <time>{{.ExportedAt}}</time></p>
    </header>
    {{if .IncludeMeta}}
    <dl class="metadata">{{range .MetaRows}}<dt>{{.Label}}</dt><dd>{{.Value}}</dd>{{end}}</dl>
    {{end}}
    <section aria-label="{{.ConversationHeading}}">
      <h2 class="transcript-title">{{.ConversationHeading}}</h2>
      {{if .Messages}}{{range .Messages}}
      <article class="message role-{{.RoleClass}}" data-index="{{.Index}}" data-role="{{.Role}}"{{with .Phase}} data-phase="{{.}}"{{end}}>
        <header class="message-header"><span>{{.RoleTitle}}</span>{{with .Phase}}<span class="phase">{{.}}</span>{{end}}</header>
        <div class="message-body">
{{.Body}}
        </div>
      </article>
      {{end}}{{else}}<p><em>No user/assistant message found in this session.</em></p>{{end}}
    </section>
  </main>
</body>
</html>
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts := parseFlags()
	format, err := parseExportFormat(opts.format)
	if err != nil {
		return err
	}
	sessionsDir := expandHome(opts.sessionsDir)
	currentCwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current working directory: %w", err)
	}

	summaries, err := listSessions(sessionsDir, currentCwd, opts.showAll)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		return errors.New("no sessions found")
	}

	target, err := selectTargetSession(summaries, opts)
	if err != nil {
		return err
	}
	if target == nil {
		fmt.Println("cancelled")
		return nil
	}

	outputPath := determineOutputPath(opts.output, *target, format)
	if !opts.force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file already exists: %s (use --force to overwrite)", outputPath)
		}
	}

	meta, messages, err := collectConversation(target.Path)
	if err != nil {
		return err
	}
	messages = selectMessagesForExport(messages, opts.full)

	content, err := buildExportContent(*target, meta, messages, opts.full, format)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write export file %s: %w", outputPath, err)
	}

	fmt.Printf("exported %d messages to %s\n", len(messages), outputPath)
	return nil
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.sessionsDir, "sessions-dir", defaultSessionsDir, "Codex sessions directory")
	flag.BoolVar(&opts.showAll, "all", false, "show all sessions (disable current-cwd filter)")
	flag.StringVar(&opts.output, "output", "", "output file path")
	flag.StringVar(&opts.format, "format", string(formatHTML), "output format: html or markdown")
	flag.BoolVar(&opts.last, "last", false, "export latest session without picker")
	flag.StringVar(&opts.sessionID, "session-id", "", "export by session ID without picker")
	flag.BoolVar(&opts.force, "force", false, "overwrite existing output file")
	flag.BoolVar(&opts.full, "full", false, "export full conversation (include non-final assistant phases)")
	flag.Parse()
	return opts
}

func parseExportFormat(raw string) (exportFormat, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "html", "htm":
		return formatHTML, nil
	case "markdown", "md":
		return formatMarkdown, nil
	default:
		return "", fmt.Errorf("unsupported output format %q (use markdown or html)", raw)
	}
}

func listSessions(sessionsDir string, currentCwd string, showAll bool) ([]sessionSummary, error) {
	rolloutFiles, err := findRolloutFiles(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("scan sessions directory: %w", err)
	}
	threadTitles, err := loadThreadNameIndex(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("read session index: %w", err)
	}
	codexHome := filepath.Dir(sessionsDir)
	dbTitles, err := loadThreadTitlesFromStateDB(codexHome)
	if err == nil {
		for threadID, title := range dbTitles {
			if _, exists := threadTitles[threadID]; exists {
				continue
			}
			threadTitles[threadID] = title
		}
	}

	normalizedCurrent := normalizePath(currentCwd)
	summaries := make([]sessionSummary, 0, len(rolloutFiles))
	for _, rolloutPath := range rolloutFiles {
		summary, ok, err := readSummaryFromRollout(rolloutPath)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		if !showAll {
			if summary.Cwd == "" {
				continue
			}
			if normalizePath(summary.Cwd) != normalizedCurrent {
				continue
			}
		}
		if summary.SessionID != "" {
			summary.Title = threadTitles[summary.SessionID]
		}
		summaries = append(summaries, summary)
	}

	sort.SliceStable(summaries, func(i, j int) bool {
		left := summaries[i]
		right := summaries[j]
		if !left.CreatedAt.IsZero() && !right.CreatedAt.IsZero() {
			if !left.CreatedAt.Equal(right.CreatedAt) {
				return left.CreatedAt.After(right.CreatedAt)
			}
		}
		return left.UpdatedAt.After(right.UpdatedAt)
	})

	return summaries, nil
}

func findRolloutFiles(root string) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func readSummaryFromRollout(path string) (sessionSummary, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionSummary{}, false, err
	}
	defer file.Close()

	summary := sessionSummary{Path: path}
	fileInfo, statErr := file.Stat()
	if statErr == nil {
		summary.UpdatedAt = fileInfo.ModTime()
	}
	summary.CreatedAt = parseCreatedAtFromFilename(filepath.Base(path))

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), scannerBufferSize)

	metaFound := false

	for scanner.Scan() {
		line := rolloutLine{}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		switch line.Type {
		case "session_meta":
			if metaFound {
				continue
			}
			meta := sessionMetaPayload{}
			if err := json.Unmarshal(line.Payload, &meta); err != nil {
				continue
			}
			source := decodeSource(meta.SourceRaw)
			if !isInteractiveSource(source) {
				return sessionSummary{}, false, nil
			}
			summary.SessionID = meta.ID
			summary.Cwd = meta.Cwd
			summary.Source = source
			summary.ModelProvider = meta.ModelProvider
			metaFound = true
		}

		if metaFound {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return sessionSummary{}, false, err
	}
	if !metaFound {
		return sessionSummary{}, false, fmt.Errorf("missing session_meta in %s", path)
	}
	return summary, true, nil
}

func selectTargetSession(summaries []sessionSummary, opts options) (*sessionSummary, error) {
	if opts.sessionID != "" {
		for i := range summaries {
			if summaries[i].SessionID == opts.sessionID {
				return &summaries[i], nil
			}
		}
		return nil, fmt.Errorf("session not found: %s", opts.sessionID)
	}

	if opts.last {
		return &summaries[0], nil
	}

	index, cancelled, err := runSessionPicker(summaries, opts.showAll, time.Now(), tea.NewProgram)
	if err != nil {
		return nil, err
	}
	if cancelled {
		return nil, nil
	}
	return &summaries[index], nil
}

func collectConversation(path string) (sessionMetaPayload, []exportMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionMetaPayload{}, nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), scannerBufferSize)

	meta := sessionMetaPayload{}
	metaFound := false
	messages := make([]exportMessage, 0)

	for scanner.Scan() {
		line := rolloutLine{}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		switch line.Type {
		case "session_meta":
			if metaFound {
				continue
			}
			if err := json.Unmarshal(line.Payload, &meta); err == nil {
				metaFound = true
			}
		case "response_item":
			item := responseItem{}
			if err := json.Unmarshal(line.Payload, &item); err != nil {
				continue
			}
			if item.Type != "message" {
				continue
			}
			if item.Role != "user" && item.Role != "assistant" {
				continue
			}
			body := strings.TrimSpace(cleanExportBody(extractMessageBody(item.Content)))
			if body == "" {
				continue
			}
			messages = append(messages, exportMessage{Role: item.Role, Phase: item.Phase, Body: body})
		}
	}
	if err := scanner.Err(); err != nil {
		return sessionMetaPayload{}, nil, err
	}
	if !metaFound {
		return sessionMetaPayload{}, nil, errors.New("session metadata not found")
	}
	return meta, messages, nil
}

func selectMessagesForExport(messages []exportMessage, full bool) []exportMessage {
	if full {
		return messages
	}
	filtered := make([]exportMessage, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if isSystemInjectedUserMessage(msg.Body) {
				continue
			}
			filtered = append(filtered, msg)
		case "assistant":
			phase := strings.ToLower(strings.TrimSpace(msg.Phase))
			if phase == "final" || phase == "final_answer" {
				filtered = append(filtered, msg)
			}
		}
	}
	return filtered
}

func isSystemInjectedUserMessage(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	prefixes := []string{
		"# AGENTS.md instructions for ",
		"<environment_context>",
		"<subagent_notification>",
		"<turn_aborted>",
		"<permissions instructions>",
		"<collaboration_mode>",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func buildExportContent(summary sessionSummary, meta sessionMetaPayload, messages []exportMessage, includeMeta bool, format exportFormat) (string, error) {
	exporter := conversationExporter{
		summary:     summary,
		meta:        meta,
		messages:    messages,
		includeMeta: includeMeta,
	}
	switch format {
	case formatMarkdown:
		return exporter.Markdown(), nil
	case formatHTML:
		html, err := exporter.HTML()
		if err != nil {
			return "", fmt.Errorf("build html export: %w", err)
		}
		return html, nil
	default:
		return "", fmt.Errorf("unsupported output format %q", format)
	}
}

func buildMarkdown(summary sessionSummary, meta sessionMetaPayload, messages []exportMessage, includeMeta bool) string {
	return conversationExporter{
		summary:     summary,
		meta:        meta,
		messages:    messages,
		includeMeta: includeMeta,
	}.Markdown()
}

func buildHTML(summary sessionSummary, meta sessionMetaPayload, messages []exportMessage, includeMeta bool) (string, error) {
	return conversationExporter{
		summary:     summary,
		meta:        meta,
		messages:    messages,
		includeMeta: includeMeta,
	}.HTML()
}

func (e conversationExporter) Markdown() string {
	var sb strings.Builder
	mainTitle := "Codex Session Export"
	if !e.includeMeta {
		mainTitle = displayTitle(e.summary.Title)
	}
	sb.WriteString(fmt.Sprintf("# %s\n\n", mainTitle))
	sb.WriteString(markdownSessionStartMarker(e))
	sb.WriteString("\n\n")
	if e.includeMeta {
		sb.WriteString(fmt.Sprintf("- Title: `%s`\n", displayTitle(e.summary.Title)))
		sb.WriteString(fmt.Sprintf("- Session ID: `%s`\n", nonEmpty(e.meta.ID, e.summary.SessionID)))
		sb.WriteString(fmt.Sprintf("- Source: `%s`\n", nonEmpty(decodeSource(e.meta.SourceRaw), e.summary.Source)))
		sb.WriteString(fmt.Sprintf("- Model Provider: `%s`\n", nonEmpty(e.meta.ModelProvider, e.summary.ModelProvider)))
		sb.WriteString(fmt.Sprintf("- CWD: `%s`\n", nonEmpty(e.meta.Cwd, e.summary.Cwd)))
		sb.WriteString(fmt.Sprintf("- Created: `%s`\n", nonEmpty(e.meta.Timestamp, formatTime(e.summary.CreatedAt))))
		sb.WriteString(fmt.Sprintf("- Updated: `%s`\n", formatTime(e.summary.UpdatedAt)))
		sb.WriteString(fmt.Sprintf("- Rollout File: `%s`\n", e.summary.Path))
		sb.WriteString(fmt.Sprintf("- Exported At: `%s`\n", time.Now().Format(time.RFC3339)))
		sb.WriteString("\n")
	}
	conversationHeading := "## Conversation"
	if !e.includeMeta {
		conversationHeading = "## Transcript"
	}
	sb.WriteString(conversationHeading + "\n\n")

	if len(e.messages) == 0 {
		sb.WriteString("_No user/assistant message found in this session._\n")
		sb.WriteString("\n")
		sb.WriteString(markdownSessionEndMarker(e))
		sb.WriteString("\n")
		return sb.String()
	}

	for i, msg := range e.messages {
		index := i + 1
		sb.WriteString(markdownMessageStartMarker(index, msg))
		sb.WriteString("\n\n")
		sb.WriteString(markdownSpeakerLine(msg))
		sb.WriteString("\n\n")
		sb.WriteString(cleanExportBody(msg.Body))
		sb.WriteString("\n")
		sb.WriteString("\n")
		sb.WriteString(markdownMessageEndMarker(index))
		if i != len(e.messages)-1 {
			sb.WriteString("\n\n---\n\n")
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(markdownSessionEndMarker(e))
	sb.WriteString("\n")

	return sb.String()
}

func (e conversationExporter) HTML() (string, error) {
	messages := make([]htmlMessageView, 0, len(e.messages))
	for i, msg := range e.messages {
		body, err := renderMarkdownFragment(cleanExportBody(msg.Body))
		if err != nil {
			return "", fmt.Errorf("render message %d markdown: %w", i+1, err)
		}
		messages = append(messages, htmlMessageView{
			Index:     i + 1,
			Role:      msg.Role,
			RoleClass: sanitizeMarkerValue(msg.Role),
			RoleTitle: humanRole(msg.Role),
			Phase:     strings.TrimSpace(msg.Phase),
			Body:      body,
		})
	}

	heading := displayTitle(e.summary.Title)
	if e.includeMeta {
		heading = "Codex Session Export"
	}
	exportedAt := time.Now().Format(time.RFC3339)
	data := htmlPageData{
		Title:               displayTitle(e.summary.Title),
		Heading:             heading,
		IncludeMeta:         e.includeMeta,
		ConversationHeading: "Transcript",
		MetaRows:            e.htmlMetaRows(exportedAt),
		Messages:            messages,
		ExportedAt:          exportedAt,
	}
	if e.includeMeta {
		data.ConversationHeading = "Conversation"
	}

	var sb strings.Builder
	tmpl, err := template.New("session-html").Parse(sessionHTMLTemplate)
	if err != nil {
		return "", fmt.Errorf("parse html template: %w", err)
	}
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("execute html template: %w", err)
	}
	return sb.String(), nil
}

func (e conversationExporter) htmlMetaRows(exportedAt string) []htmlMetaRow {
	if !e.includeMeta {
		return nil
	}
	return []htmlMetaRow{
		{Label: "Title", Value: displayTitle(e.summary.Title)},
		{Label: "Session ID", Value: nonEmpty(e.meta.ID, e.summary.SessionID)},
		{Label: "Source", Value: nonEmpty(decodeSource(e.meta.SourceRaw), e.summary.Source)},
		{Label: "Model Provider", Value: nonEmpty(e.meta.ModelProvider, e.summary.ModelProvider)},
		{Label: "CWD", Value: nonEmpty(e.meta.Cwd, e.summary.Cwd)},
		{Label: "Created", Value: nonEmpty(e.meta.Timestamp, formatTime(e.summary.CreatedAt))},
		{Label: "Updated", Value: formatTime(e.summary.UpdatedAt)},
		{Label: "Rollout File", Value: e.summary.Path},
		{Label: "Exported At", Value: exportedAt},
	}
}

func determineOutputPath(output string, summary sessionSummary, format exportFormat) string {
	if output != "" {
		return output
	}
	sessionID := summary.SessionID
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(summary.Path), filepath.Ext(summary.Path))
	}
	return filepath.Join(".", fmt.Sprintf("codex-session-%s%s", sessionID, format.fileExtension()))
}

func (format exportFormat) fileExtension() string {
	switch format {
	case formatHTML:
		return ".html"
	default:
		return ".md"
	}
}

func cleanExportBody(body string) string {
	cleaned := memoryCitationBlockPattern.ReplaceAllString(body, "\n\n")
	cleaned = excessiveBlankLinesPattern.ReplaceAllString(cleaned, "\n\n")
	return strings.TrimSpace(cleaned)
}

func markdownSessionStartMarker(e conversationExporter) string {
	if !e.includeMeta {
		return "<!-- codex-exporter:session-start format=markdown -->"
	}
	return fmt.Sprintf(
		"<!-- codex-exporter:session-start id=%s format=markdown -->",
		sanitizeMarkerValue(nonEmpty(e.meta.ID, e.summary.SessionID)),
	)
}

func markdownSessionEndMarker(e conversationExporter) string {
	if !e.includeMeta {
		return "<!-- codex-exporter:session-end -->"
	}
	return fmt.Sprintf(
		"<!-- codex-exporter:session-end id=%s -->",
		sanitizeMarkerValue(nonEmpty(e.meta.ID, e.summary.SessionID)),
	)
}

func markdownMessageStartMarker(index int, msg exportMessage) string {
	parts := []string{
		"codex-exporter:message-start",
		fmt.Sprintf("index=%d", index),
		fmt.Sprintf("role=%s", sanitizeMarkerValue(msg.Role)),
	}
	if strings.TrimSpace(msg.Phase) != "" {
		parts = append(parts, fmt.Sprintf("phase=%s", sanitizeMarkerValue(msg.Phase)))
	}
	return fmt.Sprintf("<!-- %s -->", strings.Join(parts, " "))
}

func markdownMessageEndMarker(index int) string {
	return fmt.Sprintf("<!-- codex-exporter:message-end index=%d -->", index)
}

func markdownSpeakerLine(msg exportMessage) string {
	line := fmt.Sprintf("**Speaker:** %s", humanRole(msg.Role))
	if strings.TrimSpace(msg.Phase) != "" {
		line += fmt.Sprintf("  \n**Phase:** `%s`", strings.TrimSpace(msg.Phase))
	}
	return line
}

func sanitizeMarkerValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	var sb strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			sb.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r)
		case r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == ':':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	if sb.Len() == 0 {
		return "unknown"
	}
	return sb.String()
}

func renderMarkdownFragment(markdown string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(markdown), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func loadThreadNameIndex(sessionsDir string) (map[string]string, error) {
	indexPath := filepath.Join(filepath.Dir(sessionsDir), "session_index.jsonl")
	if _, err := os.Stat(indexPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	file, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	names := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), scannerBufferSize)
	for scanner.Scan() {
		var entry sessionIndexEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		trimmed := strings.TrimSpace(entry.ThreadName)
		if trimmed == "" {
			continue
		}
		// session_index is append-only; later entry wins.
		names[entry.ID] = trimmed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func loadThreadTitlesFromStateDB(codexHome string) (map[string]string, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return map[string]string{}, nil
	}
	dbPath, ok := findStateDBPath(codexHome)
	if !ok {
		return map[string]string{}, nil
	}

	cmd := exec.Command(
		"sqlite3",
		"-noheader",
		"-separator",
		"\t",
		dbPath,
		"SELECT id, title FROM threads WHERE title <> '';",
	)
	output, err := cmd.Output()
	if err != nil {
		return map[string]string{}, nil
	}
	return parseThreadTitleLines(string(output)), nil
}

func parseThreadTitleLines(raw string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		title := strings.TrimSpace(parts[1])
		if id == "" || title == "" {
			continue
		}
		result[id] = title
	}
	return result
}

func findStateDBPath(codexHome string) (string, bool) {
	entries, err := os.ReadDir(codexHome)
	if err != nil {
		return "", false
	}
	bestNumber := -1
	bestPath := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := stateDBPattern.FindStringSubmatch(entry.Name())
		if len(matches) != 2 {
			continue
		}
		n, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		if n <= bestNumber {
			continue
		}
		bestNumber = n
		bestPath = filepath.Join(codexHome, entry.Name())
	}
	if bestPath == "" {
		return "", false
	}
	return bestPath, true
}

func extractMessageBody(content []contentItem) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch item.Type {
		case "input_text", "output_text":
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case "input_image":
			if item.ImageURL != "" {
				parts = append(parts, fmt.Sprintf("![input image](%s)", item.ImageURL))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func parseCreatedAtFromFilename(name string) time.Time {
	matches := rolloutNamePattern.FindStringSubmatch(name)
	if len(matches) != 2 {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02T15-04-05", matches[1], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func decodeSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var sourceString string
	if err := json.Unmarshal(raw, &sourceString); err == nil {
		return sourceString
	}
	var sourceMap map[string]any
	if err := json.Unmarshal(raw, &sourceMap); err == nil {
		if _, ok := sourceMap["sub_agent"]; ok {
			return "sub_agent"
		}
		if _, ok := sourceMap["subagent"]; ok {
			return "subagent"
		}
	}
	return ""
}

func isInteractiveSource(source string) bool {
	return source == "cli" || source == "vscode"
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

func truncateString(input string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	if limit <= 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func displayTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "(untitled)"
	}
	return trimmed
}

func relativeTime(target time.Time, now time.Time) string {
	if target.IsZero() {
		return "-"
	}
	if now.IsZero() {
		now = time.Now()
	}
	diff := now.Sub(target)
	future := diff < 0
	if future {
		diff = -diff
	}

	var value int64
	var unit string
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		value = int64(diff / time.Minute)
		unit = "m"
	case diff < 24*time.Hour:
		value = int64(diff / time.Hour)
		unit = "h"
	case diff < 7*24*time.Hour:
		value = int64(diff / (24 * time.Hour))
		unit = "d"
	case diff < 30*24*time.Hour:
		value = int64(diff / (7 * 24 * time.Hour))
		unit = "w"
	case diff < 365*24*time.Hour:
		value = int64(diff / (30 * 24 * time.Hour))
		unit = "mo"
	default:
		value = int64(diff / (365 * 24 * time.Hour))
		unit = "y"
	}
	if future {
		return fmt.Sprintf("in %d%s", value, unit)
	}
	return fmt.Sprintf("%d%s ago", value, unit)
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	resolved := path
	if abs, err := filepath.Abs(path); err == nil {
		resolved = abs
	}
	if symlinkResolved, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = symlinkResolved
	}
	return filepath.Clean(resolved)
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func nonEmpty(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "-"
}

func humanRole(role string) string {
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	default:
		if role == "" {
			return "Unknown"
		}
		runes := []rune(role)
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		return string(runes)
	}
}
