package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	defaultSessionsDir = "~/.codex/sessions"
	scannerBufferSize  = 64 * 1024 * 1024
)

type options struct {
	sessionsDir  string
	showAll      bool
	output       string
	last         bool
	sessionID    string
	force        bool
	full         bool
	batch        bool
	project      string
	outputDir    string
	setOutputDir string
	single       bool
	version      bool
	report       *portableReport
	explicit     map[string]bool
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
	Type     string          `json:"type"`
	Role     string          `json:"role,omitempty"`
	Phase    string          `json:"phase,omitempty"`
	Content  []contentItem   `json:"content,omitempty"`
	Metadata messageMetadata `json:"internal_chat_message_metadata_passthrough,omitempty"`
}

type messageMetadata struct {
	ContentItemKinds []string `json:"content_item_kinds,omitempty"`
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
	Role      string
	Phase     string
	Body      string
	turnID    string
	userInput bool // content parts have already been classified before attachment expansion
}

type eventPayload struct {
	Type     string `json:"type"`
	TurnID   string `json:"turn_id"`
	NumTurns int    `json:"num_turns"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts := parseFlags()
	if err := validateOptions(opts); err != nil {
		return err
	}
	if opts.version {
		fmt.Printf("Codex Exporter %s\n", version)
		return nil
	}
	if len(os.Args) == 1 {
		if !consoleInteractive() {
			return errors.New("无参数启动需要交互式终端；使用 -help 查看命令行用法")
		}
		return runPortableMenu()
	}
	if opts.setOutputDir != "" {
		if err := saveOutputRoot(opts.setOutputDir); err != nil {
			return err
		}
		fmt.Println("Default output directory saved.")
		return nil
	}
	if opts.output == "" && opts.outputDir == "" {
		root, err := chooseOutputRoot(os.Stdin, os.Stdout, consoleInteractive())
		if err != nil {
			return err
		}
		opts.outputDir = root
	}
	if opts.batch {
		return runBatchExport(opts)
	}
	return runSingleExport(opts)
}

func runSingleExport(opts options) error {
	sessionsDir := expandHome(opts.sessionsDir)
	currentCwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current working directory: %w", err)
	}

	summaries, err := listSessions(sessionsDir, currentCwd, opts.showAll)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Session discovery is incomplete: %s\n", safeFailure(err))
		if opts.report != nil {
			opts.report.DiscoveryErrors = append(opts.report.DiscoveryErrors, "扫描不完整："+safeFailure(err))
		}
		if len(summaries) == 0 {
			return err
		}
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

	content, count, err := renderSession(*target, opts.full)
	if err != nil {
		return err
	}
	outputPath, err := writeMarkdownExportInDir(opts.output, opts.outputDir, *target, content, opts.force)
	if err != nil {
		return err
	}

	fmt.Printf("exported %d messages to %s\n", count, outputPath)
	if opts.report != nil {
		opts.report.File = filepath.Base(outputPath)
		opts.report.Directory, _ = filepath.Abs(filepath.Dir(outputPath))
		opts.report.Messages = count
	}
	return nil
}

func parseFlags() options {
	var opts options
	flag.CommandLine.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Codex Exporter "+version+"\n无参数：中文主菜单（需要交互终端）\n-single：单独导出 | -batch：项目批量导出\n-set-output-dir <目录>：保存默认目录 | -output-dir <目录>：仅本次使用")
		flag.PrintDefaults()
	}
	flag.StringVar(&opts.sessionsDir, "sessions-dir", sessionStorageDirectory(), "Codex sessions directory")
	flag.BoolVar(&opts.single, "single", false, "open Session Picker across all interactive sessions")
	flag.BoolVar(&opts.version, "version", false, "show program version and exit")
	flag.BoolVar(&opts.showAll, "all", false, "show all sessions (disable current-cwd filter)")
	flag.StringVar(&opts.output, "output", "", "Markdown output file path")
	flag.BoolVar(&opts.last, "last", false, "export latest session without picker")
	flag.StringVar(&opts.sessionID, "session-id", "", "export by session ID without picker")
	flag.BoolVar(&opts.force, "force", false, "overwrite an explicitly specified output file")
	flag.BoolVar(&opts.full, "full", false, "export full conversation (include non-final assistant phases)")
	flag.BoolVar(&opts.batch, "batch", false, "export all interactive sessions belonging to one project")
	flag.StringVar(&opts.project, "project", "", "project path (batch mode only; otherwise open project picker)")
	flag.StringVar(&opts.outputDir, "output-dir", "", "export root directory for this run (does not change saved default)")
	flag.StringVar(&opts.setOutputDir, "set-output-dir", "", "save or change the default export directory, then exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments; use -help")
		os.Exit(2)
	}
	opts.explicit = make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { opts.explicit[f.Name] = true })
	if opts.single {
		opts.showAll = true
	}
	return opts
}

func validateOptions(opts options) error {
	provided := func(name string, value bool) bool { return opts.explicit[name] || value }
	if provided("version", opts.version) {
		if len(opts.explicit) > 1 {
			return errors.New("-version cannot be combined with export options")
		}
		return nil
	}
	if provided("single", opts.single) && (opts.batch || provided("last", opts.last) || provided("session-id", opts.sessionID != "")) {
		return errors.New("-single opens the Session Picker; cannot combine with -batch, -last or -session-id")
	}
	if provided("set-output-dir", opts.setOutputDir != "") {
		if strings.TrimSpace(opts.setOutputDir) == "" {
			return errors.New("-set-output-dir must not be empty")
		}
		if len(opts.explicit) > 1 || opts.single || opts.batch || opts.output != "" || opts.outputDir != "" || opts.project != "" || opts.last || opts.sessionID != "" || opts.showAll || opts.force || opts.full {
			return errors.New("-set-output-dir is a standalone settings command; do not combine it with export options")
		}
		return nil
	}
	if !opts.batch && provided("project", opts.project != "") {
		return errors.New("-project requires -batch")
	}
	if provided("output", opts.output != "") && provided("output-dir", opts.outputDir != "") {
		return errors.New("-output and -output-dir cannot be combined")
	}
	if opts.explicit["output-dir"] && strings.TrimSpace(opts.outputDir) == "" {
		return errors.New("-output-dir must not be empty")
	}
	if opts.batch {
		for _, field := range []struct {
			name string
			used bool
		}{
			{"output", opts.output != ""}, {"last", opts.last}, {"session-id", opts.sessionID != ""},
			{"all", opts.showAll}, {"full", opts.full}, {"force", opts.force},
		} {
			if provided(field.name, field.used) {
				return fmt.Errorf("-%s is a single-export option and cannot be used with -batch", field.name)
			}
		}
		if opts.explicit["project"] && strings.TrimSpace(opts.project) == "" {
			return errors.New("-project must not be empty")
		}
	}
	return nil
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
	resolved = filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
		resolved = strings.ToLower(resolved)
	}
	return resolved
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
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
