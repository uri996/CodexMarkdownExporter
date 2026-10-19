package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const syntheticEnvironment = "<environment_context>\n<current_date>2026-10-19</current_date>\n<timezone>Australia/Sydney</timezone>\n<filesystem><workspace_roots><root>synthetic</root></workspace_roots><permission_profile type=\"managed\"><file_system type=\"restricted\"/></permission_profile></filesystem>\n</environment_context>"

func TestChosenDirectorySettings(t *testing.T) {
	home := isolatedHome(t)
	if _, err := resolveOutputRoot(""); !errors.Is(err, errNoOutputDirectory) {
		t.Fatalf("unexpected implicit default: %v", err)
	}
	if _, err := chooseOutputRoot(strings.NewReader(""), new(bytes.Buffer), false); !errors.Is(err, errNoOutputDirectory) {
		t.Fatal("noninteractive process chose a directory")
	}
	if _, err := chooseOutputRoot(strings.NewReader(""), new(bytes.Buffer), true); !errors.Is(err, errNoOutputDirectory) {
		t.Fatal("unusable terminal did not explain how to configure output")
	}
	if _, err := os.Stat(filepath.Join(home, "Codex-Exports")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy root was created")
	}
	one := filepath.Join(home, "chosen 中文", "one")
	if err := saveOutputRoot(one); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveOutputRoot(""); err != nil || got != one {
		t.Fatalf("settings did not persist: %q %v", got, err)
	}
	two := filepath.Join(home, "other selected directory")
	if err := saveOutputRoot(two); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveOutputRoot(""); err != nil || got != two {
		t.Fatal("settings were not changed")
	}
	temporary := filepath.Join(home, "temporary")
	if got, err := resolveOutputRoot(temporary); err != nil || got != temporary {
		t.Fatal("temporary override failed")
	}
	if got, err := savedOutputRoot(); err != nil || got != two {
		t.Fatal("temporary override changed saved settings")
	}
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := json.Unmarshal(data, &value); err != nil || len(value) != 1 || value["output_directory"] != two {
		t.Fatal("settings contain unrelated data")
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveOutputRoot(""); err == nil {
		t.Fatal("corrupt settings silently fell back")
	}
	if _, err := resolveOutputRoot(temporary); err != nil {
		t.Fatal("explicit directory depends on settings")
	}
	if got, err := determineOutputPathInDir(filepath.Join(home, "exact.md"), "", sessionSummary{}); err != nil || got != filepath.Join(home, "exact.md") {
		t.Fatal("explicit output depends on settings")
	}
	if err := saveOutputRoot(one); err != nil {
		t.Fatal("settings repair failed:", err)
	}
	blocked := filepath.Join(home, "not-directory")
	if err := os.WriteFile(blocked, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveOutputRoot(filepath.Join(blocked, "nested")); err == nil {
		t.Fatal("inaccessible destination accepted")
	}
	if got, _ := savedOutputRoot(); got != one {
		t.Fatal("failed setting overwrote previous choice")
	}
}

func TestInteractiveDirectoryConsent(t *testing.T) {
	home := isolatedHome(t)
	chosen := filepath.Join(home, "chosen")
	var prompts bytes.Buffer
	got, err := chooseOutputRoot(strings.NewReader(chosen+"\nn\n"), &prompts, true)
	if err != nil || got != chosen || !strings.Contains(prompts.String(), "Save this directory") {
		t.Fatalf("interactive choice failed: %q %v", got, err)
	}
	if _, err := savedOutputRoot(); !errors.Is(err, errNoOutputDirectory) {
		t.Fatal("declined persistence was ignored")
	}
	got, err = chooseOutputRoot(strings.NewReader(chosen+"\ny\n"), new(bytes.Buffer), true)
	if err != nil || got != chosen {
		t.Fatal(err)
	}
	if saved, err := savedOutputRoot(); err != nil || saved != chosen {
		t.Fatal("confirmed default not saved")
	}
	if got, err := chooseOutputRoot(strings.NewReader(""), new(bytes.Buffer), false); err != nil || got != chosen {
		t.Fatal("saved default unavailable without a terminal")
	}
}

func TestSharedChosenDefaultDirectory(t *testing.T) {
	home := isolatedHome(t)
	storage, project := projectFixture(t)
	chosen := filepath.Join(home, "explicitly chosen")
	if err := saveOutputRoot(chosen); err != nil {
		t.Fatal(err)
	}
	if err := runSingleExport(options{sessionsDir: storage, showAll: true, last: true}); err != nil {
		t.Fatal(err)
	}
	result := exportProjectSessions("", project)
	if result.failed() || result.Success != 2 || filepath.Dir(result.Directory) != chosen {
		t.Fatalf("batch did not share default: %+v", result)
	}
	if files, _ := filepath.Glob(filepath.Join(chosen, "*.md")); len(files) != 1 {
		t.Fatal("single did not use chosen default")
	}
}

func TestHistoricalPastedRequestBoundary(t *testing.T) {
	root, _ := attachmentFixture(t)
	content := strings.Repeat("中文长 Prompt\n", 1200) + "\n# 真正标题\n\n```xml\n<example>保留</example>\n```"
	path := writeAttachment(t, root, testAttachmentID, "已粘贴的文本.txt", content)
	second := writeAttachment(t, root, secondAttachmentID, "pasted-text.txt", "第二段")
	for _, newline := range []string{"\n", "\r\n"} {
		body := "前置正文\n\n# Files pasted by the user:\n\n## \"synthetic preview\": " + root + "/" + testAttachmentID + "/已粘贴的文本.txt\n\n## \"second preview\":\n" + second + "\n\n## My request:\n额外要求"
		body = strings.ReplaceAll(body, "\n", newline)
		got := resolvePastedTextAttachments(body, root)
		if !strings.Contains(got, content+"\n\n第二段") || !strings.HasSuffix(got, "额外要求") || strings.Contains(got, "Files pasted") || strings.Contains(got, "My request:") {
			t.Fatalf("legacy wrapper not expanded: %q", got[:min(150, len(got))])
		}
	}
	fenced := "```markdown\n" + pastedWrapper(path) + "\n```"
	if got := resolvePastedTextAttachments(fenced, root); got != fenced {
		t.Fatal("wrapper example inside code was interpreted")
	}
	missing := filepath.Join(root, "33333333-3333-3333-3333-333333333333", "pasted-text.txt")
	legacy := "# Files pasted by the user:\n\n\"preview\": " + missing + "\n\n## My request:\nkeep me"
	got := resolvePastedTextAttachments(legacy, root)
	if !strings.Contains(got, "不存在") || !strings.HasSuffix(got, "keep me") || strings.Contains(got, missing) {
		t.Fatal("missing legacy attachment lost request or leaked path")
	}
}

func syntheticIDE(selection bool) string {
	text := "# Context from my IDE setup:\n\n"
	if selection {
		text += "## Active file: synthetic/中文 file.go\n\n## Active selection of the file:\nfunc synthetic() {}\n\n"
	}
	return text + "## Open tabs:\n- synthetic.go: synthetic/文件.go\n- other.md: synthetic/other.md\n\n## My request for Codex:\n"
}

func TestHistoricalContextBoundaries(t *testing.T) {
	literal := "<example>real XML</example>\n\n<!-- user comment -->\n\nActive file is a phrase in my prompt."
	for _, tc := range []struct{ name, body, want string }{
		{"pure environment", syntheticEnvironment, ""},
		{"mixed environment", "before\n" + syntheticEnvironment + "\nafter", "before\n\nafter"},
		{"IDE selection", syntheticIDE(true) + literal, literal},
		{"IDE tabs only", syntheticIDE(false) + literal, literal},
		{"IDE surrounding prose", "before\n" + syntheticIDE(true) + literal + "\nafter", "before\n" + literal + "\nafter"},
		{"repeated IDE", syntheticIDE(false) + "first\n" + syntheticIDE(true) + "second", "first\nsecond"},
		{"ordinary XML", literal, literal},
		{"unknown environment XML", "<environment_context><example>real XML</example></environment_context>", "<environment_context><example>real XML</example></environment_context>"},
		{"indented environment example", "    " + strings.ReplaceAll(syntheticEnvironment, "\n", "\n    "), "    " + strings.ReplaceAll(syntheticEnvironment, "\n", "\n    ")},
		{"code fence", "```xml\n" + syntheticEnvironment + "\n```", "```xml\n" + syntheticEnvironment + "\n```"},
		{"IDE example in fence", "~~~markdown\n" + syntheticIDE(true) + literal + "\n~~~", "~~~markdown\n" + syntheticIDE(true) + literal + "\n~~~"},
		{"no request boundary", "# Context from my IDE setup:\n\n## Open tabs:\n- example", "# Context from my IDE setup:\n\n## Open tabs:\n- example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := responseItem{Content: []contentItem{{Type: "input_text", Text: tc.body}}}
			if got := strings.TrimSpace(extractUserPrompt(item)); got != strings.TrimSpace(tc.want) {
				t.Fatalf("context boundary wrong: got %q want %q", got, tc.want)
			}
		})
	}
	for _, body := range []string{syntheticEnvironment, syntheticIDE(true) + literal} {
		item := responseItem{Content: []contentItem{{Type: "input_text", Text: body}}, Metadata: messageMetadata{ContentItemKinds: []string{"user.text"}}}
		if got := extractUserPrompt(item); got != body {
			t.Fatal("explicit author provenance was ignored")
		}
	}
	crlf := strings.ReplaceAll(syntheticIDE(true), "\n", "\r\n") + literal
	if got := extractUserPrompt(responseItem{Content: []contentItem{{Type: "input_text", Text: crlf}}}); got != literal {
		t.Fatal("CRLF context not cleaned")
	}
}

func TestSharedHistoricalCleanupPipeline(t *testing.T) {
	root, _ := attachmentFixture(t)
	_, project := projectFixture(t)
	s := project.Sessions[0]
	path := writeAttachment(t, root, testAttachmentID, "pasted-text.txt", "# 用户正文\n\n"+syntheticEnvironment+"\n\n<!-- literal attachment content -->")
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	records := []struct {
		kind    string
		payload any
	}{
		{"session_meta", map[string]any{"id": s.SessionID, "cwd": project.Root, "source": "vscode"}},
		{"response_item", responseItem{Type: "message", Role: "user", Content: []contentItem{{Type: "input_text", Text: syntheticEnvironment}}}},
		{"response_item", responseItem{Type: "message", Role: "user", Content: []contentItem{{Type: "input_text", Text: syntheticIDE(true) + "# Files pasted by the user:\n\n\"preview\": " + path + "\n\n## My request:\n额外真实要求"}}}},
		{"response_item", responseItem{Type: "message", Role: "assistant", Phase: "commentary", Content: []contentItem{{Type: "output_text", Text: "progress excluded"}}}},
		{"response_item", responseItem{Type: "message", Role: "assistant", Phase: "final_answer", Content: []contentItem{{Type: "output_text", Text: "COMPLETE\n\n<example>answer</example>"}}}},
	}
	for _, r := range records {
		if err := enc.Encode(map[string]any{"type": r.kind, "payload": r.payload}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(s.Path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	content, count, err := renderSession(s, false)
	if err != nil || count != 2 || strings.Count(content, "# 🧑 User\n") != 1 || strings.Count(content, "# 💻 Assistant\n") != 1 {
		t.Fatalf("empty user or phase filtering failure: %d %v", count, err)
	}
	if !strings.Contains(content, "# 用户正文\n\n"+syntheticEnvironment) || !strings.Contains(content, "额外真实要求") || !strings.Contains(content, "COMPLETE\n\n<example>answer</example>") || strings.Contains(content, "Context from my IDE") || strings.Contains(content, "Files pasted") || strings.Contains(content, "progress excluded") {
		t.Fatal("shared normalization lost content")
	}
	exports := t.TempDir()
	single, err := writeMarkdownExportInDir("", exports, s, content, false)
	if err != nil {
		t.Fatal(err)
	}
	project.Sessions = []sessionSummary{s}
	r := exportProjectSessions(exports, project)
	if r.failed() || r.Success != 1 {
		t.Fatalf("batch failed: %+v", r)
	}
	files, _ := filepath.Glob(filepath.Join(r.Directory, "*.md"))
	one, _ := os.ReadFile(single)
	two, _ := os.ReadFile(files[0])
	if !bytes.Equal(one, two) {
		t.Fatal("single and batch cleaning differs")
	}
	if again := exportProjectSessions(exports, project); again.Skipped != 1 || again.failed() {
		t.Fatal("index repeat skip regressed")
	}
}
