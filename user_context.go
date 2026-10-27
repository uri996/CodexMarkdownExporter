package main

import (
	"encoding/xml"
	"regexp"
	"strings"
	"time"
)

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
		} else if len(kinds) == 0 {
			if isSystemInjectedUserMessage(part.Text) {
				continue
			}
			part.Text = cleanHistoricalUserContext(part.Text)
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
	if start := strings.Index(body, text); text != "" && start >= 0 && markdownLiteralAt(body, start) {
		return false
	}
	if strings.HasPrefix(text, "# AGENTS.md instructions for ") &&
		strings.Contains(text, "\n\n<INSTRUCTIONS>") && strings.HasSuffix(text, "</INSTRUCTIONS>") {
		return true
	}
	if strings.HasPrefix(text, "<recommended_plugins>\nHere is a list of plugins that are available but not installed.") &&
		strings.HasSuffix(text, "</recommended_plugins>") {
		return true
	}
	return verifiedEnvironment(text)
}

var historicalEnvironment = regexp.MustCompile(`(?s)<environment_context>.*?</environment_context>`)
var ideHeader = regexp.MustCompile(`(?m)^(?:# )?Context from my IDE setup:[ \t]*\r?$`)
var ideRequest = regexp.MustCompile(`(?m)^(?:## )?My request for Codex:[ \t]*(?:\r?\n|$)`)
var ideSection = regexp.MustCompile(`(?m)^## (?:Active file:|Open tabs:|Active selection of the file:)`)

// Respect fenced and indented Markdown literals. These are provenance checks
// on the original content part, before any attachment text is expanded.
func markdownLiteralAt(text string, position int) bool {
	var fence byte
	width := 0
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if offset+len(line) > position || offset == position {
			return fence != 0 || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
		}
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if indent <= 3 && len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 {
					fence = trimmed[0]
					width = n
				} else if fence == trimmed[0] && n >= width && strings.TrimSpace(trimmed[n:]) == "" {
					fence = 0
				}
			}
		}
		offset += len(line)
	}
	return fence != 0
}

func verifiedEnvironment(text string) bool {
	text = strings.TrimSpace(text)
	match := historicalEnvironment.FindStringIndex(text)
	if match == nil || match[0] != 0 || match[1] != len(text) {
		return false
	}
	var context struct {
		XMLName    xml.Name `xml:"environment_context"`
		Cwd        string   `xml:"cwd"`
		Shell      string   `xml:"shell"`
		Date       string   `xml:"current_date"`
		Timezone   string   `xml:"timezone"`
		Filesystem *struct {
			Roots       *struct{} `xml:"workspace_roots"`
			Permissions *struct{} `xml:"permission_profile"`
		} `xml:"filesystem"`
		Unknown []xml.Name `xml:",any"`
	}
	if xml.Unmarshal([]byte(text), &context) != nil || context.XMLName.Local != "environment_context" || len(context.Unknown) > 0 {
		return false
	}
	if strings.TrimSpace(context.Cwd) != "" && strings.TrimSpace(context.Shell) != "" {
		return true
	}
	_, err := time.Parse("2006-01-02", context.Date)
	return err == nil && strings.TrimSpace(context.Timezone) != "" && context.Filesystem != nil && context.Filesystem.Roots != nil && context.Filesystem.Permissions != nil
}

func cleanHistoricalUserContext(text string) string {
	// Remove only the observed IDE preamble, not the request that follows it.
	var output strings.Builder
	last := 0
	for _, header := range ideHeader.FindAllStringIndex(text, -1) {
		if header[0] < last || markdownLiteralAt(text, header[0]) {
			continue
		}
		request := ideRequest.FindStringIndex(text[header[1]:])
		if request == nil {
			continue
		}
		start, end := header[1]+request[0], header[1]+request[1]
		preamble := text[header[1]:start]
		if !ideSection.MatchString(preamble) || !strings.Contains(preamble, "## Open tabs:") || markdownLiteralAt(text, start) {
			continue
		}
		// A second wrapper before the boundary is ambiguous; preserve it.
		if ideHeader.MatchString(preamble) {
			continue
		}
		output.WriteString(text[last:header[0]])
		last = end
	}
	output.WriteString(text[last:])
	text = output.String()
	output.Reset()
	last = 0
	for _, block := range historicalEnvironment.FindAllStringIndex(text, -1) {
		start, end := block[0], block[1]
		lineStart := strings.LastIndex(text[:start], "\n") + 1
		// Whole-line blocks only; ordinary inline XML and examples stay intact.
		if start != lineStart || markdownLiteralAt(text, start) || !verifiedEnvironment(text[start:end]) {
			continue
		}
		if end < len(text) && text[end] != '\r' && text[end] != '\n' {
			continue
		}
		output.WriteString(text[last:start])
		last = end
	}
	output.WriteString(text[last:])
	return output.String()
}
