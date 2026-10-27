package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func assertPickerFrameFits(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height-1 {
		t.Fatalf("frame has %d lines in %d-row console", len(lines), height)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w >= width {
			t.Fatalf("line %d width %d reaches/wraps %d-column console", i, w, width)
		}
	}
}

func TestSessionPickerIndicesAndFrames(t *testing.T) {
	var summaries []sessionSummary
	for i := 0; i < 75; i++ {
		summaries = append(summaries, sessionSummary{SessionID: fmt.Sprint(i), Path: fmt.Sprintf("synthetic-%d", i), Cwd: "synthetic/中文 project", Title: fmt.Sprintf("会话-%d", i)})
	}
	// Same metadata ID still represents two different physical sources.
	summaries[1].SessionID = summaries[0].SessionID
	for _, size := range [][2]int{{20, 8}, {40, 12}, {60, 20}, {80, 24}, {100, 30}, {120, 40}} {
		m := newSessionPickerModel(summaries, true, time.Now())
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(sessionPickerModel)
		if len(m.table.Rows()) != len(summaries) {
			t.Fatal("layout removed real sources")
		}
		for i, row := range m.table.Rows() {
			if row[0] != strconv.Itoa(i+1) {
				t.Fatalf("wrong index %d: %s", i, row[0])
			}
		}
		for i := 0; i < len(summaries); i++ {
			assertPickerFrameFits(t, m.View(), size[0], size[1])
			seen := map[string]bool{}
			for _, line := range strings.Split(ansi.Strip(m.table.View()), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 {
					continue
				}
				if _, err := strconv.Atoi(fields[0]); err != nil {
					continue
				}
				if seen[fields[0]] {
					t.Fatalf("rendered index %s twice in one frame", fields[0])
				}
				seen[fields[0]] = true
			}
			if !seen[strconv.Itoa(m.table.Cursor()+1)] {
				t.Fatalf("selected source %d is not visible after scrolling", m.table.Cursor()+1)
			}
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = updated.(sessionPickerModel)
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if updated.(sessionPickerModel).selected != len(summaries)-1 {
			t.Fatal("selection mapped to wrong source")
		}
	}
}

func TestPickerResizeAndFreshState(t *testing.T) {
	summaries := pickerTestSummaries(time.Now())
	for round := 0; round < 3; round++ {
		m := newSessionPickerModel(summaries, true, time.Now())
		if m.table.Cursor() != 0 || m.selected != -1 || m.cancelled {
			t.Fatal("new picker inherited menu/previous selection")
		}
		if m.Init() == nil || (menuModel{}).Init() == nil {
			t.Fatal("missing screen reset on program transition")
		}
		for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 10}, {100, 30}} {
			_, cmd := m.Update(pickerWindowSizeMsg{size[0], size[1]})
			if cmd == nil {
				t.Fatal("changed Windows console size did not notify renderer")
			}
			resize := cmd()
			event, ok := resize.(tea.WindowSizeMsg)
			if !ok {
				t.Fatal("not a renderer window-size event")
			}
			updated, _ := m.Update(event)
			m = updated.(sessionPickerModel)
			assertPickerFrameFits(t, m.View(), size[0], size[1])
		}
		m = applyPickerKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
		m = applyPickerKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	}
}

func TestPickerCellsCannotCreateExtraRows(t *testing.T) {
	summaries := []sessionSummary{{Title: "synthetic\n99 forged\x1b[41m\t标题", Cwd: "synthetic\r\npath"}}
	row := sessionPickerRows(summaries, true, time.Now())[0]
	for _, cell := range row {
		if strings.ContainsAny(cell, "\r\n\t\x1b") {
			t.Fatal("cell can inject terminal rows/styles")
		}
	}
	if summaries[0].Title != "synthetic\n99 forged\x1b[41m\t标题" {
		t.Fatal("source text changed")
	}
}

func TestSessionPickerHasOneHighlightedRow(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	var summaries []sessionSummary
	for i := 0; i < 60; i++ {
		summaries = append(summaries, sessionSummary{Title: fmt.Sprintf("synthetic %d", i)})
	}
	m := newSessionPickerModel(summaries, true, time.Now())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(sessionPickerModel)
	for i := 0; i < 60; i++ {
		count := 0
		for _, line := range strings.Split(m.table.View(), "\n") {
			if strings.Contains(line, "48;5;62") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("cursor %d has %d highlighted physical lines", m.table.Cursor(), count)
		}
		assertPickerFrameFits(t, m.View(), 80, 24)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(sessionPickerModel)
	}
}

func TestProjectPickerFitsConsole(t *testing.T) {
	_, project := projectFixture(t)
	for _, size := range [][2]int{{20, 8}, {40, 12}, {80, 24}, {120, 40}} {
		m := newProjectPickerModel([]projectSummary{project})
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(projectPickerModel)
		assertPickerFrameFits(t, m.View(), size[0], size[1])
		if len(m.projects[0].Sessions) != 2 {
			t.Fatal("layout changed project membership")
		}
	}
}

func TestDiscoveryHistoricalRecordsAndCwdIndependence(t *testing.T) {
	storage, project := projectFixture(t)
	missingCwd := syntheticSession(t, storage, "synthetic-no-cwd", "", "vscode")
	historical := syntheticSession(t, storage, "synthetic-historical", filepath.Join(t.TempDir(), "deleted project"), "cli")
	syntheticSession(t, storage, "synthetic-subagent", project.Root, "subagent")
	archives := makeDirectory(t, filepath.Join(filepath.Dir(storage), "archived_sessions"))
	syntheticSession(t, archives, "synthetic-archive", project.Root, "vscode")
	first := discoverSessions(storage, true)
	t.Chdir(t.TempDir())
	second := discoverSessions(storage, true)
	if first.Rollouts != 6 || second.Rollouts != 6 || first.Interactive != 5 || second.Interactive != 5 || len(second.Issues) != 0 {
		t.Fatalf("unexpected discovery counts %d %d", second.Rollouts, second.Interactive)
	}
	for i, s := range first.Sessions {
		if sessionSourceKey(s) != sessionSourceKey(second.Sessions[i]) {
			t.Fatal("cwd changed global discovery")
		}
	}
	projects, issues := groupSessionsByProject(second.Sessions)
	assigned := 0
	foundHistorical := false
	for _, p := range projects {
		assigned += len(p.Sessions)
		for _, s := range p.Sessions {
			if s.SessionID == missingCwd.SessionID {
				t.Fatal("guessed a missing cwd")
			}
			if s.SessionID == historical.SessionID {
				foundHistorical = p.Historical
			}
		}
	}
	if assigned != 4 || len(issues) != 1 || !foundHistorical {
		t.Fatal("history was lost or missing cwd silently guessed")
	}
	// Missing cwd remains selectable in global single export, even though it
	// cannot be honestly assigned to a project.
	all, err := listSessions(storage, "unrelated", true)
	if err != nil || len(all) != 5 {
		t.Fatal("single discovery filtered historical user sessions")
	}
	for _, s := range second.Sessions {
		if s.Source == "subagent" {
			t.Fatal("subagent included")
		}
	}
}
