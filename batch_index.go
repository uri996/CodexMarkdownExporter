package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type batchEntry struct {
	File       string `json:"file"`
	Status     string `json:"status"`
	SessionKey string `json:"session_key"`
	SourceHash string `json:"source_hash"`
	OutputHash string `json:"output_hash"`
}

type batchIndex struct {
	Version    int                   `json:"version"`
	ProjectKey string                `json:"project_key"`
	Directory  string                `json:"directory"`
	Entries    map[string]batchEntry `json:"entries"`
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func safeLeaf(name string) bool {
	if name == "" || name == "." || name == ".." || strings.TrimRight(name, " .") != name {
		return false
	}
	for _, r := range name {
		if strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func readBatchIndex(path, key string) (batchIndex, error) {
	var index batchIndex
	file, err := os.Open(path)
	if err != nil {
		return index, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024*1024))
	if err := decoder.Decode(&index); err != nil {
		return index, errors.New("batch index is corrupt or unreadable; existing Markdown will not be overwritten")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return index, errors.New("batch index has trailing or oversized data")
	}
	if index.Version != 1 || index.ProjectKey != key || !validFingerprint(key) || !safeLeaf(index.Directory) || strings.HasPrefix(index.Directory, ".") || windowsReservedName(index.Directory) || filenameUnits(index.Directory) > 85 || index.Entries == nil {
		return index, errors.New("invalid batch index identity or output directory")
	}
	for source, entry := range index.Entries {
		if !validFingerprint(source) || !safeLeaf(entry.File) || windowsReservedName(entry.File) || !strings.HasSuffix(entry.File, ".md") ||
			(entry.Status != "pending" && entry.Status != "done") || !validFingerprint(entry.SessionKey) || !validFingerprint(entry.SourceHash) || !validFingerprint(entry.OutputHash) {
			return index, errors.New("invalid batch index entry; refusing unsafe recovery")
		}
	}
	return index, nil
}

func saveBatchIndex(path string, index batchIndex) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".index-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	encodeErr := json.NewEncoder(temp).Encode(index)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(encodeErr, syncErr, closeErr); err != nil {
		return err
	}
	// Same-directory replacement leaves the previous index intact on failure.
	return os.Rename(name, path)
}

func prepareBatchIndex(root string, project projectSummary) (batchIndex, string, string, error) {
	management := filepath.Join(root, ".codex-exporter", "index")
	if err := os.MkdirAll(management, 0o700); err != nil {
		return batchIndex{}, "", "", err
	}
	indexPath := filepath.Join(management, project.Key+".json")
	index, err := readBatchIndex(indexPath, project.Key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return index, "", "", err
	}
	newIndex := errors.Is(err, os.ErrNotExist)
	owners := make(map[string]string)
	files, err := os.ReadDir(management)
	if err != nil {
		return index, "", "", err
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(file.Name(), ".json")
		other, err := readBatchIndex(filepath.Join(management, file.Name()), key)
		if err != nil {
			return index, "", "", err
		}
		lower := strings.ToLower(other.Directory)
		if previous, ok := owners[lower]; ok && previous != key {
			return index, "", "", errors.New("conflicting project directory ownership in batch indexes")
		}
		owners[lower] = key
	}
	if newIndex {
		base := safeProjectName(project.Name)
		directory := base
		if owner, ok := owners[strings.ToLower(directory)]; ok && owner != project.Key {
			directory = base + "_" + project.Key[:12]
		}
		if owner, ok := owners[strings.ToLower(directory)]; ok && owner != project.Key {
			return index, "", "", errors.New("project directory fingerprint collision")
		}
		// An existing unowned directory may contain private, hand-edited exports
		// from a lost index. Require a fresh root instead of guessing ownership.
		entries, err := os.ReadDir(root)
		if err != nil {
			return index, "", "", err
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), directory) {
				return index, "", "", errors.New("project output directory exists without its index; select a fresh -output-dir")
			}
		}
		index = batchIndex{Version: 1, ProjectKey: project.Key, Directory: directory, Entries: make(map[string]batchEntry)}
		// Persist directory ownership before creating the directory or any files.
		if err := saveBatchIndex(indexPath, index); err != nil {
			return index, "", "", err
		}
	}
	directory := filepath.Join(root, index.Directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return index, "", "", err
	}
	if info, err := os.Lstat(directory); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return index, "", "", errors.New("project output directory must not be a symbolic link or junction")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return index, "", "", err
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil || !pathWithin(resolvedRoot, resolvedDirectory) || normalizePath(resolvedDirectory) == normalizePath(resolvedRoot) {
		return index, "", "", errors.New("project output directory cannot be safely resolved within the export root")
	}
	return index, indexPath, directory, nil
}

func reserveBatchFilename(directory string, summary sessionSummary, index batchIndex) (string, error) {
	base, err := filenameForDirectory(directory, summary)
	if err != nil {
		return "", err
	}
	reserved := make(map[string]bool)
	for _, entry := range index.Entries {
		reserved[strings.ToLower(entry.File)] = true
	}
	// Case-insensitive collision handling also keeps exports portable to Windows.
	files, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		reserved[strings.ToLower(file.Name())] = true
	}
	for suffix := 1; ; suffix++ {
		name := base
		if suffix > 1 {
			name = fmt.Sprintf("%s_%d.md", strings.TrimSuffix(base, ".md"), suffix)
		}
		if filenameUnits(filepath.Join(directory, name)) > maxOutputPathUnits {
			return "", errors.New("batch filename exceeds Windows path budget")
		}
		if !reserved[strings.ToLower(name)] {
			return name, nil
		}
	}
}
