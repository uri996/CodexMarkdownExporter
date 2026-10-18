package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testAttachmentID = "11111111-1111-1111-1111-111111111111"
const secondAttachmentID = "22222222-2222-2222-2222-222222222222"

func attachmentFixture(t *testing.T) (string, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "Codex home with spaces")
	t.Setenv("CODEX_HOME", home)
	root := filepath.Join(home, "attachments")
	return root, writeAttachment(t, root, testAttachmentID, "Pasted text.txt", "真实 Prompt 🌏\n\n## 标题\n\n**保留 Markdown**\n")
}

func writeAttachment(t *testing.T, root, id, name, body string) string {
	t.Helper()
	path := filepath.Join(root, id, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pastedWrapper(paths ...string) string {
	var b strings.Builder
	b.WriteString("# Files pasted by the user:\n\n")
	for _, path := range paths {
		b.WriteString("## \"synthetic preview...\": " + path + "\n\n")
	}
	b.WriteString("Pasted text contains the user's request.\n\n## My request:\n")
	return b.String()
}

func TestResolvePastedText(t *testing.T) {
	root, path := attachmentFixture(t)
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second := writeAttachment(t, root, secondAttachmentID, "pasted-text.txt", "第二个附件")
	cases := []struct{ name, body, want string }{
		{"short prompt", "Please update the README.", "Please update the README."},
		{"ordinary Windows path", `C:\some\random\file.txt`, `C:\some\random\file.txt`},
		{"ordinary attachment path", path, path},
		{"single Unicode Markdown and spaces", pastedWrapper(path), string(text) + "\n\n"},
		{"forward slash native path", pastedWrapper(filepath.ToSlash(path)), string(text) + "\n\n"},
		{"multiple ordered", pastedWrapper(path, second), string(text) + "\n\n第二个附件\n\n"},
		{"mixed text", "before\n\n" + pastedWrapper(path) + "after", "before\n\n" + string(text) + "\n\nafter"},
		{"multiple wrappers", pastedWrapper(path) + "between\n\n" + pastedWrapper(second), string(text) + "\n\nbetween\n\n第二个附件\n\n"},
		{"plain headings multiline path", "Files pasted by the user:\n\n\"preview\":\n" + path + "\n\nPasted text contains the user's request.", string(text) + "\n\n"},
		{"CRLF", strings.ReplaceAll(pastedWrapper(path), "\n", "\r\n"), string(text) + "\n\n"},
		{"missing marker", "Files pasted by the user:\n\"preview\": " + path, "Files pasted by the user:\n\"preview\": " + path},
		{"unknown schema", "Files pasted by the user:\n" + path + "\nPasted text contains the user's request.", "Files pasted by the user:\n" + path + "\nPasted text contains the user's request."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePastedTextAttachments(tc.body, root); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPastedTextRejectsUnsafePaths(t *testing.T) {
	root, path := attachmentFixture(t)
	outside := writeAttachment(t, t.TempDir(), testAttachmentID, "Pasted text.txt", "PRIVATE OUTSIDE")
	wrongName := writeAttachment(t, root, testAttachmentID, "secret.txt", "PRIVATE NAME")
	wrongID := writeAttachment(t, root, "not-a-uuid", "Pasted text.txt", "PRIVATE ID")
	invalidUTF8 := writeAttachment(t, root, secondAttachmentID, "Pasted text.txt", string([]byte{0xff}))
	directory := filepath.Join(root, "33333333-3333-3333-3333-333333333333", "Pasted text.txt")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		outside, wrongName, wrongID, invalidUTF8, directory,
		filepath.Join(root, "44444444-4444-4444-4444-444444444444", "Pasted text.txt"),
		filepath.Dir(path) + string(filepath.Separator) + ".." + string(filepath.Separator) + testAttachmentID + string(filepath.Separator) + "Pasted text.txt",
		root + `\..\..\secret.txt`,
		"~/../.codex/attachments/" + testAttachmentID + "/Pasted text.txt",
		"../../secret.txt", `C:\secret.txt`, "Pasted text.txt",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if _, err := readPastedText(root, path); err == nil {
				t.Fatal("unsafe path unexpectedly accepted")
			}
			if got := resolvePastedTextAttachments(pastedWrapper(path), root); !strings.HasPrefix(got, pastedTextUnavailable) || strings.Contains(got, path) {
				t.Fatalf("expected safe fallback, got %q", got)
			}
		})
	}
	// A failure does not discard the other attachments in the same message.
	got := resolvePastedTextAttachments(pastedWrapper(outside, path), root)
	if !strings.HasPrefix(got, pastedTextUnavailable) || !strings.Contains(got, "真实 Prompt") {
		t.Fatalf("unexpected mixed success/failure: %q", got)
	}
}

func TestPastedTextSymlinkEscape(t *testing.T) {
	root, path := attachmentFixture(t)
	outside := writeAttachment(t, t.TempDir(), testAttachmentID, "Pasted text.txt", "PRIVATE OUTSIDE")
	for _, directoryLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directoryLink], func(t *testing.T) {
			link := filepath.Join(root, secondAttachmentID)
			target := filepath.Dir(outside)
			if !directoryLink {
				if err := os.MkdirAll(link, 0o755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, "Pasted text.txt")
				target = outside
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(link) })
			candidate := filepath.Join(root, secondAttachmentID, "Pasted text.txt")
			if _, err := readPastedText(root, candidate); err == nil {
				t.Fatal("symlink escape accepted")
			}
		})
	}
	// Never recursively expand wrapper-like content inside an attachment.
	if err := os.WriteFile(path, []byte(pastedWrapper(outside)), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolvePastedTextAttachments(pastedWrapper(path), root)
	if strings.Contains(got, "PRIVATE OUTSIDE") || !strings.Contains(got, outside) {
		t.Fatalf("attachment content was recursively interpreted: %q", got)
	}
}

func TestPastedTextPermissionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows read failure is tested with an exclusive file handle")
	}
	root, path := attachmentFixture(t)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("current user bypasses file permissions")
	}
	if got := resolvePastedTextAttachments(pastedWrapper(path), root); got != pastedTextUnavailable+"没有读取附件的权限]\n\n" {
		t.Fatalf("expected safe fallback, got %q", got)
	}
}

// Reproduces the observed Desktop structure using only synthetic data: a
// localized filename under the same UUID layout, with mixed Windows separators.
func TestLocalizedPastedTextFilename(t *testing.T) {
	root, _ := attachmentFixture(t)
	text := "中文完整 Prompt 🌏\n\n```xml\n<example>保留正文</example>\n```\n\n<!-- 用户注释 -->\n"
	path := writeAttachment(t, root, secondAttachmentID, "已粘贴的文本.txt", text)
	mixedPath := root + "/" + secondAttachmentID + "/已粘贴的文本.txt"
	for _, candidate := range []string{path, mixedPath} {
		body := "普通文本\n\n" + pastedWrapper(candidate) + "结尾文本"
		got := resolvePastedTextAttachments(body, root)
		if got != "普通文本\n\n"+text+"\n\n结尾文本" {
			t.Fatalf("localized attachment failed: %q", got)
		}
	}
	// A localized name does not waive containment, UUID, or filename checks.
	outside := writeAttachment(t, t.TempDir(), secondAttachmentID, "已粘贴的文本.txt", "outside")
	unknown := writeAttachment(t, root, secondAttachmentID, "其他文件.txt", "private")
	for _, candidate := range []string{outside, unknown} {
		if _, err := readPastedText(root, candidate); err == nil {
			t.Fatal("localized filename broadened the read boundary")
		}
	}
}

func TestPastedTextDiagnosticReasons(t *testing.T) {
	root, path := attachmentFixture(t)
	directory := filepath.Join(root, secondAttachmentID, "Pasted text.txt")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	invalidUTF8 := writeAttachment(t, root, testAttachmentID, "pasted-text.txt", string([]byte{0xff}))
	unknownName := writeAttachment(t, root, testAttachmentID, "other.txt", "private")
	missing := filepath.Join(root, "33333333-3333-3333-3333-333333333333", "Pasted text.txt")
	for _, tc := range []struct{ path, reason string }{
		{missing, "附件文件或其目录不存在"},
		{root + "/../secret.txt", "路径越界或包含不允许的路径跳转，已拒绝读取"},
		{unknownName, "路径结构或文件名不符合已识别的 Codex pasted-text 格式"},
		{directory, "附件不是普通文件"},
		{invalidUTF8, "附件文本不是有效 UTF-8"},
	} {
		got := resolvePastedTextAttachments(pastedWrapper(tc.path), root)
		if got != pastedTextUnavailable+tc.reason+"]\n\n" || strings.Contains(got, root) {
			t.Fatalf("unexpected diagnostic: %q", got)
		}
	}
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{&os.PathError{Path: path, Err: os.ErrPermission}, "没有读取附件的权限"},
		{errors.New("sensitive raw I/O error with path " + path), "无法打开或读取（文件可能被占用或发生 I/O 错误）"},
		{errAttachmentRoot, "附件根目录不可用"},
		{errAttachmentResolve, "无法安全解析附件路径（可能涉及链接或重解析点）"},
	} {
		if got := pastedTextFailure(tc.err); got != pastedTextUnavailable+tc.reason+"]" || strings.Contains(got, path) {
			t.Fatalf("unsafe or imprecise error: %q", got)
		}
	}
}

func TestPastedTextConversationExports(t *testing.T) {
	_, path := attachmentFixture(t)
	var lines []string
	add := func(kind string, payload any) {
		b, err := json.Marshal(map[string]any{"type": kind, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	add("session_meta", sessionMetaPayload{ID: "synthetic-session"})
	message := func(role, phase, text string) {
		add("response_item", responseItem{Type: "message", Role: role, Phase: phase, Content: []contentItem{{Type: "input_text", Text: text}}})
	}
	message("developer", "", "PRIVATE DEVELOPER")
	message("user", "", "short first prompt")
	message("assistant", "commentary", "PRIVATE COMMENTARY")
	message("assistant", "final", "first final reply")
	message("user", "", "before attachment\n\n"+pastedWrapper(path)+"after attachment")
	message("assistant", "analysis", "PRIVATE REASONING")
	message("tool", "", "PRIVATE TOOL OUTPUT")
	message("assistant", "final_answer", "second final reply")
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, messages, err := collectConversation(rollout)
	if err != nil {
		t.Fatal(err)
	}
	messages = selectMessagesForExport(messages, false)
	messages = normalizePastedTextMessages(messages, codexAttachmentRoot())
	if len(messages) != 4 {
		t.Fatalf("expected User/Final/User/Final, got %+v", messages)
	}
	output := buildMarkdown(sessionSummary{Title: "Synthetic"}, messages)
	last := -1
	for _, text := range []string{"short first prompt", "first final reply", "before attachment", "真实 Prompt 🌏", "after attachment", "second final reply"} {
		index := strings.Index(output, text)
		if index <= last {
			t.Fatalf("missing or out-of-order %q", text)
		}
		last = index
	}
	for _, excluded := range []string{path, "Files pasted by the user", "Pasted text contains", "My request:", "PRIVATE"} {
		if strings.Contains(output, excluded) {
			t.Fatalf("unexpected %q in export", excluded)
		}
	}
	if !strings.Contains(output, "**保留 Markdown**") {
		t.Fatal("attachment Markdown changed")
	}
}

func TestNormalizePastedTextMessagesPreservesUserContent(t *testing.T) {
	root, path := attachmentFixture(t)
	text := "<environment_context> is literal user content here"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := []exportMessage{
		{Role: "user", Body: pastedWrapper(path)},
		{Role: "assistant", Phase: "final", Body: pastedWrapper(path)},
		{Role: "user", Body: "<environment_context><cwd>/synthetic</cwd><shell>synthetic</shell></environment_context>"},
	}
	got := normalizePastedTextMessages(selectMessagesForExport(messages, false), root)
	if len(got) != 2 || !strings.Contains(got[0].Body, text) || got[1].Body != messages[1].Body {
		t.Fatalf("user prose or assistant content changed unexpectedly: %+v", got)
	}
	if messages[0].Body != pastedWrapper(path) {
		t.Fatal("normalization mutated input messages")
	}
}

func TestCodexAttachmentRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	want := filepath.Join(home, ".codex", "attachments")
	if got := codexAttachmentRoot(); got != want {
		t.Fatalf("default root = %q, want %q", got, want)
	}
	path := writeAttachment(t, want, testAttachmentID, "Pasted text.txt", "tilde prompt")
	tildePath := "~/" + filepath.ToSlash(strings.TrimPrefix(path, home+string(filepath.Separator)))
	if got, err := readPastedText(want, tildePath); err != nil || got != "tilde prompt" {
		t.Fatalf("tilde path = %q, %v", got, err)
	}
	custom := filepath.Join(home, "custom Codex")
	t.Setenv("CODEX_HOME", custom)
	if got := codexAttachmentRoot(); got != filepath.Join(custom, "attachments") {
		t.Fatalf("custom root = %q", got)
	}
}
