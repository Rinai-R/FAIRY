package companion

import "strings"

// Progressive tool-wait speech lines are intentionally not canned here.
// The mid-ReAct line is whatever user-facing content the main model returned
// alongside its tool calls; sanitizeUtteranceText cleans it into one spoken
// line and refuses structured/leaked artifacts (never invents a filler).
func sanitizeUtteranceText(draft string) string {
	text := sanitizeDisplayText(draft)
	if text == "" {
		return ""
	}
	// Refuse anything that looks like a structured reply or leaked plan JSON.
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return ""
	}
	if strings.Contains(text, `"gather"`) || strings.Contains(text, `"chains"`) || strings.Contains(text, `"speechText"`) {
		return ""
	}
	// Utterance payloads are single-line; collapse any internal breaks.
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func toolUtteranceReason(toolName string) string {
	switch toolName {
	case toolMemorySearch:
		return "searching_memory"
	case toolWebSearch:
		return "searching_web"
	default:
		return "thinking"
	}
}
