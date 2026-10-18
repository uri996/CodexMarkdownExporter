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
