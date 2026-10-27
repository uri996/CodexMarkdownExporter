package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func syntheticPortable(t *testing.T) string {
	t.Helper()
	oldBuild, oldExecutable := portableBuild, executableLocation
	t.Cleanup(func() { portableBuild, executableLocation = oldBuild, oldExecutable })
	portableBuild = "true"
	dir := filepath.Join(t.TempDir(), "中文 portable folder")
	executableLocation = func() (string, error) { return filepath.Join(dir, "Codex-Exporter.exe"), nil }
	return dir
}

func TestPortableRelativeDirectoryAndSelection(t *testing.T) {
	isolatedHome(t)
	base := syntheticPortable(t)
	t.Chdir(t.TempDir())
	want := filepath.Join(base, "exports")
	if candidate, err := portableOutputCandidate(); err != nil || candidate != want {
		t.Fatal("candidate must be relative to EXE", err)
	}
	if _, err := savedOutputRoot(); !errors.Is(err, errNoOutputDirectory) {
		t.Fatal("candidate was saved without selection")
	}
	if _, err := os.Stat(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate was created without selection")
	}
	custom := filepath.Join(t.TempDir(), "custom")
	if err := saveOutputRoot(custom); err != nil {
		t.Fatal(err)
	}
	var model tea.Model = newSettingsModel()
	if got, _ := savedOutputRoot(); got != custom {
		t.Fatal("custom directory replaced")
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model, _ = model.Update(keyMessage("enter"))
	if !model.(settingsModel).confirm || !strings.Contains(model.(settingsModel).notice, want) {
		t.Fatal("selection not confirmed with resolved destination")
	}
	if got, _ := savedOutputRoot(); got != custom {
		t.Fatal("unconfirmed selection saved")
	}
	model, _ = model.Update(keyMessage("y"))
	if !model.(settingsModel).done {
		t.Fatal(model.(settingsModel).notice)
	}
	if got, err := savedOutputRoot(); err != nil || got != want {
		t.Fatal("portable selection not saved", err)
	}
	path, _ := settingsPath()
	settings, _ := os.ReadFile(path)
	if bytes.Contains(settings, []byte(base)) || !bytes.Contains(settings, []byte(`"portable_relative":true`)) {
		t.Fatal("portable setting stores an absolute developer location")
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatal("missing directory not created", err)
	}
	before := append([]byte(nil), settings...)
	if got, err := resolveOutputRoot("临时 output"); err != nil || got != filepath.Join(base, "临时 output") {
		t.Fatal("relative override uses shell cwd", err)
	}
	settings, _ = os.ReadFile(path)
	if !bytes.Equal(before, settings) {
		t.Fatal("temporary override changed settings")
	}
	for _, input := range []string{"../escape", "../../escape"} {
		if _, err := resolveOutputRoot(input); err == nil {
			t.Fatal("escaping relative output accepted")
		}
	}
	moved := t.TempDir()
	executableLocation = func() (string, error) { return filepath.Join(moved, "Codex-Exporter.exe"), nil }
	if got, _ := savedOutputRoot(); got != filepath.Join(moved, "exports") {
		t.Fatal("relative setting did not follow EXE")
	}
	portableBuild = "false"
	if _, err := savedOutputRoot(); err == nil {
		t.Fatal("source run interpreted portable setting using temporary EXE")
	}
}

func TestSourceHasNoPortableDefault(t *testing.T) {
	isolatedHome(t)
	if portableBuild != "false" {
		t.Fatal("source test unexpectedly portable")
	}
	if _, err := portableOutputCandidate(); err == nil {
		t.Fatal("source build offered temporary EXE directory")
	}
	if _, err := chooseOutputRoot(strings.NewReader(""), &bytes.Buffer{}, false); !errors.Is(err, errNoOutputDirectory) {
		t.Fatal("source build fell back to an implicit directory", err)
	}
	var model tea.Model = newSettingsModel()
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	if model.(settingsModel).input != "" {
		t.Fatal("source settings selected portable directory")
	}
}

func TestPortableUnwritableDestinationPreservesSettings(t *testing.T) {
	isolatedHome(t)
	base := syntheticPortable(t)
	custom := t.TempDir()
	if err := saveOutputRoot(custom); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "exports"), []byte("synthetic blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveUsableOutputRoot("exports"); err == nil {
		t.Fatal("unwritable target accepted")
	}
	if got, _ := savedOutputRoot(); got != custom {
		t.Fatal("failed setting replaced current directory")
	}
}
