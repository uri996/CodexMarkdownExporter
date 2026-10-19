package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// Separate selection model: the Session Picker is never called by batch mode.
type projectPickerModel struct {
	projects  []projectSummary
	table     table.Model
	selected  int
	cancelled bool
}

func newProjectPickerModel(projects []projectSummary) projectPickerModel {
	rows := make([]table.Row, len(projects))
	for i, p := range projects {
		name := p.Name
		if p.Historical {
			name += " (historical cwd)"
		}
		rows[i] = table.Row{name, p.Root, fmt.Sprint(len(p.Sessions))}
	}
	m := projectPickerModel{projects: projects, selected: -1}
	m.table = table.New(table.WithColumns([]table.Column{{Title: "Project", Width: 28}, {Title: "Full path", Width: 76}, {Title: "Sessions", Width: 10}}), table.WithRows(rows), table.WithFocused(true), table.WithHeight(16))
	m.table.SetStyles(newSessionPickerTableStyles())
	return m
}

func (m projectPickerModel) Init() tea.Cmd { return nil }
func (m projectPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key := msg.(type) {
	case tea.WindowSizeMsg:
		m.table.SetHeight(pickerTableHeight(key.Height))
		m.table.SetColumns([]table.Column{{Title: "Project", Width: 24}, {Title: "Full path", Width: pickerMaxInt(20, key.Width-42)}, {Title: "Sessions", Width: 10}})
	case tea.KeyMsg:
		switch key.String() {
		case "q", "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			if len(m.projects) > 0 {
				m.selected = m.table.Cursor()
			}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m projectPickerModel) View() string {
	selectedPath := ""
	if len(m.projects) > 0 {
		selectedPath = m.projects[m.table.Cursor()].Root
	}
	return strings.Join([]string{pickerTitleStyle.Render("codex-exporter | Project Batch Export"), m.table.View(), "Selected project: " + selectedPath, pickerHelpStyle.Render("up/down: move | enter: export this project's sessions | q/esc: cancel")}, "\n")
}

func runProjectPicker(projects []projectSummary, factory teaProgramFactory, programOpts ...tea.ProgramOption) (int, bool, error) {
	if factory == nil {
		factory = tea.NewProgram
	}
	model, err := factory(newProjectPickerModel(projects), append([]tea.ProgramOption{tea.WithAltScreen()}, programOpts...)...).Run()
	if err != nil {
		return -1, false, fmt.Errorf("run project picker: %w", err)
	}
	m, ok := model.(projectPickerModel)
	if !ok {
		return -1, false, errors.New("unexpected project picker model")
	}
	return m.selected, m.cancelled || m.selected < 0, nil
}
