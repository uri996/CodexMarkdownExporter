package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const pastedTextUnavailable = "[Codex pasted-text attachment 未展开："

var (
	errAttachmentRoot       = errors.New("attachment root unavailable")
	errAttachmentPath       = errors.New("attachment path rejected")
	errAttachmentSchema     = errors.New("attachment path structure mismatch")
	errAttachmentNonRegular = errors.New("attachment is not a regular file")
	errAttachmentUTF8       = errors.New("attachment is not valid UTF-8")
	errAttachmentResolve    = errors.New("attachment path could not be safely resolved")
)

// Never expose raw filesystem errors: PathError contains the private path.
func pastedTextFailure(err error) string {
	reason := "无法打开或读取（文件可能被占用或发生 I/O 错误）"
	switch {
	case errors.Is(err, errAttachmentRoot):
		reason = "附件根目录不可用"
	case errors.Is(err, errAttachmentPath):
		reason = "路径越界或包含不允许的路径跳转，已拒绝读取"
	case errors.Is(err, errAttachmentSchema):
		reason = "路径结构或文件名不符合已识别的 Codex pasted-text 格式"
	case errors.Is(err, errAttachmentNonRegular):
		reason = "附件不是普通文件"
	case errors.Is(err, errAttachmentUTF8):
		reason = "附件文本不是有效 UTF-8"
	case errors.Is(err, errAttachmentResolve):
		reason = "无法安全解析附件路径（可能涉及链接或重解析点）"
	case errors.Is(err, os.ErrNotExist):
		reason = "附件文件或其目录不存在"
	case errors.Is(err, os.ErrPermission):
		reason = "没有读取附件的权限"
	}
	return pastedTextUnavailable + reason + "]"
}

var pastedTextWrapper = regexp.MustCompile(`(?ms)^(?:#{1,6}[ \t]+)?Files pasted by the user:[ \t]*\r?\n(.*?)^Pasted text contains the user's request\.[ \t]*(?:\r?\n|$)(?:\r?\n)*(?:#{1,6}[ \t]+My request:[ \t]*(?:\r?\n|$))?`)
var pastedTextLabel = regexp.MustCompile(`^(?:#{1,6}[ \t]+)?"[^\r\n]+":[ \t]*(.*)$`)
var attachmentUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Only local, trusted configuration chooses the root. Never infer it from a
// message's path, session metadata, or the location of an imported rollout.
func codexAttachmentRoot() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Join(home, "attachments")
}

// Normalize once after message selection, before rendering. Filtering the
// original message first keeps attachment prose from being mistaken for injected
// context just because the user's actual prompt starts with a context-like tag.
func normalizePastedTextMessages(messages []exportMessage, root string) []exportMessage {
	normalized := make([]exportMessage, len(messages))
	copy(normalized, messages)
	for i := range normalized {
		if normalized[i].Role == "user" {
			normalized[i].Body = resolvePastedTextAttachments(normalized[i].Body, root)
		}
	}
	return normalized
}

func resolvePastedTextAttachments(body, root string) string {
	// Replacement is deliberately non-recursive: attachment text is user content,
	// even when it contains another wrapper or local path.
	return pastedTextWrapper.ReplaceAllStringFunc(body, func(wrapper string) string {
		match := pastedTextWrapper.FindStringSubmatch(wrapper)
		paths, ok := pastedTextPaths(match[1])
		if !ok {
			return wrapper // Unknown schema: do not guess paths or read anything.
		}
		parts := make([]string, 0, len(paths))
		for _, path := range paths {
			text, err := readPastedText(root, path)
			if err != nil {
				text = pastedTextFailure(err)
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n\n") + "\n\n"
	})
}

// Accept only quoted preview entries, with the path either on the same line or
// the following line. All nonblank lines must belong to an entry.
func pastedTextPaths(entries string) ([]string, bool) {
	lines := strings.Split(strings.ReplaceAll(entries, "\r\n", "\n"), "\n")
	var paths []string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		label := pastedTextLabel.FindStringSubmatch(line)
		if label == nil {
			return nil, false
		}
		path := strings.TrimSpace(label[1])
		if path == "" {
			i++
			if i == len(lines) {
				return nil, false
			}
			path = strings.TrimSpace(lines[i])
		}
		if path == "" {
			return nil, false
		}
		paths = append(paths, path)
	}
	return paths, len(paths) > 0
}

func readPastedText(root, path string) (string, error) {
	if root == "" {
		return "", errAttachmentRoot
	}
	// Reject traversal before cleaning, including foreign-platform separators.
	for _, part := range strings.Split(strings.ReplaceAll(path, `\`, "/"), "/") {
		if part == "." || part == ".." {
			return "", errAttachmentPath
		}
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", errAttachmentPath
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errAttachmentPath
	}
	if !validPastedTextRelativePath(rel) {
		return "", errAttachmentSchema
	}
	resolvedRoot, err := resolveAttachmentPath(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := resolveAttachmentPath(path)
	if err != nil {
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || !filepath.IsLocal(resolvedRel) {
		return "", errAttachmentPath
	}
	if !validPastedTextRelativePath(resolvedRel) {
		return "", errAttachmentSchema
	}
	// OpenRoot also enforces containment during the open itself, avoiding the
	// check-then-open escape if a directory or link changes after resolution.
	dir, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	info, err := dir.Stat(resolvedRel)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errAttachmentNonRegular
	}
	file, err := dir.Open(resolvedRel)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errAttachmentNonRegular
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errAttachmentUTF8
	}
	return string(data), nil
}

func resolveAttachmentPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Some Windows reparse points can be stat'ed even when EvalSymlinks
		// reports not-found. Keep refusing the unresolved path, but do not claim
		// the attachment was deleted when the file is actually accessible.
		if _, statErr := os.Stat(path); statErr == nil {
			return "", errAttachmentResolve
		}
	}
	return resolved, err
}

func validPastedTextRelativePath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	return len(parts) == 2 && attachmentUUID.MatchString(parts[0]) &&
		(parts[1] == "Pasted text.txt" || parts[1] == "pasted-text.txt" || parts[1] == "已粘贴的文本.txt")
}
