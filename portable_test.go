package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	tea "github.com/charmbracelet/bubbletea"
)

func keyMessage(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
	}
}

func TestPortableMenuActions(t *testing.T) {
	for choice := 0; choice < 4; choice++ {
		var model tea.Model = menuModel{choice: -1}
		for i := 0; i < choice; i++ {
			model, _ = model.Update(keyMessage("down"))
		}
		model, cmd := model.Update(keyMessage("enter"))
		if model.(menuModel).choice != choice || cmd == nil {
			t.Fatalf("menu choice %d failed", choice)
		}
	}
	m, cmd := (menuModel{choice: -1}).Update(keyMessage("esc"))
	if m.(menuModel).choice != 3 || cmd == nil {
		t.Fatal("menu cannot exit")
	}
	model, err := tea.NewProgram(menuModel{choice: -1}, tea.WithInput(strings.NewReader("\x1b[B\r")), tea.WithOutput(&bytes.Buffer{}), tea.WithoutRenderer()).Run()
	if err != nil || model.(menuModel).choice != 1 {
		t.Fatalf("terminal key handling: %v", err)
	}
	if !strings.Contains((menuModel{}).View(), "设置默认保存目录") {
		t.Fatal("missing real settings entry")
	}
}

func TestPortableSettingsConfirmationAndCancel(t *testing.T) {
	home := isolatedHome(t)
	first := filepath.Join(home, "first")
	if err := saveOutputRoot(first); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(home, "中文 new directory")
	model := newSettingsModel()
	if !strings.Contains(model.View(), first) {
		t.Fatal("current directory not shown")
	}
	var m tea.Model = model
	m, _ = m.Update(keyMessage("  \"" + second + "\"  "))
	m, _ = m.Update(keyMessage("enter"))
	if !m.(settingsModel).confirm {
		t.Fatal("missing confirmation")
	}
	m, _ = m.Update(keyMessage("esc"))
	if got, _ := savedOutputRoot(); got != first {
		t.Fatal("cancel changed settings")
	}
	m = newSettingsModel()
	m, _ = m.Update(keyMessage("\"" + second + "\""))
	m, _ = m.Update(keyMessage("enter"))
	m, _ = m.Update(keyMessage("y"))
	if !m.(settingsModel).done {
		t.Fatal(m.(settingsModel).notice)
	}
	if got, _ := savedOutputRoot(); got != second {
		t.Fatal("new choice not saved")
	}
	_, cmd := m.Update(keyMessage("enter"))
	if cmd == nil {
		t.Fatal("settings cannot return")
	}
	if _, err := cleanDirectoryInput("relative/path"); err == nil {
		t.Fatal("relative settings path accepted")
	}
	blocked := filepath.Join(home, "not a directory")
	if err := os.WriteFile(blocked, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveUsableOutputRoot(blocked); err == nil {
		t.Fatal("non-directory accepted")
	}
	if got, _ := savedOutputRoot(); got != second {
		t.Fatal("invalid choice changed default")
	}
}

func TestPortableFirstSettingsAndEditing(t *testing.T) {
	isolatedHome(t)
	m := newSettingsModel()
	if !strings.Contains(m.View(), "尚未设置") {
		t.Fatal("first-use explanation missing")
	}
	v, _ := m.Update(keyMessage("中文ab"))
	m = v.(settingsModel)
	v, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = v.(settingsModel)
	v, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = v.(settingsModel)
	if m.input != "中文b" {
		t.Fatalf("Unicode editing: %q", m.input)
	}
	v, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = v.(settingsModel)
	if m.input != "" {
		t.Fatal("clear did not work")
	}
	_, _ = m.Update(keyMessage("esc"))
	if _, err := savedOutputRoot(); !errors.Is(err, errNoOutputDirectory) {
		t.Fatal("cancel created setting")
	}
}

func TestPortableRoutingAndCompatibility(t *testing.T) {
	for _, args := range [][]string{
		{"-single"}, {"-batch"}, {"-batch", "-project", "synthetic"}, {"-all"}, {"-last"}, {"-session-id", "synthetic"}, {"-output", "chosen.md"}, {"-output-dir", "chosen"}, {"-set-output-dir", "chosen"}, {"-force"}, {"-full"}, {"-version"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			oldFlags, oldArgs := flag.CommandLine, os.Args
			t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
			flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
			os.Args = append([]string{"test"}, args...)
			opts := parseFlags()
			if err := validateOptions(opts); err != nil {
				t.Fatal(err)
			}
			if opts.single && !opts.showAll {
				t.Fatal("-single must ignore launch cwd")
			}
			if args[0] == "-last" && !opts.last {
				t.Fatal("latest-session meaning lost")
			}
		})
	}
	for _, opts := range []options{
		{single: true, batch: true}, {single: true, last: true}, {single: true, sessionID: "synthetic"}, {single: true, setOutputDir: "chosen"}, {explicit: map[string]bool{"version": true, "batch": true}},
	} {
		if validateOptions(opts) == nil {
			t.Fatal("conflicting flags accepted")
		}
	}
}

func TestPortableArbitraryLaunchDirectory(t *testing.T) {
	home := isolatedHome(t)
	storage, project := projectFixture(t)
	t.Setenv("CODEX_HOME", filepath.Dir(storage))
	t.Chdir(t.TempDir()) // unrelated to source, executable, or session project
	root := filepath.Join(home, "chosen exports")
	if err := saveUsableOutputRoot(root); err != nil {
		t.Fatal(err)
	}
	opts := portableOptions(0, root, &portableReport{})
	found, err := listSessions(expandHome(opts.sessionsDir), "unrelated", opts.showAll)
	if err != nil || len(found) != 2 {
		t.Fatalf("portable single discovery: %d %v", len(found), err)
	}
	// Skip only the UI: invoke the same existing single and batch entry points.
	opts.last = true
	if err := runSingleExport(opts); err != nil {
		t.Fatal(err)
	}
	if opts.report.File == "" || opts.report.Directory != root {
		t.Fatal("single result lost actual output")
	}
	single, err := os.ReadFile(filepath.Join(root, opts.report.File))
	if err != nil {
		t.Fatal(err)
	}
	opts = portableOptions(1, root, &portableReport{})
	opts.project = project.Root
	if err := runBatchExport(opts); err != nil {
		t.Fatal(err)
	}
	if opts.report.Batch == nil || opts.report.Batch.Success != 2 {
		t.Fatal("batch result lost")
	}
	files, _ := filepath.Glob(filepath.Join(opts.report.Directory, "*.md"))
	if len(files) != 2 {
		t.Fatal("wrong batch file count")
	}
	batch, _ := os.ReadFile(files[0])
	if !bytes.Equal(single, batch) {
		t.Fatal("single/batch content changed")
	}
	if err := runBatchExport(opts); err != nil {
		t.Fatal(err)
	}
	if opts.report.Batch.Success != 0 || opts.report.Batch.Skipped != 2 {
		t.Fatal("repeat export regressed")
	}
	if files, _ := filepath.Glob("*.md"); len(files) != 0 {
		t.Fatal("exports leaked to launch directory")
	}
	t.Setenv("CODEX_HOME", "")
	if sessionStorageDirectory() != defaultSessionsDir {
		t.Fatal("default storage changed")
	}
}

func TestPortableResultAndOpenDirectory(t *testing.T) {
	dir := makeDirectory(t, filepath.Join(t.TempDir(), "中文 folder & special"))
	m := newResultModel(portableReport{File: "synthetic.md", Directory: dir, Messages: 2}, nil)
	if !strings.Contains(m.text, "synthetic.md") || !strings.Contains(m.text, dir) {
		t.Fatal("wrong result location")
	}
	_, cmd := m.Update(keyMessage("enter"))
	if cmd == nil {
		t.Fatal("cannot return to menu")
	}
	v, cmd := m.Update(keyMessage("esc"))
	if cmd == nil || !v.(resultModel).exit {
		t.Fatal("cannot exit")
	}
	_, cmd = m.Update(keyMessage("o"))
	if cmd == nil {
		t.Fatal("no explicit open action")
	} // do not launch Explorer in tests
	command, err := outputDirectoryCommand(dir)
	if runtime.GOOS == "windows" && (err != nil || len(command.Args) != 2 || command.Args[1] != dir) {
		t.Fatal("unsafe/lost directory argument")
	}
	if _, err := outputDirectoryCommand(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing directory accepted")
	}
	if _, err := outputDirectoryCommand(""); err == nil {
		t.Fatal("empty output accepted")
	}
	b := batchResult{Success: 1, Skipped: 2, ReadFailed: 1, Selected: 4, Errors: []string{"read: synthetic failure"}}
	m = newResultModel(portableReport{Batch: &b, Directory: dir, Issues: 1}, errors.New("incomplete"))
	for _, text := range []string{"未全部完成", "跳过：2", "读取失败：1", "扫描/归属问题：1", "synthetic failure"} {
		if !strings.Contains(m.text, text) {
			t.Fatalf("missing %s", text)
		}
	}
	v, _ = m.Update(folderOpenedMsg{errors.New("synthetic failure")})
	if !strings.Contains(v.(resultModel).notice, "无法打开") {
		t.Fatal("open failure hidden")
	}
}

// Real executable CLI behavior without requiring a terminal or reading private data.
func TestPortableCLIProcess(t *testing.T) {
	if os.Getenv("CODEX_EXPORTER_CLI_TEST") == "1" {
		separator := 0
		for i, arg := range os.Args {
			if arg == "--" {
				separator = i
				break
			}
		}
		os.Args = append([]string{"Codex-Exporter"}, os.Args[separator+1:]...)
		flag.CommandLine = flag.NewFlagSet("Codex-Exporter", flag.ExitOnError)
		if err := run(); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	isolatedHome(t)
	for _, item := range []struct {
		args []string
		want string
		fail bool
	}{
		{[]string{"-version"}, "Codex Exporter " + version, false},
		{[]string{"-help"}, "中文主菜单", false},
		{nil, "交互式终端", true},
		{[]string{"-single", "-batch"}, "cannot combine", true},
		{[]string{"-batch", "-output", "synthetic.md"}, "single-export", true},
	} {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestPortableCLIProcess$", "--"}, item.args...)...)
		cmd.Env = append(os.Environ(), "CODEX_EXPORTER_CLI_TEST=1")
		out, err := cmd.CombinedOutput()
		if (err != nil) != item.fail || !strings.Contains(string(out), item.want) {
			t.Fatalf("%v: %v %s", item.args, err, out)
		}
	}
}

func TestWindowsPortablePackage(t *testing.T) {
	path := filepath.Join("dist", "Codex-Exporter-"+version+"-windows-amd64.zip")
	archive, err := zip.OpenReader(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("run scripts/build-windows.ps1 before package acceptance")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 3 {
		t.Fatal("ZIP must contain exactly three allowlisted files")
	}
	want := map[string]bool{"Codex-Exporter.exe": true, "README.txt": true, "LICENSE": true}
	extracted := t.TempDir()
	for _, f := range archive.File {
		if !want[f.Name] {
			t.Fatalf("unexpected/private ZIP entry: %s", f.Name)
		}
		delete(want, f.Name)
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		workingDirectory, _ := os.Getwd()
		userHome, _ := os.UserHomeDir()
		for _, private := range []string{workingDirectory, userHome} {
			if private == "" || private == filepath.VolumeName(private)+string(filepath.Separator) {
				continue
			}
			for _, spelling := range []string{private, filepath.ToSlash(private)} {
				var wide []byte
				for _, unit := range utf16.Encode([]rune(spelling)) {
					wide = append(wide, byte(unit), byte(unit>>8))
				}
				if bytes.Contains(data, []byte(spelling)) || bytes.Contains(data, wide) {
					t.Fatal("local build/home path leaked into ZIP entry", f.Name)
				}
			}
		}
		if err := os.WriteFile(filepath.Join(extracted, f.Name), data, 0o700); err != nil {
			t.Fatal(err)
		}
		if f.Name == "LICENSE" {
			license, _ := os.ReadFile("LICENSE")
			if !bytes.Equal(data, license) {
				t.Fatal("license changed in package")
			}
		}
		if f.Name == "README.txt" && !bytes.Contains(data, []byte("无需安装 Go")) {
			t.Fatal("missing Chinese portable instructions")
		}
		if f.Name == "README.txt" {
			instructions, err := os.ReadFile("docs/portable-README.txt")
			if err != nil || !bytes.HasPrefix(data, instructions) {
				t.Fatal("packaged instructions differ from current source")
			}
			notices, err := os.ReadFile("THIRD_PARTY_NOTICES.txt")
			if err != nil || !bytes.HasSuffix(data, notices) {
				t.Fatal("packaged dependency notices differ from current source")
			}
			for _, notice := range []string{"github.com/klauspost/compress v1.18.4", "Copyright (c) 2019 Klaus Post", "Copyright (c) 2016 Caleb Spare"} {
				if !bytes.Contains(data, []byte(notice)) {
					t.Fatal("missing redistributed dependency license", notice)
				}
			}
		}
	}
	if len(want) != 0 {
		t.Fatal("missing ZIP files")
	}
	exe := filepath.Join(extracted, "Codex-Exporter.exe")
	binary, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	if binary.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		t.Fatal("not Windows amd64")
	}
	header, ok := binary.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || header.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
		t.Fatal("must be a console executable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	checksum, err := os.ReadFile(path + ".sha256")
	if err != nil || !strings.HasPrefix(string(checksum), fmt.Sprintf("%x", hash)) {
		t.Fatal("ZIP checksum mismatch")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows execution requires Windows; ZIP/PE validated")
	}
	home := isolatedHome(t)
	storage, project := projectFixture(t)
	t.Setenv("CODEX_HOME", filepath.Dir(storage))
	t.Setenv("PATH", extracted) // Go and sqlite3 are deliberately unavailable.
	// The independent EXE must include zstd itself, with no external decoder.
	compressSyntheticSession(t, project.Sessions[0], true)
	launchDirectory := t.TempDir()
	invoke := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(exe, args...)
		cmd.Dir = launchDirectory
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("extracted EXE %v: %v %s", args, err, out)
		}
		return string(out)
	}
	if !strings.Contains(invoke("-version"), version) {
		t.Fatal("wrong packaged version")
	}
	if !strings.Contains(invoke("-help"), "中文主菜单") {
		t.Fatal("wrong packaged help")
	}
	root := filepath.Join(home, "独立便携验收")
	invoke("-set-output-dir", root)
	invoke("-all", "-last")
	files, _ := filepath.Glob(filepath.Join(root, "*.md"))
	if len(files) != 1 {
		t.Fatal("detached EXE cannot use saved root")
	}
	invoke("-batch", "-project", project.Root)
	output := invoke("-batch", "-project", project.Root)
	if !strings.Contains(output, "Existing skipped: 2") {
		t.Fatal("packaged batch skip failed")
	}
	if files, _ := filepath.Glob(filepath.Join(extracted, "*.md")); len(files) != 0 {
		t.Fatal("EXE exported beside itself")
	}
	// Exercise the real portable build marker, with no Go or sqlite on PATH.
	invoke("-set-output-dir", "exports")
	invoke("-all", "-last")
	portableRoot := filepath.Join(extracted, "exports")
	if files, _ := filepath.Glob(filepath.Join(portableRoot, "*.md")); len(files) != 1 {
		t.Fatal("portable relative setting is not anchored to EXE")
	}
	invoke("-batch", "-project", project.Root)
	if output := invoke("-batch", "-project", project.Root); !strings.Contains(output, "Existing skipped: 2") {
		t.Fatal("relative-root batch repeat failed")
	}
	invoke("-all", "-last", "-output-dir", "临时 output")
	if files, _ := filepath.Glob(filepath.Join(extracted, "临时 output", "*.md")); len(files) != 1 {
		t.Fatal("portable temporary relative directory uses launch cwd")
	}
	settingsFile, _ := settingsPath()
	settings, _ := os.ReadFile(settingsFile)
	if !bytes.Contains(settings, []byte(`"output_directory":"exports"`)) {
		t.Fatal("temporary override changed saved default")
	}
	if entries, _ := os.ReadDir(launchDirectory); len(entries) != 0 {
		t.Fatal("portable outputs or configuration leaked into launch directory")
	}
}
