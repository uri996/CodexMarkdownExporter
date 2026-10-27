package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var memoryCitationBlockPattern = regexp.MustCompile(`(?s)\n*<oai-mem-citation>\s*.*?</oai-mem-citation>\s*`)

func collectConversation(path string) (sessionMetaPayload, []exportMessage, error) {
	file, err := openRollout(path)
	if err != nil {
		return sessionMetaPayload{}, nil, err
	}
	defer file.Close()

	scanner := newRolloutScanner(file)

	meta := sessionMetaPayload{}
	metaFound := false
	messages := make([]exportMessage, 0)
	activeTurnID := ""
	turnOrder := make([]string, 0)

	for scanner.Scan() {
		line := rolloutLine{}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		switch line.Type {
		case "event_msg":
			event := eventPayload{}
			if err := json.Unmarshal(line.Payload, &event); err != nil {
				continue
			}
			switch event.Type {
			case "task_started":
				if strings.TrimSpace(event.TurnID) == "" {
					continue
				}
				activeTurnID = event.TurnID
				if len(turnOrder) == 0 || turnOrder[len(turnOrder)-1] != activeTurnID {
					turnOrder = append(turnOrder, activeTurnID)
				}
			case "thread_rolled_back":
				messages, turnOrder, activeTurnID = rollbackConversationTurns(messages, turnOrder, activeTurnID, event.NumTurns)
			}
		case "session_meta":
			if metaFound {
				continue
			}
			if err := json.Unmarshal(line.Payload, &meta); err == nil {
				metaFound = true
			}
		case "response_item":
			item := responseItem{}
			if err := json.Unmarshal(line.Payload, &item); err != nil {
				continue
			}
			if item.Type != "message" {
				continue
			}
			if item.Role != "user" && item.Role != "assistant" {
				continue
			}
			body := extractMessageBody(item.Content)
			if item.Role == "user" {
				body = extractUserPrompt(item)
			} else {
				body = cleanExportBody(body)
			}
			if strings.TrimSpace(body) == "" {
				continue
			}
			messages = append(messages, exportMessage{Role: item.Role, Phase: item.Phase, Body: body, turnID: activeTurnID, userInput: item.Role == "user"})
		}
	}
	if err := rolloutScanError(scanner); err != nil {
		return sessionMetaPayload{}, nil, err
	}
	if !metaFound {
		return sessionMetaPayload{}, nil, errors.New("session metadata not found")
	}
	return meta, messages, nil
}

func selectMessagesForExport(messages []exportMessage, full bool) []exportMessage {
	if full {
		return messages
	}
	filtered := make([]exportMessage, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if !msg.userInput && isSystemInjectedUserMessage(msg.Body) {
				continue
			}
			filtered = append(filtered, msg)
		case "assistant":
			phase := strings.ToLower(strings.TrimSpace(msg.Phase))
			if phase == "final" || phase == "final_answer" {
				filtered = append(filtered, msg)
			}
		}
	}
	return filtered
}

func rollbackConversationTurns(messages []exportMessage, turnOrder []string, activeTurnID string, numTurns int) ([]exportMessage, []string, string) {
	if numTurns <= 0 || len(turnOrder) == 0 {
		return messages, turnOrder, activeTurnID
	}

	removed := make(map[string]struct{}, numTurns)
	for i := 0; i < numTurns && len(turnOrder) > 0; i++ {
		turnID := turnOrder[len(turnOrder)-1]
		turnOrder = turnOrder[:len(turnOrder)-1]
		if turnID != "" {
			removed[turnID] = struct{}{}
		}
	}
	if len(removed) == 0 {
		return messages, turnOrder, activeTurnID
	}

	kept := messages[:0]
	for _, msg := range messages {
		if _, ok := removed[msg.turnID]; ok {
			continue
		}
		kept = append(kept, msg)
	}
	if _, ok := removed[activeTurnID]; ok {
		activeTurnID = ""
	}
	return kept, turnOrder, activeTurnID
}

func buildMarkdown(summary sessionSummary, messages []exportMessage) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n", displayTitle(summary.Title))
	for _, msg := range messages {
		icon := "🧑"
		if msg.Role == "assistant" {
			icon = "💻"
		}
		fmt.Fprintf(&sb, "\n# %s %s\n\n", icon, humanRole(msg.Role))
		sb.WriteString(msg.Body)
		if !strings.HasSuffix(msg.Body, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteString("\n---\n")
	}
	return sb.String()
}

func cleanExportBody(body string) string {
	return memoryCitationBlockPattern.ReplaceAllString(body, "\n\n")
}

func extractMessageBody(content []contentItem) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch item.Type {
		case "input_text", "output_text":
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case "input_image":
			if item.ImageURL != "" {
				parts = append(parts, fmt.Sprintf("![input image](%s)", item.ImageURL))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderSession(summary sessionSummary, full bool) (string, int, error) {
	_, messages, err := collectConversation(summary.Path)
	if err != nil {
		return "", 0, err
	}
	messages = selectMessagesForExport(messages, full)
	messages = normalizePastedTextMessages(messages, codexAttachmentRoot())
	return buildMarkdown(summary, messages), len(messages), nil
}
