package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var rolloutNamePattern = regexp.MustCompile(`^rollout-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-.+\.jsonl(?:\.zst)?$`)
var stateDBPattern = regexp.MustCompile(`^state_(\d+)\.sqlite$`)

type discoveryResult struct {
	Sessions    []sessionSummary
	Rollouts    int
	Interactive int
	Issues      []error
}

func listSessions(sessionsDir string, currentCwd string, showAll bool) ([]sessionSummary, error) {
	found := discoverSessions(sessionsDir, true)
	summaries := make([]sessionSummary, 0, len(found.Sessions))
	for _, summary := range found.Sessions {
		if !showAll && (summary.Cwd == "" || normalizePath(summary.Cwd) != normalizePath(currentCwd)) {
			continue
		}
		summaries = append(summaries, summary)
	}
	return summaries, errors.Join(found.Issues...)
}

func discoverSessions(sessionsDir string, archives bool) discoveryResult {
	var result discoveryResult
	roots := []string{sessionsDir}
	if archives {
		roots = append(roots, filepath.Join(filepath.Dir(sessionsDir), "archived_sessions"))
	}
	titles, err := loadThreadNameIndex(sessionsDir)
	if err != nil {
		result.Issues = append(result.Issues, fmt.Errorf("read session title index: %w", err))
		titles = map[string]string{}
	}
	dbTitles, _ := loadThreadTitlesFromStateDB(filepath.Dir(sessionsDir))
	for id, title := range dbTitles {
		if _, ok := titles[id]; !ok {
			titles[id] = title
		}
	}
	seen := make(map[string]bool)
	for i, root := range roots {
		if i > 0 {
			if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		files, err := findRolloutFiles(root)
		if err != nil {
			result.Issues = append(result.Issues, err)
		}
		for _, path := range files {
			canonical := normalizePath(path)
			if seen[canonical] {
				continue
			}
			seen[canonical] = true
			result.Rollouts++
			summary, ok, err := readSummaryFromRollout(path)
			if err != nil {
				result.Issues = append(result.Issues, fmt.Errorf("read session summary: %w", err))
				continue
			}
			if !ok {
				continue
			}
			result.Interactive++
			summary.Title = titles[summary.SessionID]
			result.Sessions = append(result.Sessions, summary)
		}
	}
	sort.SliceStable(result.Sessions, func(i, j int) bool {
		left, right := result.Sessions[i], result.Sessions[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return normalizePath(left.Path) < normalizePath(right.Path)
	})
	return result
}

func findRolloutFiles(root string) ([]string, error) {
	var files []string
	var issues []error
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			issues = append(issues, fmt.Errorf("scan sessions directory: %w", walkErr))
			return nil
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") && isRolloutPath(d.Name()) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		issues = append(issues, err)
	}
	return files, errors.Join(issues...)
}

func readSummaryFromRollout(path string) (sessionSummary, bool, error) {
	file, err := openRollout(path)
	if err != nil {
		return sessionSummary{}, false, err
	}
	defer file.Close()

	summary := sessionSummary{Path: file.Path}
	fileInfo, statErr := file.file.Stat()
	if statErr == nil {
		summary.UpdatedAt = fileInfo.ModTime()
	}
	summary.CreatedAt = parseCreatedAtFromFilename(filepath.Base(file.Path))

	scanner := newRolloutScanner(file)

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
			if summary.CreatedAt.IsZero() {
				summary.CreatedAt, _ = time.Parse(time.RFC3339Nano, meta.Timestamp)
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
	if err := rolloutScanError(scanner); err != nil {
		return sessionSummary{}, false, err
	}
	if !metaFound {
		return sessionSummary{}, false, fmt.Errorf("missing session_meta in %s", path)
	}
	// Validate the remaining compressed stream, including its checksum, before
	// advertising an interactive source as readable. No transcript is buffered.
	if file.decoder != nil {
		if _, err := io.Copy(io.Discard, file); err != nil {
			return sessionSummary{}, false, err
		}
	}
	return summary, true, nil
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
		"-readonly",
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
