package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Override with -ldflags "-X main.version=..." when building a release.
var version = "v1.2.1"

func sessionStorageDirectory() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(expandHome(home), "sessions")
	}
	return defaultSessionsDir
}

type portableReport struct {
	File, Directory, Project                string
	Messages, Rollouts, Interactive, Issues int
	Batch                                   *batchResult
	DiscoveryErrors                         []string
}

func (r portableReport) summary(err error) string {
	heading := "导出完成"
	if err != nil {
		heading = "操作未全部完成：" + safeFailure(err)
	}
	if r.Batch != nil {
		b := r.Batch
		return fmt.Sprintf("%s\n\n项目：%s\n发现 rollout：%d；交互记录：%d；项目记录：%d\n成功：%d；跳过：%d；去重：%d；同 ID 冲突：%d\n读取失败：%d；写入失败：%d；索引失败：%d\n创建文件：%d；附件警告：%d；扫描/归属问题：%d\n%s\n%s\n\n保存目录：\n%s", heading, r.Project, r.Rollouts, r.Interactive, b.Selected, b.Success, b.Skipped, b.Deduplicated, b.Conflicts, b.ReadFailed, b.WriteFailed, b.IndexFailed, b.Written, b.AttachmentWarnings, r.Issues, strings.Join(b.Errors, "\n"), strings.Join(r.DiscoveryErrors, "\n"), r.Directory)
	}
	if r.File == "" && err == nil {
		return "操作已取消"
	}
	return fmt.Sprintf("%s\n\n文件：%s\n消息数：%d\n保存目录：\n%s\n%s", heading, r.File, r.Messages, r.Directory, strings.Join(r.DiscoveryErrors, "\n"))
}

type menuModel struct {
	cursor int
	choice int
}

func (m menuModel) Init() tea.Cmd { return tea.ClearScreen }
func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "up", "k":
			m.cursor = (m.cursor + 3) % 4
		case "down", "j":
			m.cursor = (m.cursor + 1) % 4
		case "enter":
			m.choice = m.cursor
			return m, tea.Quit
		case "esc", "q", "ctrl+c":
			m.choice = 3
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m menuModel) View() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Codex Markdown Exporter %s\n\n请选择操作：\n\n", version)
	for i, label := range []string{"单独导出一个会话", "按项目批量导出", "设置默认保存目录", "退出"} {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		fmt.Fprintf(&out, "%s%d. %s\n", marker, i+1, label)
	}
	out.WriteString("\n↑↓ 选择 · Enter 确认 · Esc 退出\n帮助：选择会话或项目后导出 Markdown。首次导出需先设置目录。\n")
	return out.String()
}

// The menu only orchestrates existing V1.1 entry points; no second parser/index.
func runPortableMenu() error {
	for {
		model, err := tea.NewProgram(menuModel{choice: -1}, tea.WithAltScreen()).Run()
		if err != nil {
			return err
		}
		choice := model.(menuModel).choice
		if choice < 0 || choice == 3 {
			return nil
		}
		if choice == 2 {
			if err := runDirectorySettings(); err != nil {
				return err
			}
			continue
		}
		root, err := savedOutputRoot()
		if err != nil {
			if err := runDirectorySettings(); err != nil {
				return err
			}
			root, err = savedOutputRoot()
			if err != nil {
				continue
			}
		}
		report := portableReport{}
		opts := portableOptions(choice, root, &report)
		if choice == 0 {
			err = runSingleExport(opts)
		} else {
			err = runBatchExport(opts)
		}
		model, uiErr := tea.NewProgram(newResultModel(report, err), tea.WithAltScreen()).Run()
		if uiErr != nil {
			return uiErr
		}
		if model.(resultModel).exit {
			return nil
		}
	}
}

func portableOptions(choice int, root string, report *portableReport) options {
	return options{sessionsDir: sessionStorageDirectory(), showAll: choice == 0, batch: choice == 1, outputDir: root, report: report}
}

type settingsModel struct {
	input           string
	cursor          int
	current, notice string
	confirm, done   bool
}

func newSettingsModel() settingsModel {
	current, err := savedOutputRoot()
	if err != nil {
		current = "尚未设置（或设置不可读）；可以重新设置"
	}
	return settingsModel{current: current}
}
func (m settingsModel) Init() tea.Cmd { return nil }
func (m settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "esc" || key.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.done && key.String() == "enter" {
			return m, tea.Quit
		}
		if m.confirm {
			switch key.String() {
			case "y", "Y":
				if err := saveUsableOutputRoot(m.input); err != nil {
					m.notice = "设置失败：" + safeFailure(err)
					m.confirm = false
				} else {
					m.current, _ = savedOutputRoot()
					m.done = true
					m.confirm = false
					m.notice = "设置已保存。Enter 返回主菜单。"
				}
			case "n", "N":
				m.confirm = false
				m.notice = "已取消确认，可修改目录或 Esc 返回。"
			}
			return m, nil
		}
		if key.String() == "f2" && !m.done {
			if _, err := portableOutputCandidate(); err == nil {
				m.input, m.cursor = "exports", len("exports")
				m.notice = "已选择便携候选目录；Enter 后按 Y 才会保存。"
			}
			return m, nil
		}
		if key.String() == "enter" && !m.done {
			dir, err := cleanDirectoryInput(m.input)
			if err != nil {
				m.notice = err.Error()
				return m, nil
			}
			m.input = dir
			m.cursor = len([]rune(dir))
			m.confirm = true
			m.notice = "保存为默认目录？Y 确认 / N 修改 / Esc 取消"
			if resolved, err := resolveExplicitOutputDirectory(dir); err == nil {
				m.notice += "\n目标目录：" + resolved
			}
			return m, nil
		}
		if !m.done {
			runes := []rune(m.input)
			switch key.Type {
			case tea.KeyRunes:
				// Terminal paste is input, never an application clipboard read.
				clean := []rune(strings.Map(func(r rune) rune {
					if r < 32 || r == 127 {
						return -1
					}
					return r
				}, string(key.Runes)))
				runes = append(append(append([]rune{}, runes[:m.cursor]...), clean...), runes[m.cursor:]...)
				m.cursor += len(clean)
			case tea.KeySpace:
				runes = append(append(append([]rune{}, runes[:m.cursor]...), ' '), runes[m.cursor:]...)
				m.cursor++
			case tea.KeyBackspace:
				if m.cursor > 0 {
					runes = append(runes[:m.cursor-1], runes[m.cursor:]...)
					m.cursor--
				}
			case tea.KeyDelete:
				if m.cursor < len(runes) {
					runes = append(runes[:m.cursor], runes[m.cursor+1:]...)
				}
			case tea.KeyLeft:
				if m.cursor > 0 {
					m.cursor--
				}
			case tea.KeyRight:
				if m.cursor < len(runes) {
					m.cursor++
				}
			case tea.KeyHome:
				m.cursor = 0
			case tea.KeyEnd:
				m.cursor = len(runes)
			case tea.KeyCtrlU:
				runes = nil
				m.cursor = 0
			}
			m.input = string(runes)
		}
	}
	return m, nil
}
func (m settingsModel) View() string {
	runes := []rune(m.input)
	footer := "Esc 取消，原设置保持不变。"
	if m.done {
		footer = "Enter / Esc 返回主菜单（新设置已保存）。"
	}
	if candidate, err := portableOutputCandidate(); err == nil {
		footer += "\nF2 选择便携目录：" + candidate + "（相对 EXE，确认后保存）"
	}
	return "设置默认保存目录\n\n当前默认保存目录：\n" + m.current + "\n\n新目录（Enter 确认，Ctrl+U 清空）：\n> " + string(runes[:m.cursor]) + "▏" + string(runes[m.cursor:]) + "\n\n" + m.notice + "\n" + footer + "\n"
}
func runDirectorySettings() error {
	_, err := tea.NewProgram(newSettingsModel(), tea.WithAltScreen()).Run()
	return err
}
func cleanDirectoryInput(input string) (string, error) {
	input = strings.TrimSpace(input)
	if len(input) >= 2 && ((input[0] == '"' && input[len(input)-1] == '"') || (input[0] == '\'' && input[len(input)-1] == '\'')) {
		input = strings.TrimSpace(input[1 : len(input)-1])
	}
	input = expandHome(input)
	if portableBuild == "true" && filepath.IsLocal(input) {
		return filepath.Clean(input), nil
	}
	if !filepath.IsAbs(input) {
		return "", errors.New("请输入完整的绝对目录路径")
	}
	return filepath.Clean(input), nil
}
func saveUsableOutputRoot(input string) error {
	dir, err := cleanDirectoryInput(input)
	if err != nil {
		return err
	}
	return saveOutputRoot(dir)
}

type folderOpenedMsg struct{ err error }
type resultModel struct {
	text, directory, notice string
	exit                    bool
	width, height           int
	viewport                viewport.Model
}

func newResultModel(r portableReport, err error) resultModel {
	m := resultModel{text: r.summary(err), directory: r.Directory, viewport: viewport.New(80, 18)}
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(m.text))
	return m
}
func (m resultModel) Init() tea.Cmd { return nil }
func (m resultModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case folderOpenedMsg:
		if v.err != nil {
			m.notice = "无法打开目录：" + safeFailure(v.err)
		} else {
			m.notice = "已请求打开输出目录"
		}
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.viewport.Width = max(20, v.Width)
		m.viewport.Height = max(3, v.Height-5)
		m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(m.text))
	case tea.KeyMsg:
		switch v.String() {
		case "enter":
			return m, tea.Quit
		case "esc", "q", "ctrl+c":
			m.exit = true
			return m, tea.Quit
		case "o", "O":
			return m, func() tea.Msg { return folderOpenedMsg{openOutputDirectory(m.directory)} }
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}
func (m resultModel) View() string {
	return m.viewport.View() + "\n\n" + m.notice + "\n↑↓ 滚动 · O 打开输出文件夹 · Enter 返回主菜单 · Esc 退出\n"
}

func outputDirectoryCommand(directory string) (*exec.Cmd, error) {
	if directory == "" {
		return nil, errors.New("本次操作没有可打开的输出目录")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("输出位置不是目录")
	}
	if runtime.GOOS != "windows" {
		return nil, errors.New("打开目录仅支持 Windows，请手动打开显示的路径")
	}
	return exec.Command("explorer.exe", abs), nil
}
func openOutputDirectory(directory string) error {
	cmd, err := outputDirectoryCommand(directory)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
