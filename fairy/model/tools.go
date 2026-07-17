package model

import (
	"encoding/json"
	"strings"
)

// ToolSpec is an OpenAI-compatible function tool declaration.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// FunctionCall is a model-requested tool invocation.
type FunctionCall struct {
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func FunctionCallsFromEvents(events []StreamEvent) []FunctionCall {
	out := make([]FunctionCall, 0)
	for _, event := range events {
		if event.Type == "function_calls" && len(event.FunctionCalls) > 0 {
			out = append(out, event.FunctionCalls...)
		}
	}
	return out
}

func CollectTextFromEvents(events []StreamEvent) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Type == "text_delta" {
			builder.WriteString(event.Data)
		}
	}
	return builder.String()
}

func chatToolDefinitions(tools []ToolSpec) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  decodeToolParameters(tool.Parameters),
			},
		})
	}
	return out
}

func responsesToolDefinitions(tools []ToolSpec) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  decodeToolParameters(tool.Parameters),
		})
	}
	return out
}

func decodeToolParameters(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return decoded
}
