package delivery

import (
	"strings"

	"fairy/agent/reply"
	"fairy/agent/tool"
)

// SanitizeUtteranceText cleans the model's user-facing tool-wait line and
// rejects structured or leaked planning artifacts.
func SanitizeUtteranceText(draft string) string {
	text := reply.SanitizeDisplayText(draft)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return ""
	}
	if strings.Contains(text, `"gather"`) || strings.Contains(text, `"chains"`) {
		return ""
	}
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func ToolUtteranceReason(toolName string) string {
	switch toolName {
	case tool.MemorySearch, tool.PublicMemorySearch:
		return "searching_memory"
	case tool.WebSearch:
		return "searching_web"
	default:
		return "thinking"
	}
}
