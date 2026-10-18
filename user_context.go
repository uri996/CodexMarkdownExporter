package main

import "strings"

// Desktop records label content parts by provenance. Only kinds observed in
// local rollouts are excluded; unknown kinds and explicitly user-authored text
// are preserved, even when they look like product XML.
func extractUserPrompt(item responseItem) string {
	parts := make([]contentItem, 0, len(item.Content))
	kinds := item.Metadata.ContentItemKinds
	aligned := len(kinds) == len(item.Content)
	for i, part := range item.Content {
		if aligned {
			switch kinds[i] {
			case "plugins.recommendations", "agents_md.instructions", "environments.environment_context",
				"additional_content.codex_apps_open_page", "user.heartbeat", "skills.selected_skill_instructions":
				continue
			}
		} else if len(kinds) == 0 && isSystemInjectedUserMessage(part.Text) {
			continue
		}
		// A mismatched kind list cannot safely be paired with content parts.
		parts = append(parts, part)
	}
	return extractMessageBody(parts)
}

// Older rollouts lack provenance. Recognize only complete, known product
// wrappers at a content-part boundary, never arbitrary XML or inline blocks.
func isSystemInjectedUserMessage(body string) bool {
	text := strings.TrimSpace(body)
	if strings.HasPrefix(text, "# AGENTS.md instructions for ") &&
		strings.Contains(text, "\n\n<INSTRUCTIONS>") && strings.HasSuffix(text, "</INSTRUCTIONS>") {
		return true
	}
	if strings.HasPrefix(text, "<recommended_plugins>\nHere is a list of plugins that are available but not installed.") &&
		strings.HasSuffix(text, "</recommended_plugins>") {
		return true
	}
	return strings.HasPrefix(text, "<environment_context>") && strings.HasSuffix(text, "</environment_context>") &&
		strings.Contains(text, "<cwd>") && strings.Contains(text, "<shell>")
}
