package tool

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/context/recall"
	"fairy/context/social"
	"fairy/runtime/model"
)

func TestRetrievalToolPromptItemsPreserveCorrelationAndSafeProgress(t *testing.T) {
	call := model.FunctionCall{CallID: "call-memory-1", Name: MemorySearch, Arguments: `{"query":"你好"}`}
	items := RetrievalPromptItems(call, "ok", recall.Context{SemanticStatus: "ready"})
	if len(items) != 2 || items[0].Type != model.PromptItemToolCall || items[1].Type != model.PromptItemToolResult {
		t.Fatalf("tool prompt items = %#v", items)
	}
	if items[0].ToolCallID != call.CallID || items[0].ToolName != call.Name || items[0].ToolArguments != call.Arguments || items[1].ToolCallID != call.CallID {
		t.Fatalf("tool correlation = %#v", items)
	}
	if items[1].Parts == nil || len(*items[1].Parts) != 1 {
		t.Fatalf("tool result parts = %#v", items[1].Parts)
	}
	var summary RetrievalResultSummary
	if err := json.Unmarshal([]byte((*items[1].Parts)[0].Text), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "ok" || !summary.Empty || summary.SemanticStatus != "ready" {
		t.Fatalf("empty result summary = %#v", summary)
	}

	private := recall.Context{
		PersonalMemories: []personal.Retrieved{{ID: "private-1", Content: "never duplicate this private content"}},
		Knowledge:        []knowledge.Retrieved{{ID: "knowledge-1", Statement: "never duplicate this knowledge"}},
	}
	items = RetrievalPromptItems(call, "provider-secret-error", private)
	resultText := (*items[1].Parts)[0].Text
	if strings.Contains(resultText, "provider-secret-error") || strings.Contains(resultText, "never duplicate") {
		t.Fatalf("tool result leaked internal content: %s", resultText)
	}
	if err := json.Unmarshal([]byte(resultText), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "failed" || summary.Empty || summary.PersonalCount != 1 || summary.KnowledgeCount != 1 {
		t.Fatalf("safe result summary = %#v", summary)
	}
}

func TestRuntimeRetrievalToolDetailCapturesResultAndMergedContext(t *testing.T) {
	result := recall.Context{
		Knowledge: []knowledge.Retrieved{{
			ID: "web-1", Layer: "knowledge", Topic: "web_search", Statement: "标题 — 摘要",
			Sources: []knowledge.AssistantSource{{Title: "标题", URL: "https://example.com/one", Snippet: "摘要", Rank: 1}},
		}},
		SemanticStatus: "unavailable",
	}
	merged := MergeRetrieval(recall.Context{
		PersonalMemories: []personal.Retrieved{{ID: "memory-1", Kind: "preference", Content: "喜欢蓝色"}},
	}, result)
	detail := RetrievalRuntimeDetail(" 苍之彼方的四重奏 ", "ok", result, merged)
	if detail.Version != "v1" || detail.Arguments.Query != "苍之彼方的四重奏" || detail.Result == nil || detail.MergedContext == nil {
		t.Fatalf("tool detail = %#v", detail)
	}
	if detail.Receipt.KnowledgeCount != 1 || detail.Receipt.Empty {
		t.Fatalf("tool receipt = %#v", detail.Receipt)
	}
	if len(detail.Result.Knowledge) != 1 || detail.Result.Knowledge[0].Sources[0].URL != "https://example.com/one" {
		t.Fatalf("tool result = %#v", detail.Result)
	}
	if len(detail.MergedContext.PersonalMemories) != 1 || len(detail.MergedContext.Knowledge) != 1 {
		t.Fatalf("merged context = %#v", detail.MergedContext)
	}
}

func TestRuntimeRetrievalToolDetailOmitsFailurePayloadAndRedactsSecretLikeText(t *testing.T) {
	failure := recall.Context{Knowledge: []knowledge.Retrieved{{Statement: "upstream Authorization: secret"}}}
	detail := RetrievalRuntimeDetail("Bearer private", "failed", failure, failure)
	if detail.Arguments.Query != "[已脱敏]" || detail.Result != nil || detail.MergedContext != nil {
		t.Fatalf("failure detail = %#v", detail)
	}
	if detail.Receipt.Status != "failed" || detail.Receipt.KnowledgeCount != 1 {
		t.Fatalf("failure receipt = %#v", detail.Receipt)
	}
}

func TestMergeRetrievalContextDeduplicatesAndPreservesPriority(t *testing.T) {
	base := recall.Context{
		PersonalMemories: []personal.Retrieved{{ID: "personal-1"}},
		Knowledge:        []knowledge.Retrieved{{ID: "knowledge-1"}},
		SocialMemories:   social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{{ID: "social-1"}}},
		SemanticStatus:   "ready",
	}
	extra := recall.Context{
		PersonalMemories: []personal.Retrieved{{ID: "personal-1"}, {ID: "personal-2"}},
		Knowledge:        []knowledge.Retrieved{{ID: "knowledge-1"}, {ID: "knowledge-2"}},
		SocialMemories:   social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{{ID: "social-1"}, {ID: "social-2"}}},
		SemanticStatus:   "used",
	}
	merged := MergeRetrieval(base, extra)
	if len(merged.PersonalMemories) != 2 || len(merged.Knowledge) != 2 || len(merged.SocialMemories.Entries) != 2 {
		t.Fatalf("merged context = %#v", merged)
	}
	if merged.SemanticStatus != "used" {
		t.Fatalf("semantic status = %q", merged.SemanticStatus)
	}
}

func TestBoundSocialToolRetrievalKeepsFeedbackCoverageWithinLimit(t *testing.T) {
	base := social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
		{ID: "base-1"}, {ID: "base-2"},
	}}
	extra := recall.Context{
		SocialMemories: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
			{ID: "base-2"}, {ID: "tool-1"}, {ID: "tool-2"},
		}},
		Knowledge: []knowledge.Retrieved{
			{ID: "base-2"}, {ID: "tool-1"}, {ID: "tool-2"},
		},
	}

	got := BoundSocialRetrieval(base, extra, 3)
	if len(got.SocialMemories.Entries) != 2 || got.SocialMemories.Entries[0].ID != "base-2" || got.SocialMemories.Entries[1].ID != "tool-1" {
		t.Fatalf("bounded social entries = %#v", got.SocialMemories.Entries)
	}
	if len(got.Knowledge) != 2 || got.Knowledge[0].ID != "base-2" || got.Knowledge[1].ID != "tool-1" {
		t.Fatalf("bounded social knowledge = %#v", got.Knowledge)
	}
	merged := MergeSocialMemory(base, got.SocialMemories)
	if len(merged.Entries) != 3 {
		t.Fatalf("merged feedback entries = %#v", merged.Entries)
	}
}

func TestWebAndErrorProjectionPreserveExistingShape(t *testing.T) {
	batch, err := knowledge.NewSearchBatch("conversation", "turn", "call", []knowledge.WebSearchHit{{Title: "Title", URL: "https://example.com", Snippet: "Snippet"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	web := FromSearchBatch(batch)
	if len(web.Knowledge) != 1 || web.Knowledge[0].ID != batch.Sources[0].ID || web.Knowledge[0].Statement != "Title — Snippet" || len(web.Knowledge[0].Sources) != 1 || web.Knowledge[0].Sources[0].FetchedAtUnixMS != 1 {
		t.Fatalf("web projection = %#v", web)
	}
	failure := FromError(MemorySearch, errors.New("unavailable"))
	if len(failure.Knowledge) != 1 || failure.Knowledge[0].ID != "tool-error-memory_search" || failure.Knowledge[0].Statement != "memory_search failed: unavailable" {
		t.Fatalf("error projection = %#v", failure)
	}
}
