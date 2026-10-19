package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPastedTextWindowsReadFailure(t *testing.T) {
	root, path := attachmentFixture(t)
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// No sharing: opening the existing regular file for reading must fail.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	if got := resolvePastedTextAttachments(pastedWrapper(path), root); !strings.HasPrefix(got, pastedTextUnavailable) || strings.Contains(got, path) {
		t.Fatalf("expected safe fallback, got %q", got)
	}
}

func TestPastedTextWindowsJunctionEscape(t *testing.T) {
	root, _ := attachmentFixture(t)
	outside := writeAttachment(t, t.TempDir(), testAttachmentID, "Pasted text.txt", "PRIVATE OUTSIDE")
	link := filepath.Join(root, secondAttachmentID)
	// Junction creation does not require Windows' symbolic-link privilege. Pass
	// paths through the environment so no path text becomes PowerShell code.
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"New-Item -ItemType Junction -Path $env:CODEX_EXPORTER_TEST_LINK -Target $env:CODEX_EXPORTER_TEST_TARGET -ErrorAction Stop | Out-Null")
	cmd.Env = append(os.Environ(), "CODEX_EXPORTER_TEST_LINK="+link, "CODEX_EXPORTER_TEST_TARGET="+filepath.Dir(outside))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	path := filepath.Join(link, "Pasted text.txt")
	if _, err := readPastedText(root, path); err == nil {
		t.Fatal("junction escape accepted")
	}
	got := resolvePastedTextAttachments(pastedWrapper(path), root)
	if got != pastedTextUnavailable+"路径越界或包含不允许的路径跳转，已拒绝读取]\n\n" && got != pastedTextUnavailable+"无法安全解析附件路径（可能涉及链接或重解析点）]\n\n" {
		t.Fatalf("expected safe fallback, got %q", got)
	}
}

func TestV11WindowsBatchSourceFailure(t *testing.T) {
	_, project := projectFixture(t)
	name, err := syscall.UTF16PtrFromString(project.Sessions[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	r := exportProjectSessions(filepath.Join(t.TempDir(), "exports"), project)
	if r.ReadFailed != 1 || r.Success != 1 {
		t.Fatalf("Windows locked source stopped the batch: %+v", r)
	}
}

func TestLockedSettingsPreserveDefault(t *testing.T) {
	home := isolatedHome(t)
	one := filepath.Join(home, "first choice")
	if err := saveOutputRoot(one); err != nil {
		t.Fatal(err)
	}
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = syscall.CloseHandle(handle)
		}
	}()
	if err := saveOutputRoot(filepath.Join(home, "second choice")); err == nil {
		t.Fatal("locked settings update did not report failure")
	}
	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	closed = true
	if got, err := savedOutputRoot(); err != nil || got != one {
		t.Fatal("failed settings update lost previous choice")
	}
}
