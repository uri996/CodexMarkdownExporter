package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type batchResult struct {
	Selected           int
	Success            int
	Skipped            int
	Deduplicated       int
	Conflicts          int
	ReadFailed         int
	WriteFailed        int
	IndexFailed        int
	Written            int
	AttachmentWarnings int
	Directory          string
	Errors             []string
}

func (r batchResult) failed() bool { return r.ReadFailed+r.WriteFailed+r.IndexFailed > 0 }

func sessionSourceKey(s sessionSummary) string {
	// Content is not part of identity: appending turns never creates a new key.
	return fingerprint(s.SessionID + "\x00" + normalizePath(s.Path))
}

func fileFingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func safeFailure(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	case errors.Is(err, os.ErrNotExist):
		return "file or directory missing"
	default:
		var pathError *os.PathError
		var linkError *os.LinkError
		if errors.As(err, &pathError) || errors.As(err, &linkError) {
			return "filesystem I/O failure"
		}
		return err.Error() // only program-generated diagnostics, never input text
	}
}

func exportProjectSessions(root string, project projectSummary) batchResult {
	result := batchResult{Selected: len(project.Sessions)}
	fail := func(stage, key string, err error) {
		switch stage {
		case "read":
			result.ReadFailed++
		case "write":
			result.WriteFailed++
		default:
			result.IndexFailed++
		}
		result.Errors = append(result.Errors, stage+" "+key+": "+safeFailure(err))
	}
	root, err := resolveOutputRoot(root)
	if err != nil {
		fail("write", "export-root", err)
		return result
	}
	management := filepath.Join(root, ".codex-exporter")
	if err := os.MkdirAll(management, 0o700); err != nil {
		fail("write", "export-root", err)
		return result
	}
	lockPath := filepath.Join(management, "batch.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		result.IndexFailed++
		result.Errors = append(result.Errors, "index: output root is locked or inaccessible; if a previous run crashed, confirm no export is running before removing .codex-exporter/batch.lock")
		return result
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		fail("index", "lock", err)
		return result
	}
	defer os.Remove(lockPath)
	index, indexPath, directory, err := prepareBatchIndex(root, project)
	if err != nil {
		result.IndexFailed++
		// Index validation messages contain no prompt or raw filesystem path.
		if _, ok := err.(*os.PathError); ok {
			result.Errors = append(result.Errors, "index: "+safeFailure(err))
		} else {
			result.Errors = append(result.Errors, "index: "+err.Error())
		}
		return result
	}
	result.Directory = directory
	seenContent := make(map[string]batchEntry)
	for _, entry := range index.Entries {
		if entry.Status == "done" {
			if info, err := os.Lstat(filepath.Join(directory, entry.File)); err == nil && info.Mode().IsRegular() {
				seenContent[entry.SourceHash] = entry
			}
		}
	}
	seenIDs := make(map[string]map[string]bool)
	for _, summary := range project.Sessions {
		key := sessionSourceKey(summary)
		short := key[:12]
		digest, err := fileFingerprint(summary.Path)
		if err != nil {
			fail("read", short, err)
			continue
		}
		idKey := fingerprint(summary.SessionID)
		if seenIDs[idKey] == nil {
			seenIDs[idKey] = make(map[string]bool)
		}
		if !seenIDs[idKey][digest] && len(seenIDs[idKey]) > 0 {
			result.Conflicts++
		}
		seenIDs[idKey][digest] = true
		entry, exists := index.Entries[key]
		if exists {
			path := filepath.Join(directory, entry.File)
			info, statErr := os.Lstat(path)
			if statErr == nil {
				if !info.Mode().IsRegular() {
					fail("write", short, errors.New("existing target is not a regular file"))
					continue
				}
				if entry.Status == "pending" {
					actual, readErr := fileFingerprint(path)
					if readErr != nil || actual != entry.OutputHash {
						fail("write", short, errors.New("pending target differs; preserve it and use a fresh output root"))
						continue
					}
					entry.Status = "done"
					index.Entries[key] = entry
					if err := saveBatchIndex(indexPath, index); err != nil {
						entry.Status = "pending"
						index.Entries[key] = entry
						fail("index", short, err)
						continue
					}
				}
				result.Skipped++
				seenContent[digest] = entry
				continue // preserve hand edits and growing-session snapshots
			}
			if !errors.Is(statErr, os.ErrNotExist) {
				fail("write", short, statErr)
				continue
			}
		}
		if previous, ok := seenContent[digest]; ok {
			index.Entries[key] = previous
			if err := saveBatchIndex(indexPath, index); err != nil {
				fail("index", short, err)
				continue
			}
			result.Deduplicated++
			continue
		}
		content, _, err := renderSession(summary, false)
		if err != nil {
			fail("read", short, err)
			continue
		}
		// Refuse to register an inconsistent snapshot if the active rollout grew
		// while rendering. A later run can retry this source without duplicates.
		after, err := fileFingerprint(summary.Path)
		if err != nil || after != digest {
			fail("read", short, errors.New("session changed while exporting; retry"))
			continue
		}
		if !exists {
			entry.File, err = reserveBatchFilename(directory, summary, index)
			if err != nil {
				fail("write", short, err)
				continue
			}
		}
		entry.Status = "pending"
		entry.SessionKey = idKey
		entry.SourceHash = digest
		entry.OutputHash = fingerprint(content)
		index.Entries[key] = entry
		if err := saveBatchIndex(indexPath, index); err != nil {
			fail("index", short, err)
			continue
		}
		path := filepath.Join(directory, entry.File)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			fail("write", short, err)
			continue
		}
		if err := finishMarkdownFile(file, content); err != nil {
			// Remove only the file exclusively created by this iteration; the
			// durable pending reservation keeps retries on the same filename.
			removeErr := os.Remove(path)
			fail("write", short, errors.Join(err, removeErr))
			continue
		}
		result.Written++
		entry.Status = "done"
		index.Entries[key] = entry
		if err := saveBatchIndex(indexPath, index); err != nil {
			entry.Status = "pending"
			index.Entries[key] = entry
			fail("index", short, err)
			continue
		}
		result.Success++
		result.AttachmentWarnings += strings.Count(content, pastedTextUnavailable)
		seenContent[digest] = entry
	}
	return result
}

func runBatchExport(opts options) error {
	found := discoverSessions(expandHome(opts.sessionsDir), true)
	fmt.Printf("Rollouts discovered: %d\nInteractive rollout records: %d\n", found.Rollouts, found.Interactive)
	var project projectSummary
	issues := append([]error(nil), found.Issues...)
	if opts.project != "" {
		var groupingIssues []error
		project, groupingIssues = sessionsForProject(opts.project, found.Sessions)
		issues = append(issues, groupingIssues...)
	} else {
		projects, groupingIssues := groupSessionsByProject(found.Sessions)
		issues = append(issues, groupingIssues...)
		if len(projects) == 0 {
			return errors.New("no projects with interactive sessions found; discovery may be incomplete")
		}
		index, cancelled, err := runProjectPicker(projects, nil)
		if err != nil {
			return err
		}
		if cancelled {
			fmt.Println("cancelled")
			return nil
		}
		project = projects[index]
	}
	for i, issue := range issues {
		fmt.Fprintf(os.Stderr, "Discovery/grouping issue %d: %s; scan may be incomplete\n", i+1, safeFailure(issue))
	}
	if project.Historical {
		fmt.Fprintln(os.Stderr, "Historical cwd: grouping uses exact recorded paths; former repository boundaries cannot be recovered.")
	}
	if len(project.Sessions) == 0 {
		return errors.New("no interactive sessions could be assigned to the selected project")
	}
	result := exportProjectSessions(opts.outputDir, project)
	fmt.Printf("Project: %s\nProject rollout records: %d\nExported: %d\nExisting skipped: %d\nDuplicates: %d\nSame-ID content conflicts: %d\nRead failures: %d\nWrite failures: %d\nIndex failures: %d\nMarkdown files created: %d\nAttachment warnings: %d\nDiscovery/grouping issues: %d\nOutput: %s\n",
		project.Name, result.Selected, result.Success, result.Skipped, result.Deduplicated, result.Conflicts, result.ReadFailed, result.WriteFailed, result.IndexFailed, result.Written, result.AttachmentWarnings, len(issues), result.Directory)
	for _, message := range result.Errors {
		fmt.Fprintln(os.Stderr, message)
	}
	if result.failed() || len(issues) > 0 {
		return errors.New("batch completed with failures or incomplete discovery; review the summary")
	}
	return nil
}
