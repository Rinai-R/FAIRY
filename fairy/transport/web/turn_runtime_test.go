package web

import "testing"

func TestProjectTurnRuntimeMetadataFailsClosed(t *testing.T) {
	projected := projectTurnRuntimeMetadata("tool", map[string]any{
		"tool": "web_search", "phase": "model_driven", "status": "ok", "knowledgeCount": float64(2),
		"arguments": `{"query":"private"}`, "result": "secret", "credential": "token",
	})
	if projected["tool"] != "web_search" || projected["status"] != "ok" || projected["knowledgeCount"] != float64(2) {
		t.Fatalf("projected = %#v", projected)
	}
	for _, key := range []string{"arguments", "result", "credential"} {
		if _, found := projected[key]; found {
			t.Fatalf("sensitive key %q was projected", key)
		}
	}
	if unknown := projectTurnRuntimeMetadata("future_event", map[string]any{"prompt": "secret"}); len(unknown) != 0 {
		t.Fatalf("unknown event metadata = %#v", unknown)
	}
}

func TestProjectTurnRuntimeUsageUsesKnownFieldsOnly(t *testing.T) {
	projected := projectTurnRuntimeMetadata("terminal", map[string]any{
		"status": "completed",
		"usage": []any{map[string]any{
			"lane": "respond", "historyWindow": float64(3), "providerSecret": "hidden",
			"usage": map[string]any{
				"inputTokens": float64(100), "outputTokens": float64(20), "rawPrompt": "hidden",
				"cachedInputTokens": map[string]any{"status": "observed", "tokens": float64(80), "private": "hidden"},
			},
		}},
	})
	usage := projected["usage"].([]map[string]any)
	if len(usage) != 1 || usage[0]["providerSecret"] != nil {
		t.Fatalf("usage = %#v", usage)
	}
	fields := usage[0]["usage"].(map[string]any)
	if fields["rawPrompt"] != nil || fields["inputTokens"] != float64(100) {
		t.Fatalf("usage fields = %#v", fields)
	}
}

func TestProjectTurnRuntimeToolDetailUsesNestedAllowlist(t *testing.T) {
	projected := projectTurnRuntimeMetadata("tool", map[string]any{
		"tool": "web_search", "status": "ok",
		"detail": map[string]any{
			"version":   "v1",
			"arguments": map[string]any{"query": "苍之彼方的四重奏", "raw": "hidden"},
			"receipt":   map[string]any{"status": "ok", "knowledgeCount": float64(1), "provider": "hidden"},
			"result": map[string]any{
				"knowledge": []any{map[string]any{
					"id": "web-1", "statement": "标题 — 摘要", "providerPayload": "hidden",
					"sources": []any{map[string]any{"title": "标题", "url": "https://example.com/one", "snippet": "摘要", "rank": float64(1), "headers": "hidden"}},
				}},
				"semanticStatus": "unavailable",
			},
			"mergedContext": map[string]any{
				"personalMemories": []any{map[string]any{"id": "memory-1", "kind": "preference", "content": "喜欢蓝色", "hidden": "no"}},
				"knowledge":        []any{},
			},
			"prompt": "hidden",
		},
	})
	detail := projected["detail"].(map[string]any)
	arguments := detail["arguments"].(map[string]any)
	if arguments["query"] != "苍之彼方的四重奏" || arguments["raw"] != nil || detail["prompt"] != nil {
		t.Fatalf("arguments/detail = %#v", detail)
	}
	result := detail["result"].(map[string]any)
	knowledge := result["knowledge"].([]map[string]any)
	if knowledge[0]["providerPayload"] != nil || knowledge[0]["statement"] != "标题 — 摘要" {
		t.Fatalf("knowledge = %#v", knowledge)
	}
	sources := knowledge[0]["sources"].([]map[string]any)
	if sources[0]["headers"] != nil || sources[0]["url"] != "https://example.com/one" {
		t.Fatalf("sources = %#v", sources)
	}
	merged := detail["mergedContext"].(map[string]any)
	personal := merged["personalMemories"].([]map[string]any)
	if personal[0]["content"] != "喜欢蓝色" || personal[0]["hidden"] != nil {
		t.Fatalf("personal memories = %#v", personal)
	}
}

func TestProjectTurnRuntimeToolDetailRejectsUnknownVersionAndSecretLikeText(t *testing.T) {
	if detail := projectRuntimeToolDetail(map[string]any{"version": "v2"}); detail != nil {
		t.Fatalf("unknown detail = %#v", detail)
	}
	detail := projectRuntimeToolDetail(map[string]any{
		"version":   "v1",
		"arguments": map[string]any{"query": "Bearer hidden"},
		"receipt":   map[string]any{"status": "failed"},
	})
	arguments := detail["arguments"].(map[string]any)
	if _, found := arguments["query"]; found {
		t.Fatalf("secret-like query projected: %#v", arguments)
	}
}
