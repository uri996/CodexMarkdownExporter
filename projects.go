package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type projectSummary struct {
	Name       string
	Root       string
	Key        string
	Sessions   []sessionSummary
	Historical bool
}

func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func newProject(root string) projectSummary {
	return projectSummary{Name: filepath.Base(root), Root: root, Key: fingerprint(normalizePath(root))}
}

func pathWithin(root, child string) bool {
	rel, err := filepath.Rel(normalizePath(root), normalizePath(child))
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// Recognizes both ordinary repositories and worktrees (.git is a file).
func nearestGitRoot(cwd string) (string, error) {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			return "", nil
		}
	}
}

func projectForSession(session sessionSummary) (projectSummary, error) {
	if !filepath.IsAbs(session.Cwd) {
		return projectSummary{}, errors.New("session cwd is missing or is not absolute")
	}
	cwd := filepath.Clean(session.Cwd)
	info, err := os.Stat(cwd)
	if errors.Is(err, os.ErrNotExist) {
		p := newProject(cwd)
		p.Historical = true
		return p, nil // exact historical cwd only; never guess a former Git root
	}
	if err != nil {
		return projectSummary{}, err
	}
	if !info.IsDir() {
		return projectSummary{}, errors.New("session cwd is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return projectSummary{}, fmt.Errorf("cannot safely resolve project cwd: %w", err)
	}
	root, err := nearestGitRoot(resolved)
	if err != nil {
		return projectSummary{}, err
	}
	if root == "" {
		root = resolved
	}
	return newProject(root), nil
}

func groupSessionsByProject(sessions []sessionSummary) ([]projectSummary, []error) {
	groups := make(map[string]*projectSummary)
	var issues []error
	for _, session := range sessions {
		project, err := projectForSession(session)
		if err != nil {
			issues = append(issues, fmt.Errorf("project grouping for source %s: %s", sessionSourceKey(session)[:12], safeFailure(err)))
			continue
		}
		if groups[project.Key] == nil {
			copy := project
			groups[project.Key] = &copy
		}
		groups[project.Key].Sessions = append(groups[project.Key].Sessions, session)
	}
	var projects []projectSummary
	for _, p := range groups {
		projects = append(projects, *p)
	}
	sort.Slice(projects, func(i, j int) bool { return normalizePath(projects[i].Root) < normalizePath(projects[j].Root) })
	return projects, issues
}

func sessionsForProject(root string, sessions []sessionSummary) (projectSummary, []error) {
	root, err := filepath.Abs(expandHome(root))
	if err != nil {
		return projectSummary{}, []error{err}
	}
	info, err := os.Stat(root)
	if err != nil {
		return projectSummary{}, []error{fmt.Errorf("project directory unavailable: %w", err)}
	}
	if !info.IsDir() {
		return projectSummary{}, []error{errors.New("project path is not a directory")}
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return projectSummary{}, []error{fmt.Errorf("cannot safely resolve project directory: %w", err)}
	}
	project := newProject(root)
	var issues []error
	for _, s := range sessions {
		if !filepath.IsAbs(s.Cwd) || !pathWithin(root, s.Cwd) {
			continue
		}
		if normalizePath(root) != normalizePath(s.Cwd) {
			info, err := os.Stat(s.Cwd)
			if err != nil || !info.IsDir() {
				issues = append(issues, errors.New("historical subdirectory cwd cannot be assigned reliably"))
				continue
			}
			nested, err := nearestGitRoot(normalizePath(s.Cwd))
			if err != nil {
				issues = append(issues, errors.New("cannot inspect nested repository boundary"))
				continue
			}
			if nested != "" && normalizePath(nested) != normalizePath(root) && pathWithin(root, nested) {
				continue
			}
		}
		project.Sessions = append(project.Sessions, s)
	}
	return project, issues
}

func safeProjectName(name string) string {
	name = safeFilenameTitle(name, 60)
	if name == "" {
		name = "project"
	}
	if windowsReservedName(name) {
		name = "project-" + name
	}
	return name
}

func windowsReservedName(name string) bool {
	base := strings.ToUpper(strings.Split(name, ".")[0])
	reserved := base == "CON" || base == "PRN" || base == "AUX" || base == "NUL"
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		reserved = true
	}
	return reserved
}
