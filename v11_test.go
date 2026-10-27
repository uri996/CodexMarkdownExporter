package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	return home
}

func makeDirectory(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func syntheticSession(t *testing.T, storage, id, cwd, source string) sessionSummary {
	t.Helper()
	makeDirectory(t, storage)
	path := filepath.Join(storage, "rollout-2026-10-13T09-00-00-"+id+".jsonl")
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	meta := map[string]any{"id": id, "cwd": cwd, "source": source, "timestamp": "2026-10-13T09:00:00Z"}
	if err := enc.Encode(map[string]any{"type": "session_meta", "payload": meta}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []responseItem{
		{Type: "message", Role: "user", Content: []contentItem{{Type: "input_text", Text: "synthetic prompt 中文"}}},
		{Type: "message", Role: "assistant", Phase: "commentary", Content: []contentItem{{Type: "output_text", Text: "synthetic progress"}}},
		{Type: "message", Role: "assistant", Phase: "final_answer", Content: []contentItem{{Type: "output_text", Text: "synthetic final 中文"}}},
	} {
		if err := enc.Encode(map[string]any{"type": "response_item", "payload": m}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return sessionSummary{Path: path, SessionID: id, Cwd: cwd, Source: source, Title: "Synthetic 中文", CreatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
}

func projectFixture(t *testing.T) (string, projectSummary) {
	t.Helper()
	base := t.TempDir()
	root := makeDirectory(t, filepath.Join(base, "项目 with spaces"))
	makeDirectory(t, filepath.Join(root, ".git"))
	project := newProject(root)
	storage := filepath.Join(base, "storage", "sessions")
	project.Sessions = []sessionSummary{
		syntheticSession(t, storage, "synthetic-a", root, "vscode"),
		syntheticSession(t, storage, "synthetic-b", root, "cli"),
	}
	return storage, project
}

func TestV11SingleExportDirectories(t *testing.T) {
	home := isolatedHome(t)
	if err := saveOutputRoot(filepath.Join(home, "chosen exports")); err != nil {
		t.Fatal(err)
	}
	storage, project := projectFixture(t)
	checkout := makeDirectory(t, filepath.Join(t.TempDir(), "checkout"))
	t.Chdir(checkout)
	if err := runSingleExport(options{sessionsDir: storage, sessionID: project.Sessions[0].SessionID, showAll: true}); err != nil {
		t.Fatal(err)
	}
	defaultRoot := filepath.Join(home, "chosen exports")
	files, err := filepath.Glob(filepath.Join(defaultRoot, "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("default output missing: %v %v", files, err)
	}
	if files, _ := filepath.Glob(filepath.Join(checkout, "*.md")); len(files) != 0 {
		t.Fatal("export polluted checkout")
	}
	if _, err := os.Stat(filepath.Join(defaultRoot, ".codex-exporter")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("single export initialized batch metadata")
	}
	customDir := filepath.Join(home, "自定义 output", "nested")
	if err := runSingleExport(options{sessionsDir: storage, showAll: true, last: true, outputDir: customDir}); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(customDir, "*.md")); len(files) != 1 {
		t.Fatal("custom output directory missing")
	}
	chosen := filepath.Join(home, "chosen", "exact.md")
	if err := runSingleExport(options{sessionsDir: storage, showAll: true, last: true, output: chosen}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(chosen); err != nil {
		t.Fatal(err)
	}
	// Corrupt batch state must not affect a subsequent single export.
	makeDirectory(t, filepath.Join(defaultRoot, ".codex-exporter", "index"))
	if err := os.WriteFile(filepath.Join(defaultRoot, ".codex-exporter", "index", "bad.json"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runSingleExport(options{sessionsDir: storage, showAll: true, last: true}); err != nil {
		t.Fatal("batch state leaked into single export:", err)
	}
	// Reporting an unrelated malformed rollout must not disable an otherwise
	// usable single-session selection, unlike batch's completeness requirement.
	if err := os.WriteFile(filepath.Join(storage, "rollout-unrelated-invalid.jsonl"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runSingleExport(options{sessionsDir: storage, showAll: true, last: true}); err != nil {
		t.Fatal("unrelated discovery error disabled single export:", err)
	}
}

func TestV11CLIValidation(t *testing.T) {
	for _, args := range [][]string{
		{"-project", "somewhere"}, {"-output", "x.md", "-output-dir", "exports"},
		{"-batch", "-output", "x.md"}, {"-batch", "-last"}, {"-batch", "-session-id", "synthetic"},
		{"-batch", "-all"}, {"-batch", "-full"}, {"-batch", "-force"},
		{"-batch", "-last=false"}, {"-output-dir", ""}, {"-batch", "-project", ""},
		{"-set-output-dir", ""}, {"-set-output-dir", "chosen", "-batch"}, {"-set-output-dir", "chosen", "-output-dir", "other"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			oldFlags, oldArgs := flag.CommandLine, os.Args
			t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
			flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
			os.Args = append([]string{"test"}, args...)
			if err := validateOptions(parseFlags()); err == nil {
				t.Fatal("invalid combination accepted")
			}
		})
	}
	for _, opts := range []options{{}, {showAll: true, last: true}, {output: "x.md", force: true}, {outputDir: "dir"}, {batch: true}, {batch: true, project: "path", outputDir: "dir"}} {
		if err := validateOptions(opts); err != nil {
			t.Fatalf("valid flags rejected: %+v: %v", opts, err)
		}
	}
}

func TestV11ProjectBoundaries(t *testing.T) {
	base := t.TempDir()
	root := makeDirectory(t, filepath.Join(base, "项目 with spaces"))
	makeDirectory(t, filepath.Join(root, ".git"))
	child := makeDirectory(t, filepath.Join(root, "frontend"))
	prefix := makeDirectory(t, root+"-backup")
	makeDirectory(t, filepath.Join(prefix, ".git"))
	nested := makeDirectory(t, filepath.Join(root, "nested"))
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	nestedChild := makeDirectory(t, filepath.Join(nested, "child"))
	other := makeDirectory(t, filepath.Join(base, "elsewhere", filepath.Base(root)))
	makeDirectory(t, filepath.Join(other, ".git"))
	sessions := []sessionSummary{{Cwd: root}, {Cwd: child}, {Cwd: prefix}, {Cwd: nested}, {Cwd: nestedChild}, {Cwd: other}}
	project, issues := sessionsForProject(root, sessions)
	if len(issues) != 0 || len(project.Sessions) != 2 {
		t.Fatalf("wrong project members: %+v %v", project.Sessions, issues)
	}
	groups, issues := groupSessionsByProject(sessions)
	if len(issues) != 0 || len(groups) != 4 {
		t.Fatalf("wrong grouped projects: %+v %v", groups, issues)
	}
	if pathWithin(root, prefix) || !pathWithin(root, child) {
		t.Fatal("path boundary error")
	}
	missing := filepath.Join(root, "removed")
	_, issues = sessionsForProject(root, []sessionSummary{{Cwd: missing}})
	if len(issues) != 1 {
		t.Fatal("historical membership guessed")
	}
	historical, err := projectForSession(sessionSummary{Cwd: missing})
	if err != nil || !historical.Historical || normalizePath(historical.Root) != normalizePath(missing) {
		t.Fatal("historical cwd not preserved")
	}
	if runtime.GOOS == "windows" {
		alias := strings.ToUpper(filepath.ToSlash(root))
		p, issues := sessionsForProject(alias, []sessionSummary{{Cwd: child}})
		if len(issues) != 0 || len(p.Sessions) != 1 || newProject(alias).Key != newProject(root).Key {
			t.Fatal("Windows path aliases not normalized")
		}
	}
}

func TestV11NonGitProjectGrouping(t *testing.T) {
	base := t.TempDir()
	if root, err := nearestGitRoot(base); err != nil || root != "" {
		t.Skip("temporary directory is inside an existing Git repository")
	}
	one := makeDirectory(t, filepath.Join(base, "one", "same-name"))
	two := makeDirectory(t, filepath.Join(base, "two", "same-name"))
	groups, issues := groupSessionsByProject([]sessionSummary{{Cwd: one}, {Cwd: two}})
	if len(issues) != 0 || len(groups) != 2 || groups[0].Key == groups[1].Key {
		t.Fatal("non-Git cwd identities were merged")
	}
}

func TestV11ProjectSymlink(t *testing.T) {
	base := t.TempDir()
	root := makeDirectory(t, filepath.Join(base, "real"))
	makeDirectory(t, filepath.Join(root, ".git"))
	link := filepath.Join(base, "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p, err := projectForSession(sessionSummary{Cwd: link})
	if err != nil || p.Key != newProject(root).Key {
		t.Fatalf("symlink project alias failed: %v", err)
	}
}

func TestV11DiscoveryArchivesAndErrors(t *testing.T) {
	base := t.TempDir()
	storage := filepath.Join(base, "sessions")
	root := makeDirectory(t, filepath.Join(base, "project"))
	s := syntheticSession(t, storage, "interactive", root, "vscode")
	syntheticSession(t, storage, "worker", root, "subagent")
	archives := makeDirectory(t, filepath.Join(base, "archived_sessions"))
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archives, filepath.Base(s.Path)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Do not require sqlite3 for discovery or title lookup.
	t.Setenv("PATH", t.TempDir())
	found := discoverSessions(storage, true)
	if found.Rollouts != 3 || found.Interactive != 2 || len(found.Sessions) != 2 || len(found.Issues) != 0 {
		t.Fatalf("wrong discovery: %+v", found)
	}
	p := newProject(root)
	p.Sessions = found.Sessions
	r := exportProjectSessions(filepath.Join(base, "exports"), p)
	if r.Success != 1 || r.Deduplicated != 1 || r.failed() {
		t.Fatalf("archive duplicate not handled: %+v", r)
	}
	missing := discoverSessions(filepath.Join(base, "missing"), true)
	if len(missing.Issues) == 0 {
		t.Fatal("scan access failure hidden")
	}
	bad := filepath.Join(storage, "rollout-malformed.jsonl")
	if err := os.WriteFile(bad, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	found = discoverSessions(storage, true)
	if len(found.Issues) == 0 || found.Interactive != 2 {
		t.Fatal("bad metadata did not surface or stopped valid discovery")
	}
	// A nonexistent archive directory is optional, not an incomplete scan.
	if err := os.Remove(filepath.Join(archives, filepath.Base(s.Path))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archives); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	if found := discoverSessions(storage, true); len(found.Issues) != 0 {
		t.Fatal(found.Issues)
	}
}

func TestV11BatchRepeatConflictsAndManualEdits(t *testing.T) {
	_, project := projectFixture(t)
	root := filepath.Join(t.TempDir(), "export root")
	// Same metadata ID but distinct source/content must remain a distinct item.
	conflict := syntheticSession(t, filepath.Join(t.TempDir(), "sessions"), project.Sessions[0].SessionID, project.Root, "vscode")
	f, err := os.OpenFile(conflict.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	project.Sessions = append(project.Sessions, conflict)
	first := exportProjectSessions(root, project)
	if first.Success != 3 || first.Conflicts != 1 || first.failed() {
		t.Fatalf("first batch: %+v", first)
	}
	files, _ := filepath.Glob(filepath.Join(first.Directory, "*.md"))
	if len(files) != 3 {
		t.Fatal("sessions were combined or lost")
	}
	if err := os.WriteFile(files[0], []byte("USER EDIT"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Appending turns on the same source path must not create a new identity.
	f, err = os.OpenFile(project.Sessions[0].Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	second := exportProjectSessions(root, project)
	if second.Skipped != 3 || second.Success != 0 || second.failed() {
		t.Fatalf("repeat batch: %+v", second)
	}
	files, _ = filepath.Glob(filepath.Join(first.Directory, "*.md"))
	if len(files) != 3 {
		t.Fatal("repeat generated copies")
	}
	if text, err := os.ReadFile(files[0]); err != nil || string(text) != "USER EDIT" {
		t.Fatal("manual edits overwritten")
	}
	indexPath := filepath.Join(root, ".codex-exporter", "index", project.Key+".json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(indexData, []byte(project.Root)) || bytes.Contains(indexData, []byte("synthetic prompt")) || bytes.Contains(indexData, []byte("synthetic final")) {
		t.Fatal("private content or path entered batch index")
	}
	if err := os.WriteFile(indexPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := exportProjectSessions(root, project); result.IndexFailed == 0 || result.Success != 0 {
		t.Fatal("corrupt index accepted")
	}
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if result := exportProjectSessions(root, project); result.IndexFailed == 0 || result.Success != 0 {
		t.Fatal("lost index guessed ownership")
	}
	files, _ = filepath.Glob(filepath.Join(first.Directory, "*.md"))
	if len(files) != 3 {
		t.Fatal("corrupt/lost index generated files")
	}
}

func TestV11BatchPartialReadFailureRetry(t *testing.T) {
	_, project := projectFixture(t)
	root := filepath.Join(t.TempDir(), "exports")
	data, err := os.ReadFile(project.Sessions[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(project.Sessions[0].Path); err != nil {
		t.Fatal(err)
	}
	first := exportProjectSessions(root, project)
	if first.ReadFailed != 1 || first.Success != 1 {
		t.Fatalf("batch stopped on first error: %+v", first)
	}
	if err := os.WriteFile(project.Sessions[0].Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	second := exportProjectSessions(root, project)
	if second.Success != 1 || second.Skipped != 1 || second.failed() {
		t.Fatalf("partial retry: %+v", second)
	}
}

func TestV11BatchParserFailureContinues(t *testing.T) {
	_, project := projectFixture(t)
	f, err := os.OpenFile(project.Sessions[0].Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(strings.Repeat("x", scannerBufferSize+1024) + "\n")
	if err := errors.Join(writeErr, f.Close()); err != nil {
		t.Fatal(err)
	}
	r := exportProjectSessions(filepath.Join(t.TempDir(), "exports"), project)
	if r.ReadFailed != 1 || r.Success != 1 {
		t.Fatalf("parser failure stopped the batch: %+v", r)
	}
}

func TestV11LongSameNamedProjectIndexes(t *testing.T) {
	_, one := projectFixture(t)
	_, two := projectFixture(t)
	one.Name = strings.Repeat("P", 90)
	two.Name = one.Name
	root := filepath.Join(t.TempDir(), "exports")
	for _, project := range []projectSummary{one, two} {
		first := exportProjectSessions(root, project)
		second := exportProjectSessions(root, project)
		if first.failed() || second.failed() || first.Success != 2 || second.Skipped != 2 {
			t.Fatalf("long project directory failed recovery: %+v %+v", first, second)
		}
	}
}

func TestV11ObjectSubagentSource(t *testing.T) {
	storage, project := projectFixture(t)
	path := filepath.Join(storage, "rollout-object-worker.jsonl")
	data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "synthetic-worker", "cwd": project.Root, "source": map[string]any{"subagent": map[string]any{"other": "synthetic"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	found := discoverSessions(storage, true)
	if found.Rollouts != 3 || found.Interactive != 2 || len(found.Issues) != 0 {
		t.Fatalf("object subagent source counted as user: %+v", found)
	}
}

func TestV11PendingIndexRecoveryAndWriteFailure(t *testing.T) {
	_, project := projectFixture(t)
	root := makeDirectory(t, filepath.Join(t.TempDir(), "exports"))
	index, path, dir, err := prepareBatchIndex(root, project)
	if err != nil {
		t.Fatal(err)
	}
	s := project.Sessions[0]
	content, _, err := renderSession(s, false)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := fileFingerprint(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	entry := batchEntry{File: "20261013_pending.md", Status: "pending", SessionKey: fingerprint(s.SessionID), SourceHash: sourceHash, OutputHash: fingerprint(content)}
	index.Entries[sessionSourceKey(s)] = entry
	if err := saveBatchIndex(path, index); err != nil {
		t.Fatal(err)
	}
	// Simulate a successful file write followed by an interrupted index update.
	if err := os.WriteFile(filepath.Join(dir, entry.File), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	r := exportProjectSessions(root, project)
	if r.Skipped != 1 || r.Success != 1 || r.failed() {
		t.Fatalf("pending recovery: %+v", r)
	}
	index, err = readBatchIndex(path, project.Key)
	if err != nil {
		t.Fatal(err)
	}
	entry = index.Entries[sessionSourceKey(s)]
	entry.Status = "pending"
	index.Entries[sessionSourceKey(s)] = entry
	if err := saveBatchIndex(path, index); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, entry.File), []byte("manual pending edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = exportProjectSessions(root, project)
	if r.WriteFailed != 1 || r.Skipped != 1 {
		t.Fatalf("pending mismatch was overwritten: %+v", r)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, entry.File)); string(got) != "manual pending edit" {
		t.Fatal("pending user edit lost")
	}
	// Removing a failed pending target allows a later retry on its reserved name.
	if err := os.Remove(filepath.Join(dir, entry.File)); err != nil {
		t.Fatal(err)
	}
	r = exportProjectSessions(root, project)
	if r.Success != 1 || r.Skipped != 1 || r.failed() {
		t.Fatalf("pending retry failed: %+v", r)
	}
}

func TestV11SameNamedProjectsAndForeignFiles(t *testing.T) {
	_, one := projectFixture(t)
	_, two := projectFixture(t)
	root := filepath.Join(t.TempDir(), "exports")
	a := exportProjectSessions(root, one)
	b := exportProjectSessions(root, two)
	if a.failed() || b.failed() || a.Directory == b.Directory || !strings.HasSuffix(b.Directory, two.Key[:12]) {
		t.Fatalf("project directory collision: %+v %+v", a, b)
	}
	// A manual Markdown not registered in an otherwise valid index is preserved.
	_, three := projectFixture(t)
	root2 := makeDirectory(t, filepath.Join(t.TempDir(), "exports"))
	index, _, dir, err := prepareBatchIndex(root2, three)
	if err != nil {
		t.Fatal(err)
	}
	_ = index
	name, err := filenameForDirectory(dir, three.Sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("manual file"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exportProjectSessions(root2, three)
	if c.Success != 2 || c.failed() {
		t.Fatalf("manual filename conflict: %+v", c)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != "manual file" {
		t.Fatal("foreign file overwritten")
	}
}

func TestV11IndexValidationAndLock(t *testing.T) {
	_, project := projectFixture(t)
	root := makeDirectory(t, filepath.Join(t.TempDir(), "exports"))
	index, path, _, err := prepareBatchIndex(root, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"../escape", ".codex-exporter", "CON"} {
		index.Directory = unsafe
		if err := saveBatchIndex(path, index); err != nil {
			t.Fatal(err)
		}
		if result := exportProjectSessions(root, project); result.IndexFailed == 0 {
			t.Fatal("unsafe index directory accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".codex-exporter", "batch.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if result := exportProjectSessions(root, project); result.IndexFailed != 1 {
		t.Fatal("existing batch lock ignored")
	}
	blocked := filepath.Join(t.TempDir(), "file-not-directory")
	if err := os.WriteFile(blocked, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := exportProjectSessions(blocked, project); result.WriteFailed != 1 {
		t.Fatal("unwritable output root not reported")
	}
}

func TestV11BatchCLIIsolationAndIncompleteScan(t *testing.T) {
	storage, project := projectFixture(t)
	syntheticSession(t, storage, "other-project", makeDirectory(t, filepath.Join(t.TempDir(), "other")), "vscode")
	syntheticSession(t, storage, "subagent", project.Root, "subagent")
	root := filepath.Join(t.TempDir(), "exports")
	opts := options{batch: true, sessionsDir: storage, project: project.Root, outputDir: root}
	if err := runBatchExport(opts); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, safeProjectName(project.Name), "*.md"))
	if len(files) != 2 {
		t.Fatal("wrong project/source exported")
	}
	if err := runBatchExport(opts); err != nil {
		t.Fatal(err)
	}
	files, _ = filepath.Glob(filepath.Join(root, safeProjectName(project.Name), "*.md"))
	if len(files) != 2 {
		t.Fatal("repeated CLI batch generated copies")
	}
	if err := os.WriteFile(filepath.Join(storage, "rollout-invalid.jsonl"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runBatchExport(opts); err == nil {
		t.Fatal("incomplete discovery reported complete success")
	}
}

func TestV11ProjectPicker(t *testing.T) {
	_, project := projectFixture(t)
	m := newProjectPickerModel([]projectSummary{project})
	// Full-path assertions need a viewport wide enough for this temporary path.
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 2*len([]rune(project.Root)) + 60, Height: 24})
	m = resized.(projectPickerModel)
	if view := m.View(); !strings.Contains(view, project.Root) || !strings.Contains(view, project.Name) || !strings.Contains(view, "2") {
		t.Fatal("project picker lacks identity/count")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(projectPickerModel).selected != 0 || cmd == nil {
		t.Fatal("project selection did not finish")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !updated.(projectPickerModel).cancelled {
		t.Fatal("project cancel failed")
	}
}

func TestV11PermissionErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows inaccessible-file behavior is covered by exclusive-handle tests")
	}
	_, project := projectFixture(t)
	root := makeDirectory(t, filepath.Join(t.TempDir(), "blocked"))
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if _, err := os.ReadDir(root); err == nil {
		t.Skip("current user bypasses file permissions")
	}
	if result := exportProjectSessions(root, project); result.WriteFailed != 1 {
		t.Fatal("output permission error hidden")
	}
	if _, err := findRolloutFiles(root); err == nil {
		t.Fatal("discovery permission error hidden")
	}
}
