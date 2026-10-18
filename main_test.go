package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestExtractMessageBody(t *testing.T) {
	content := []contentItem{
		{Type: "input_text", Text: "hello"},
		{Type: "input_image", ImageURL: "https://example.com/a.png"},
		{Type: "output_text", Text: "world"},
	}

	body := extractMessageBody(content)
	if !strings.Contains(body, "hello") {
		t.Fatalf("expected body to contain user text, got: %s", body)
	}
	if !strings.Contains(body, "![input image](https://example.com/a.png)") {
		t.Fatalf("expected body to contain image markdown, got: %s", body)
	}
	if !strings.Contains(body, "world") {
		t.Fatalf("expected body to contain output text, got: %s", body)
	}
}

func TestReadSummaryFromRollout(t *testing.T) {
	dir := t.TempDir()
	rolloutPath := filepath.Join(dir, "rollout-2026-03-04T12-00-00-11111111-1111-1111-1111-111111111111.jsonl")

	content := strings.Join([]string{
		`{"timestamp":"2026-03-04T12:00:00Z","type":"session_meta","payload":{"id":"11111111-1111-1111-1111-111111111111","timestamp":"2026-03-04T12:00:00Z","cwd":"/tmp/work","source":"cli","model_provider":"quotio"}}`,
		`{"timestamp":"2026-03-04T12:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"ignored for summary"}]}}`,
	}, "\n")

	if err := os.WriteFile(rolloutPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write rollout fixture: %v", err)
	}

	summary, ok, err := readSummaryFromRollout(rolloutPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !ok {
		t.Fatalf("expected summary to be valid")
	}
	if summary.SessionID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected session id: %s", summary.SessionID)
	}
	if summary.Cwd != "/tmp/work" {
		t.Fatalf("unexpected cwd: %s", summary.Cwd)
	}
	if summary.Source != "cli" {
		t.Fatalf("unexpected source: %s", summary.Source)
	}
	if summary.ModelProvider != "quotio" {
		t.Fatalf("unexpected model provider: %s", summary.ModelProvider)
	}
	if summary.Title != "" {
		t.Fatalf("summary title should not be resolved at this stage: %s", summary.Title)
	}
	if summary.CreatedAt.IsZero() {
		t.Fatalf("expected created_at from filename")
	}
	if summary.UpdatedAt.IsZero() {
		t.Fatalf("expected updated_at from file stat")
	}
}

func TestCollectConversationAppliesThreadRollback(t *testing.T) {
	dir := t.TempDir()
	rolloutPath := filepath.Join(dir, "rollout.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-03-04T12:00:00Z","type":"session_meta","payload":{"id":"session-123","timestamp":"2026-03-04T12:00:00Z","cwd":"/tmp/work","source":"cli","model_provider":"quotio"}}`,
		`{"timestamp":"2026-03-04T12:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-old"}}`,
		`{"timestamp":"2026-03-04T12:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old user text"}]}}`,
		`{"timestamp":"2026-03-04T12:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"old assistant text"}]}}`,
		`{"timestamp":"2026-03-04T12:00:04Z","type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":1}}`,
		`{"timestamp":"2026-03-04T12:00:05Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-edited"}}`,
		`{"timestamp":"2026-03-04T12:00:06Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"edited user text"}]}}`,
		`{"timestamp":"2026-03-04T12:00:07Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"edited assistant text"}]}}`,
	}, "\n")
	if err := os.WriteFile(rolloutPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write rollout fixture: %v", err)
	}

	_, messages, err := collectConversation(rolloutPath)
	if err != nil {
		t.Fatalf("collect conversation: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages after rollback, got %d: %+v", len(messages), messages)
	}
	for _, msg := range messages {
		if strings.Contains(msg.Body, "old") {
			t.Fatalf("rolled-back message should not be exported: %+v", msg)
		}
	}
	if messages[0].Role != "user" || messages[0].Body != "edited user text" {
		t.Fatalf("unexpected edited user message: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Body != "edited assistant text" {
		t.Fatalf("unexpected edited assistant message: %+v", messages[1])
	}
}

func TestLoadThreadNameIndexLatestWins(t *testing.T) {
	codexHome := t.TempDir()
	sessionsDir := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("create sessions dir: %v", err)
	}

	indexPath := filepath.Join(codexHome, "session_index.jsonl")
	indexContent := strings.Join([]string{
		`{"id":"11111111-1111-1111-1111-111111111111","thread_name":"old title","updated_at":"2026-03-04T12:00:00Z"}`,
		`{"id":"22222222-2222-2222-2222-222222222222","thread_name":"another title","updated_at":"2026-03-04T12:00:01Z"}`,
		`{"id":"11111111-1111-1111-1111-111111111111","thread_name":"new title","updated_at":"2026-03-04T12:00:02Z"}`,
	}, "\n")
	if err := os.WriteFile(indexPath, []byte(indexContent), 0o644); err != nil {
		t.Fatalf("write index file: %v", err)
	}

	index, err := loadThreadNameIndex(sessionsDir)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	if got := index["11111111-1111-1111-1111-111111111111"]; got != "new title" {
		t.Fatalf("expected latest title, got: %s", got)
	}
	if got := index["22222222-2222-2222-2222-222222222222"]; got != "another title" {
		t.Fatalf("unexpected title for second id: %s", got)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 3, 4, 18, 10, 0, 0, time.UTC)
	cases := []struct {
		name   string
		target time.Time
		want   string
	}{
		{name: "just now", target: now.Add(-20 * time.Second), want: "just now"},
		{name: "minutes", target: now.Add(-5 * time.Minute), want: "5m ago"},
		{name: "hours", target: now.Add(-3 * time.Hour), want: "3h ago"},
		{name: "days", target: now.Add(-2 * 24 * time.Hour), want: "2d ago"},
		{name: "future", target: now.Add(90 * time.Minute), want: "in 1h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeTime(tc.target, now)
			if got != tc.want {
				t.Fatalf("relativeTime() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisplayTitle(t *testing.T) {
	if got := displayTitle("  my title  "); got != "my title" {
		t.Fatalf("unexpected title: %s", got)
	}
	if got := displayTitle("   "); got != "(untitled)" {
		t.Fatalf("expected untitled placeholder, got: %s", got)
	}
}

func TestParseThreadTitleLines(t *testing.T) {
	raw := strings.Join([]string{
		"11111111-1111-1111-1111-111111111111\tFirst Title",
		"invalid line",
		"22222222-2222-2222-2222-222222222222\tSecond Title",
		"11111111-1111-1111-1111-111111111111\tLatest Title",
	}, "\n")
	got := parseThreadTitleLines(raw)
	if got["11111111-1111-1111-1111-111111111111"] != "Latest Title" {
		t.Fatalf("expected latest title for duplicated id, got: %q", got["11111111-1111-1111-1111-111111111111"])
	}
	if got["22222222-2222-2222-2222-222222222222"] != "Second Title" {
		t.Fatalf("unexpected title: %q", got["22222222-2222-2222-2222-222222222222"])
	}
}

func TestFindStateDBPath(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("state_4.sqlite")
	write("state_12.sqlite")
	write("state_9.sqlite")

	path, ok := findStateDBPath(dir)
	if !ok {
		t.Fatalf("expected to find state db")
	}
	if !strings.HasSuffix(path, "state_12.sqlite") {
		t.Fatalf("expected highest numbered state db, got: %s", path)
	}
}

func TestCleanExportBodyStripsMemoryCitationBlocks(t *testing.T) {
	body := strings.Join([]string{
		"answer",
		"",
		"<oai-mem-citation>",
		"<citation_entries>",
		"MEMORY.md:1-2|note=[internal]",
		"</citation_entries>",
		"</oai-mem-citation>",
		"",
		"tail",
	}, "\n")

	cleaned := cleanExportBody(body)
	if strings.Contains(cleaned, "oai-mem-citation") || strings.Contains(cleaned, "MEMORY.md") {
		t.Fatalf("expected memory citation block to be stripped, got: %s", cleaned)
	}
	if cleaned != "answer\n\ntail" {
		t.Fatalf("unexpected cleaned body: %q", cleaned)
	}
}

func TestSelectMessagesForExportDefault(t *testing.T) {
	messages := []exportMessage{
		{Role: "user", Body: "# AGENTS.md instructions for /synthetic\n\n<INSTRUCTIONS>synthetic</INSTRUCTIONS>"},
		{Role: "user", Body: "<recommended_plugins>\nHere is a list of plugins that are available but not installed.\n</recommended_plugins>"},
		{Role: "user", Body: "u1"},
		{Role: "assistant", Phase: "analysis", Body: "thinking"},
		{Role: "assistant", Phase: "final", Body: "a1"},
		{Role: "assistant", Phase: "", Body: "a2"},
		{Role: "assistant", Phase: "final_answer", Body: "a3"},
		{Role: "tool", Body: "tool"},
	}

	filtered := selectMessagesForExport(messages, false)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 messages after default filtering, got: %d", len(filtered))
	}

	if filtered[0].Role != "user" || filtered[0].Body != "u1" {
		t.Fatalf("unexpected first filtered message: %+v", filtered[0])
	}
	if filtered[1].Role != "assistant" || filtered[1].Phase != "final" {
		t.Fatalf("expected assistant final message, got: %+v", filtered[1])
	}
	if filtered[2].Role != "assistant" || filtered[2].Phase != "final_answer" {
		t.Fatalf("expected assistant final_answer message, got: %+v", filtered[2])
	}
}

func TestSelectMessagesForExportFull(t *testing.T) {
	messages := []exportMessage{
		{Role: "user", Body: "u1"},
		{Role: "assistant", Phase: "analysis", Body: "thinking"},
		{Role: "assistant", Phase: "final", Body: "a1"},
	}

	filtered := selectMessagesForExport(messages, true)
	if len(filtered) != len(messages) {
		t.Fatalf("expected full export to keep all messages, got: %d", len(filtered))
	}
	for index := range messages {
		if filtered[index] != messages[index] {
			t.Fatalf("unexpected message at index %d: got=%+v want=%+v", index, filtered[index], messages[index])
		}
	}
}

func TestSessionPickerViewColumns(t *testing.T) {
	now := time.Date(2026, 3, 4, 18, 10, 0, 0, time.UTC)
	summaries := pickerTestSummaries(now)

	defaultView := newSessionPickerModel(summaries, false, now).View()
	if !strings.Contains(defaultView, "Index") || !strings.Contains(defaultView, "Updated") || !strings.Contains(defaultView, "Created") || !strings.Contains(defaultView, "Title") {
		t.Fatalf("default view missing required columns: %s", defaultView)
	}
	if strings.Contains(defaultView, "CWD") {
		t.Fatalf("default view should not include CWD column: %s", defaultView)
	}

	allView := newSessionPickerModel(summaries, true, now).View()
	if !strings.Contains(allView, "CWD") {
		t.Fatalf("showAll view should include CWD column: %s", allView)
	}
}

func TestSessionPickerSelectCurrentItem(t *testing.T) {
	now := time.Date(2026, 3, 4, 18, 10, 0, 0, time.UTC)
	summaries := pickerTestSummaries(now)

	model := newSessionPickerModel(summaries, false, now)
	model = applyPickerKey(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.table.Cursor() != 1 {
		t.Fatalf("cursor should move down with down arrow, got: %d", model.table.Cursor())
	}
	model = applyPickerKey(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.terminated {
		t.Fatalf("model should terminate on enter")
	}
	if model.cancelled {
		t.Fatalf("enter should not cancel selection")
	}
	if model.selected != 1 {
		t.Fatalf("unexpected selected index: %d", model.selected)
	}
}

func TestSessionPickerCancelKeys(t *testing.T) {
	now := time.Date(2026, 3, 4, 18, 10, 0, 0, time.UTC)
	summaries := pickerTestSummaries(now)

	cancelled := newSessionPickerModel(summaries, false, now)
	cancelled = applyPickerKey(t, cancelled, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !cancelled.terminated || !cancelled.cancelled {
		t.Fatalf("q should cancel picker")
	}

	cancelled = newSessionPickerModel(summaries, false, now)
	cancelled = applyPickerKey(t, cancelled, tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled.terminated || !cancelled.cancelled {
		t.Fatalf("esc should cancel picker")
	}
}

func TestSessionPickerWindowSizeUpdatesTableHeight(t *testing.T) {
	now := time.Date(2026, 3, 4, 18, 10, 0, 0, time.UTC)
	model := newSessionPickerModel(pickerTestSummaries(now), false, now)
	initialHeight := model.tableHeight

	model = applyPickerMsg(t, model, tea.WindowSizeMsg{Width: 140, Height: 48})
	if model.tableHeight == initialHeight {
		t.Fatalf("table height should change after window resize, initial=%d current=%d", initialHeight, model.tableHeight)
	}
	if model.tableHeight != pickerTableHeight(48) {
		t.Fatalf("unexpected table height after resize, got=%d want=%d", model.tableHeight, pickerTableHeight(48))
	}
}

func pickerTestSummaries(now time.Time) []sessionSummary {
	return []sessionSummary{
		{
			Title:     "first",
			Cwd:       "/tmp/one",
			UpdatedAt: now.Add(-1 * time.Hour),
			CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			Title:     "second",
			Cwd:       "/tmp/two",
			UpdatedAt: now.Add(-3 * time.Hour),
			CreatedAt: now.Add(-4 * time.Hour),
		},
	}
}

func applyPickerKey(t *testing.T, model sessionPickerModel, key tea.KeyMsg) sessionPickerModel {
	t.Helper()
	return applyPickerMsg(t, model, key)
}

func applyPickerMsg(t *testing.T, model sessionPickerModel, msg tea.Msg) sessionPickerModel {
	t.Helper()
	next, _ := model.Update(msg)
	updated, ok := next.(sessionPickerModel)
	if !ok {
		t.Fatalf("unexpected model type: %T", next)
	}
	return updated
}
