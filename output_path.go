package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const maxOutputPathUnits = 240 // leave room below legacy Windows MAX_PATH

func filenameUnits(text string) int {
	n := 0
	for _, r := range text {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func safeFilenameTitle(title string, limit int) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case strings.ContainsRune(`<>:"/\|?*`, r), unicode.IsControl(r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	text := strings.Join(strings.Fields(b.String()), "-")
	var result strings.Builder
	units := 0
	for _, r := range text {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > limit {
			break
		}
		result.WriteRune(r)
		units += size
	}
	return strings.Trim(result.String(), " .-_")
}

func determineOutputPath(output string, summary sessionSummary) (string, error) {
	return determineOutputPathInDir(output, "", summary)
}

func resolveOutputRoot(directory string) (string, error) {
	if directory == "" {
		return savedOutputRoot()
	}
	return resolveExplicitOutputDirectory(directory)
}

func determineOutputPathInDir(output, directory string, summary sessionSummary) (string, error) {
	if output != "" {
		return output, nil
	}
	directory, err := resolveOutputRoot(directory)
	if err != nil {
		return "", err
	}
	name, err := filenameForDirectory(directory, summary)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, name), nil
}

func filenameForDirectory(directory string, summary sessionSummary) (string, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	// Date prefix, extension, separator, and room for a collision suffix.
	limit := min(100, maxOutputPathUnits-filenameUnits(directory)-1-9-3-12)
	if limit < 24 {
		return "", errors.New("export directory path is too long; choose a shorter output directory or -output path")
	}
	title := safeFilenameTitle(summary.Title, limit)
	if title == "" {
		id := safeFilenameTitle(summary.SessionID, 8)
		if id == "" {
			id = "unknown"
		}
		title = "codex-session_" + id
	}
	date := "00000000"
	created := summary.CreatedAt
	if created.IsZero() {
		created = parseCreatedAtFromFilename(filepath.Base(summary.Path))
	}
	if !created.IsZero() {
		date = created.Format("20060102")
	}
	return date + "_" + title + ".md", nil
}

func writeMarkdownExport(output string, summary sessionSummary, content string, force bool) (string, error) {
	return writeMarkdownExportInDir(output, "", summary, content, force)
}

func writeMarkdownExportInDir(output, directory string, summary sessionSummary, content string, force bool) (string, error) {
	base, err := determineOutputPathInDir(output, directory, summary)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return "", fmt.Errorf("create export directory: %w", err)
	}
	for suffix := 1; ; suffix++ {
		path := base
		if suffix > 1 {
			path = fmt.Sprintf("%s_%d.md", strings.TrimSuffix(base, ".md"), suffix)
		}
		if output == "" {
			absolute, err := filepath.Abs(path)
			if err != nil || filenameUnits(absolute) > maxOutputPathUnits {
				return "", errors.New("output path is too long; use -output with a shorter path")
			}
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if output != "" && force {
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		}
		file, err := os.OpenFile(path, flags, 0o644)
		if errors.Is(err, os.ErrExist) && output == "" {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create export file %s: %w", path, err)
		}
		if err := finishMarkdownFile(file, content); err != nil {
			return "", fmt.Errorf("write export file %s: %w", path, err)
		}
		return path, nil
	}
}

func finishMarkdownFile(file *os.File, content string) error {
	_, writeErr := file.WriteString(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
