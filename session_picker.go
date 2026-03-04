package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type teaProgramFactory func(model tea.Model, opts ...tea.ProgramOption) *tea.Program

type sessionPickerModel struct {
	summaries   []sessionSummary
	showAll     bool
	now         time.Time
	table       table.Model
	width       int
	height      int
	tableHeight int
	selected    int
	cancelled   bool
	terminated  bool
}

var (
	pickerTitleStyle = lipgloss.NewStyle().Bold(true)
	pickerMetaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pickerHelpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func newSessionPickerModel(summaries []sessionSummary, showAll bool, now time.Time) sessionPickerModel {
	if now.IsZero() {
		now = time.Now()
	}
	model := sessionPickerModel{
		summaries: summaries,
		showAll:   showAll,
		now:       now,
		selected:  -1,
		width:     120,
		height:    24,
	}

	model.table = table.New(
		table.WithColumns(sessionPickerColumns(showAll, model.width)),
		table.WithRows(sessionPickerRows(summaries, showAll, now)),
		table.WithFocused(true),
	)
	model.table.SetStyles(newSessionPickerTableStyles())
	model.updateTableLayout()
	return model
}

func newSessionPickerTableStyles() table.Styles {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true)
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	return styles
}

func sessionPickerColumns(showAll bool, width int) []table.Column {
	if width <= 0 {
		width = 120
	}
	if showAll {
		const fixedWidth = 6 + 12 + 12 + 28
		titleWidth := pickerMaxInt(20, width-fixedWidth)
		return []table.Column{
			{Title: "Index", Width: 6},
			{Title: "Updated", Width: 12},
			{Title: "Created", Width: 12},
			{Title: "CWD", Width: 28},
			{Title: "Title", Width: titleWidth},
		}
	}
	const fixedWidth = 6 + 12 + 12
	titleWidth := pickerMaxInt(24, width-fixedWidth)
	return []table.Column{
		{Title: "Index", Width: 6},
		{Title: "Updated", Width: 12},
		{Title: "Created", Width: 12},
		{Title: "Title", Width: titleWidth},
	}
}

func sessionPickerRows(summaries []sessionSummary, showAll bool, now time.Time) []table.Row {
	rows := make([]table.Row, 0, len(summaries))
	titleLimit := 120
	if showAll {
		titleLimit = 100
	}
	for i, summary := range summaries {
		row := table.Row{
			fmt.Sprintf("%d", i+1),
			relativeTime(summary.UpdatedAt, now),
			relativeTime(summary.CreatedAt, now),
		}
		if showAll {
			row = append(row, truncateString(summary.Cwd, 28))
		}
		row = append(row, truncateString(displayTitle(summary.Title), titleLimit))
		rows = append(rows, row)
	}
	return rows
}

func (m *sessionPickerModel) updateTableLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.tableHeight = pickerTableHeight(m.height)
	m.table.SetHeight(m.tableHeight)
	m.table.SetColumns(sessionPickerColumns(m.showAll, m.width))
}

func pickerTableHeight(height int) int {
	return pickerMaxInt(5, height-8)
}

func pickerMaxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func (m *sessionPickerModel) moveCursor(offset int) {
	if len(m.summaries) == 0 {
		return
	}
	next := m.table.Cursor() + offset
	if next < 0 {
		next = 0
	}
	maxCursor := len(m.summaries) - 1
	if next > maxCursor {
		next = maxCursor
	}
	m.table.SetCursor(next)
}

func (m sessionPickerModel) Init() tea.Cmd {
	return nil
}

func (m sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.updateTableLayout()
		return m, nil
	case tea.KeyMsg:
		switch typed.String() {
		case "q", "esc", "ctrl+c":
			m.cancelled = true
			m.terminated = true
			return m, tea.Quit
		case "up", "k":
			m.moveCursor(-1)
			return m, nil
		case "down", "j":
			m.moveCursor(1)
			return m, nil
		case "enter":
			if len(m.summaries) == 0 {
				m.cancelled = true
			} else {
				m.selected = m.table.Cursor()
			}
			m.terminated = true
			return m, tea.Quit
		}
		updatedTable, cmd := m.table.Update(typed)
		m.table = updatedTable
		return m, cmd
	}

	return m, nil
}

func (m sessionPickerModel) View() string {
	scope := "current cwd"
	if m.showAll {
		scope = "all sessions"
	}

	lines := []string{
		pickerTitleStyle.Render("codex-exporter"),
		pickerMetaStyle.Render(fmt.Sprintf("Session Picker | Total: %d | Scope: %s", len(m.summaries), scope)),
		m.table.View(),
		pickerHelpStyle.Render("j/k or up/down: move | enter: select | q/esc/ctrl+c: cancel"),
	}
	return strings.Join(lines, "\n")
}

func runSessionPicker(summaries []sessionSummary, showAll bool, now time.Time, factory teaProgramFactory, programOpts ...tea.ProgramOption) (int, bool, error) {
	if factory == nil {
		factory = tea.NewProgram
	}

	model := newSessionPickerModel(summaries, showAll, now)
	allProgramOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, programOpts...)
	finalModel, err := factory(model, allProgramOpts...).Run()
	if err != nil {
		return -1, false, fmt.Errorf("run interactive session picker: %w", err)
	}

	resultModel, ok := finalModel.(sessionPickerModel)
	if !ok {
		return -1, false, errors.New("unexpected picker model type")
	}
	if resultModel.cancelled || resultModel.selected < 0 {
		return -1, true, nil
	}
	return resultModel.selected, false, nil
}
