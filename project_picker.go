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
	projects      []projectSummary
	table         table.Model
	selected      int
	cancelled     bool
	width, height int
}

func newProjectPickerModel(projects []projectSummary) projectPickerModel {
	rows := make([]table.Row, len(projects))
	for i, p := range projects {
		name := p.Name
		if p.Historical {
			name += " (historical cwd)"
		}
		rows[i] = table.Row{pickerCellText(name), pickerCellText(p.Root), fmt.Sprint(len(p.Sessions))}
	}
	m := projectPickerModel{projects: projects, selected: -1, width: 80, height: 24}
	m.table = table.New(table.WithColumns([]table.Column{{Title: "Project", Width: 28}, {Title: "Full path", Width: 76}, {Title: "Sessions", Width: 10}}), table.WithRows(rows), table.WithFocused(true), table.WithHeight(16))
	m.table.SetStyles(newSessionPickerTableStyles())
	m.updateLayout()
	return m
}

func (m *projectPickerModel) updateLayout() {
	width := pickerContentWidth(m.width)
	name, count := 24, 8
	if width < 80 {
		name, count = 16, 5
	}
	if width < 40 {
		name, count = 8, 3
	}
	m.table.SetColumns([]table.Column{{Title: "Project", Width: name}, {Title: "Full path", Width: max(1, width-name-count-6)}, {Title: "Sessions", Width: count}})
	m.table.SetWidth(width)
	m.table.SetHeight(pickerTableHeight(m.height))
}

func (m projectPickerModel) Init() tea.Cmd { return tea.ClearScreen }
func (m projectPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key := msg.(type) {
	case pickerWindowSizeMsg:
		return m, pickerResizeCommand(key, m.width, m.height)
	case tea.WindowSizeMsg:
		changed := m.width != key.Width || m.height != key.Height
		m.width, m.height = key.Width, key.Height
		m.updateLayout()
		if changed {
			return m, tea.Batch(tea.ClearScreen, pollPickerWindow(m.width, m.height))
		}
		return m, pollPickerWindow(m.width, m.height)
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
	if m.width < 20 || m.height < 7 {
		return fitPickerView("请扩大窗口 / Esc 退出", m.width, m.height)
	}
	selectedPath := ""
	if len(m.projects) > 0 {
		selectedPath = pickerCellText(m.projects[m.table.Cursor()].Root)
	}
	return fitPickerView(strings.Join([]string{pickerTitleStyle.Render("codex-exporter | Project Batch Export"), m.table.View(), "Selected project: " + selectedPath, pickerHelpStyle.Render("up/down: move | enter: export this project's sessions | q/esc: cancel")}, "\n"), m.width, m.height)
}

func runProjectPicker(projects []projectSummary, factory teaProgramFactory, programOpts ...tea.ProgramOption) (int, bool, error) {
	if factory == nil {
		factory = tea.NewProgram
	}
	initial := newProjectPickerModel(projects)
	initial.width, initial.height = pickerInitialSize()
	initial.updateLayout()
	model, err := factory(initial, append([]tea.ProgramOption{tea.WithAltScreen()}, programOpts...)...).Run()
	if err != nil {
		return -1, false, fmt.Errorf("run project picker: %w", err)
	}
	m, ok := model.(projectPickerModel)
	if !ok {
		return -1, false, errors.New("unexpected project picker model")
	}
	return m.selected, m.cancelled || m.selected < 0, nil
}
