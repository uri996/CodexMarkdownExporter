package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
)

const (
	defaultSessionsDir = "~/.codex/sessions"
	scannerBufferSize  = 16 * 1024 * 1024
)

var rolloutNamePattern = regexp.MustCompile(`^rollout-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-.+\.jsonl$`)
var stateDBPattern = regexp.MustCompile(`^state_(\d+)\.sqlite$`)

type options struct {
	sessionsDir string
	showAll     bool
	output      string
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts := parseFlags()
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

	outputPath := determineOutputPath(opts.output, *target)
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

	markdown := buildMarkdown(*target, meta, messages, opts.full)
	if err := os.WriteFile(outputPath, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write markdown file %s: %w", outputPath, err)
	}

	fmt.Printf("exported %d messages to %s\n", len(messages), outputPath)
	return nil
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.sessionsDir, "sessions-dir", defaultSessionsDir, "Codex sessions directory")
	flag.BoolVar(&opts.showAll, "all", false, "show all sessions (disable current-cwd filter)")
	flag.StringVar(&opts.output, "output", "", "output markdown file path")
	flag.BoolVar(&opts.last, "last", false, "export latest session without picker")
	flag.StringVar(&opts.sessionID, "session-id", "", "export by session ID without picker")
	flag.BoolVar(&opts.force, "force", false, "overwrite existing output file")
	flag.BoolVar(&opts.full, "full", false, "export full conversation (include non-final assistant phases)")
	flag.Parse()
	return opts
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
			body := strings.TrimSpace(extractMessageBody(item.Content))
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

func buildMarkdown(summary sessionSummary, meta sessionMetaPayload, messages []exportMessage, includeMeta bool) string {
	var sb strings.Builder
	mainTitle := "Codex Session Export"
	if !includeMeta {
		mainTitle = displayTitle(summary.Title)
	}
	sb.WriteString(fmt.Sprintf("# %s\n\n", mainTitle))
	if includeMeta {
		sb.WriteString(fmt.Sprintf("- Title: `%s`\n", displayTitle(summary.Title)))
		sb.WriteString(fmt.Sprintf("- Session ID: `%s`\n", nonEmpty(meta.ID, summary.SessionID)))
		sb.WriteString(fmt.Sprintf("- Source: `%s`\n", nonEmpty(decodeSource(meta.SourceRaw), summary.Source)))
		sb.WriteString(fmt.Sprintf("- Model Provider: `%s`\n", nonEmpty(meta.ModelProvider, summary.ModelProvider)))
		sb.WriteString(fmt.Sprintf("- CWD: `%s`\n", nonEmpty(meta.Cwd, summary.Cwd)))
		sb.WriteString(fmt.Sprintf("- Created: `%s`\n", nonEmpty(meta.Timestamp, formatTime(summary.CreatedAt))))
		sb.WriteString(fmt.Sprintf("- Updated: `%s`\n", formatTime(summary.UpdatedAt)))
		sb.WriteString(fmt.Sprintf("- Rollout File: `%s`\n", summary.Path))
		sb.WriteString(fmt.Sprintf("- Exported At: `%s`\n", time.Now().Format(time.RFC3339)))
		sb.WriteString("\n")
	}
	conversationHeading := "## Conversation"
	if !includeMeta {
		conversationHeading = "## Transcript"
	}
	sb.WriteString(conversationHeading + "\n\n")

	if len(messages) == 0 {
		sb.WriteString("_No user/assistant message found in this session._\n")
		return sb.String()
	}

	for i, msg := range messages {
		roleTitle := humanRole(msg.Role)
		if msg.Phase != "" {
			roleTitle = fmt.Sprintf("%s (%s)", roleTitle, msg.Phase)
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n", roleTitle))
		sb.WriteString(msg.Body)
		sb.WriteString("\n")
		if i != len(messages)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func determineOutputPath(output string, summary sessionSummary) string {
	if output != "" {
		return output
	}
	sessionID := summary.SessionID
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(summary.Path), filepath.Ext(summary.Path))
	}
	return filepath.Join(".", fmt.Sprintf("codex-session-%s.md", sessionID))
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
