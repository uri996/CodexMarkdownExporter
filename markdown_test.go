package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestReadableMarkdown(t *testing.T) {
	messages := []exportMessage{
		{Role: "user", Body: "first prompt"},
		{Role: "assistant", Phase: "final", Body: "first reply"},
		{Role: "user", Body: "second prompt"},
		{Role: "assistant", Phase: "final_answer", Body: "COMPLETE"},
	}
	got := buildMarkdown(sessionSummary{Title: "中文 Session"}, messages)
	want := "# 中文 Session\n\n# 🧑 User\n\nfirst prompt\n\n---\n\n# 💻 Assistant\n\nfirst reply\n\n---\n\n# 🧑 User\n\nsecond prompt\n\n---\n\n# 💻 Assistant\n\nCOMPLETE\n\n---\n"
	if got != want {
		t.Fatalf("unexpected Markdown: %q", got)
	}
	for _, excluded := range []string{"<!--", "Speaker:", "Phase:", "Transcript", "index=", "role=", "phase=", "final_answer"} {
		if strings.Contains(got, excluded) {
			t.Fatalf("unexpected metadata %q", excluded)
		}
	}
}

func TestMarkdownHeadingIconsPreserveMessageBody(t *testing.T) {
	body := "Literal emoji and headings: 👤 🧑 💻\n\n# 👤 User\n\n```markdown\n# 👤 User\n```"
	messages := []exportMessage{
		{Role: "user", Body: body},
		{Role: "assistant", Phase: "final", Body: body},
	}
	want := "# Synthetic\n\n# 🧑 User\n\n" + body + "\n\n---\n\n# 💻 Assistant\n\n" + body + "\n\n---\n"
	if got := buildMarkdown(sessionSummary{Title: "Synthetic"}, messages); got != want {
		t.Fatal("heading icons or literal body emoji changed unexpectedly")
	}
}

func TestUserContextContentPartProvenance(t *testing.T) {
	plugin := "<recommended_plugins>\nHere is a list of plugins that are available but not installed.\nsynthetic\n</recommended_plugins>"
	literal := "<example>用户 XML</example>\n\n<!-- user comment -->\n\n```xml\n<recommended_plugins>literal code</recommended_plugins>\n```\n\n\nkeep blank lines"
	for _, tc := range []struct {
		name  string
		parts []string
		kinds []string
		want  string
	}{
		{"mixed parts", []string{plugin, literal, "injected environment", "second input"}, []string{"plugins.recommendations", "user.text", "environments.environment_context", "user.text"}, literal + "\n\nsecond input"},
		{"explicit user plugin XML", []string{plugin}, []string{"user.text"}, plugin},
		{"explicit user agents text", []string{"# AGENTS.md instructions for literal example"}, []string{"user.text"}, "# AGENTS.md instructions for literal example"},
		{"ordinary XML without metadata", []string{literal}, nil, literal},
		{"old plugin wrapper", []string{plugin, literal}, nil, literal},
		{"old partial wrapper", []string{"<recommended_plugins>literal"}, nil, "<recommended_plugins>literal"},
		{"plain instructions heading", []string{"# AGENTS.md instructions for a user example"}, nil, "# AGENTS.md instructions for a user example"},
		{"unknown kind", []string{plugin}, []string{"future.unknown"}, plugin},
		{"misaligned metadata", []string{plugin, literal}, []string{"plugins.recommendations"}, plugin + "\n\n" + literal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := responseItem{Type: "message", Role: "user", Metadata: messageMetadata{ContentItemKinds: tc.kinds}}
			for _, text := range tc.parts {
				item.Content = append(item.Content, contentItem{Type: "input_text", Text: text})
			}
			if got := extractUserPrompt(item); got != tc.want {
				t.Fatalf("prompt changed: got %q, want %q", got, tc.want)
			}
		})
	}
	for _, kind := range []string{"plugins.recommendations", "agents_md.instructions", "environments.environment_context", "additional_content.codex_apps_open_page", "user.heartbeat", "skills.selected_skill_instructions"} {
		item := responseItem{Content: []contentItem{{Type: "input_text", Text: "synthetic injected content"}}, Metadata: messageMetadata{ContentItemKinds: []string{kind}}}
		if got := extractUserPrompt(item); got != "" {
			t.Fatalf("known injected kind %s survived", kind)
		}
	}
}

func TestDefaultFilenameDirectoryBudget(t *testing.T) {
	root := t.TempDir()
	for _, directoryUnits := range []int{175, 220} {
		componentLength := directoryUnits - filenameUnits(root) - 1
		if componentLength < 1 {
			t.Skip("temporary directory already exceeds the test budget")
		}
		dir := filepath.Join(root, strings.Repeat("d", componentLength))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		path, err := determineOutputPath("", sessionSummary{Title: strings.Repeat("中文", 100)})
		if directoryUnits == 220 {
			if err == nil {
				t.Fatal("overlong directory should require explicit shorter output path")
			}
			if got, err := determineOutputPath(filepath.Join(root, "chosen.md"), sessionSummary{}); err != nil || got != filepath.Join(root, "chosen.md") {
				t.Fatal("explicit path incorrectly limited")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		absolute, err := filepath.Abs(path)
		if err != nil || filenameUnits(absolute) > maxOutputPathUnits || !utf8.ValidString(path) {
			t.Fatalf("unsafe default path %q, %v", absolute, err)
		}
	}
}

func TestContextFilteringBeforeAttachmentExpansion(t *testing.T) {
	root, path := attachmentFixture(t)
	literal := "<recommended_plugins>\nHere is a list of plugins that are available but not installed.\nThis is literal attachment content.\n</recommended_plugins>\n\n<!-- user comment -->\n\n\n<example>保留</example>"
	if err := os.WriteFile(path, []byte(literal), 0o600); err != nil {
		t.Fatal(err)
	}
	items := []responseItem{
		{Type: "message", Role: "user", Content: []contentItem{{Type: "input_text", Text: "injected"}, {Type: "input_text", Text: pastedWrapper(path)}}, Metadata: messageMetadata{ContentItemKinds: []string{"plugins.recommendations", "user.text"}}},
		{Type: "message", Role: "assistant", Phase: "commentary", Content: []contentItem{{Type: "output_text", Text: "progress"}}},
		{Type: "message", Role: "assistant", Phase: "final_answer", Content: []contentItem{{Type: "output_text", Text: "COMPLETE\n\n```xml\n<example>answer</example>\n```\n\n\n<!-- answer comment -->"}}},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	if err := enc.Encode(map[string]any{"type": "session_meta", "payload": sessionMetaPayload{ID: "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := enc.Encode(map[string]any{"type": "response_item", "payload": item}); err != nil {
			t.Fatal(err)
		}
	}
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(rollout, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_, messages, err := collectConversation(rollout)
	if err != nil {
		t.Fatal(err)
	}
	messages = normalizePastedTextMessages(selectMessagesForExport(messages, false), root)
	if len(messages) != 2 || !strings.Contains(messages[0].Body, literal) || messages[1].Body != items[2].Content[0].Text {
		t.Fatalf("body changed or context leaked: %+v", messages)
	}
}

func TestHumanReadableOutputNames(t *testing.T) {
	t.Chdir(t.TempDir())
	created := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ title, want string }{
		{"新增模板专属与全部 CSS 复制", "20261013_新增模板专属与全部-CSS-复制.md"},
		{"  A<>:\"/\\|?* B...  ", "20261013_A----------B.md"},
		{"", "20261013_codex-session_12345678.md"},
		{"<>:\"/\\|?*...", "20261013_codex-session_12345678.md"},
		{"CON", "20261013_CON.md"},
		{"alpha  \t beta_2-3", "20261013_alpha-beta_2-3.md"},
	} {
		got, err := determineOutputPath("", sessionSummary{Title: tc.title, CreatedAt: created, SessionID: "12345678-abcd-1234-abcd-123456789012"})
		if err != nil || got != tc.want {
			t.Fatalf("title %q: got %q, %v; want %q", tc.title, got, err, tc.want)
		}
	}
	got, err := determineOutputPath("", sessionSummary{Title: strings.Repeat("中文🌏", 120), CreatedAt: created})
	if err != nil || !utf8.ValidString(got) || filenameUnits(strings.TrimSuffix(strings.TrimPrefix(got, "20261013_"), ".md")) > 100 {
		t.Fatalf("unsafe long filename %q: %v", got, err)
	}
	abs, err := filepath.Abs(got)
	if err != nil || filenameUnits(abs) > maxOutputPathUnits {
		t.Fatalf("path too long: %q", abs)
	}
	custom := filepath.Join("chosen", "custom.name.md")
	if got, err := determineOutputPath(custom, sessionSummary{}); err != nil || got != custom {
		t.Fatalf("custom path changed: %q, %v", got, err)
	}
	if got, err := determineOutputPath("", sessionSummary{Title: "filename-date", Path: "rollout-2026-01-02T03-04-05-synthetic.jsonl"}); err != nil || got != "20260102_filename-date.md" {
		t.Fatalf("creation date fallback: %q, %v", got, err)
	}
}

func TestOutputCollisionsAndExplicitOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	summary := sessionSummary{Title: "同名", CreatedAt: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	for i, name := range []string{"20261013_同名.md", "20261013_同名_2.md", "20261013_同名_3.md"} {
		got, err := writeMarkdownExport("", summary, "original", i == 2)
		if err != nil || got != name {
			t.Fatalf("collision filename: %q, %v", got, err)
		}
	}
	custom := "my-output.md"
	if _, err := writeMarkdownExport(custom, summary, "first", false); err != nil {
		t.Fatal(err)
	}
	if _, err := writeMarkdownExport(custom, summary, "second", false); err == nil {
		t.Fatal("explicit file overwritten without -force")
	}
	if data, err := os.ReadFile(custom); err != nil || string(data) != "first" {
		t.Fatal("original data changed")
	}
	if _, err := writeMarkdownExport(custom, summary, "second", true); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(custom); err != nil || string(data) != "second" {
		t.Fatal("explicit -force failed")
	}
}

func TestMarkdownCLIAndREADME(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	flag.CommandLine = flag.NewFlagSet("codex-exporter", flag.ContinueOnError)
	var help bytes.Buffer
	flag.CommandLine.SetOutput(&help)
	os.Args = []string{"codex-exporter", "-help"}
	parseFlags()
	if flag.Lookup("format") != nil || strings.Contains(strings.ToLower(help.String()), "html") {
		t.Fatal("obsolete format option remains")
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(readme)), "html") || strings.Contains(string(readme), "-format") {
		t.Fatal("obsolete output documentation remains")
	}
	flag.VisitAll(func(f *flag.Flag) {
		if !strings.Contains(help.String(), "-"+f.Name) || !strings.Contains(string(readme), "`-"+f.Name+"`") {
			t.Errorf("undocumented flag %s", f.Name)
		}
	})
}
