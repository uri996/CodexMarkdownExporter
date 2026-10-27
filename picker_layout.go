package main

import (
	"os"
	"runtime"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Tables must include cell padding in their width budget. Leave the last
// console column unused: writing a styled cell there can trigger auto-wrap.
func pickerContentWidth(width int) int { return max(1, width-1) }

func pickerCellText(text string) string {
	text = ansi.Strip(text)
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func fitPickerView(text string, width, height int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > max(1, height-1) {
		lines = lines[:max(1, height-1)]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, pickerContentWidth(width), "")
	}
	return strings.Join(lines, "\n")
}

func pickerInitialSize() (int, int) {
	if w, h, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

type pickerWindowSizeMsg struct{ width, height int }

// Bubble Tea v1 on Windows receives the initial size but has no SIGWINCH.
// Poll only while a picker is active, and send a renderer resize only on change.
func pollPickerWindow(width, height int) tea.Cmd {
	if runtime.GOOS != "windows" {
		return nil
	}
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		w, h, err := term.GetSize(os.Stdout.Fd())
		if err != nil || w < 1 || h < 1 {
			w, h = width, height
		}
		return pickerWindowSizeMsg{w, h}
	})
}

func pickerResizeCommand(msg pickerWindowSizeMsg, width, height int) tea.Cmd {
	if msg.width == width && msg.height == height {
		return pollPickerWindow(width, height)
	}
	return func() tea.Msg { return tea.WindowSizeMsg{Width: msg.width, Height: msg.height} }
}
