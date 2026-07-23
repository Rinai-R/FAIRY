package companion

import (
	"context"
	"strings"
	"testing"

	"fairy/memory"
)

func TestSelectSocialExpressionsForToolKeepsOnlyExpressions(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "e1", Kind: memory.SocialMemoryEpisode, Situation: "话题", Content: "进展", RecallCue: "话题"},
		{ID: "e2", Kind: memory.SocialMemoryExpression, Situation: "安慰焦虑", Content: "先短句接住", RecallCue: "焦虑"},
		{ID: "e3", Kind: memory.SocialMemoryExpression, Situation: "吐槽", Content: "轻轻接梗", RecallCue: "吐槽"},
	}}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	got, err := service.selectSocialExpressionsForTool(context.Background(), "character-1", "conversation-1", "焦虑")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Knowledge) != 2 {
		t.Fatalf("knowledge = %#v", got.Knowledge)
	}
	if got.SemanticStatus != "unavailable" {
		t.Fatalf("SemanticStatus = %q", got.SemanticStatus)
	}
	for _, item := range got.Knowledge {
		if item.Layer != "social_expression" {
			t.Fatalf("item = %#v", item)
		}
	}
}

func TestSelectSocialContextForToolKeepsEpisodeAndBehavior(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "e1", Kind: memory.SocialMemoryEpisode, Situation: "话题", Content: "进展", RecallCue: "话题"},
		{ID: "e2", Kind: memory.SocialMemoryExpression, Situation: "安慰焦虑", Content: "先短句接住", RecallCue: "焦虑"},
		{ID: "e3", Kind: memory.SocialMemoryBehavior, Situation: "被点名", Content: "先短回", RecallCue: "点名"},
	}}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	got, err := service.selectSocialContextForTool(context.Background(), "character-1", "conversation-1", "话题")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Knowledge) != 2 {
		t.Fatalf("knowledge = %#v", got.Knowledge)
	}
	if got.SemanticStatus != "unavailable" {
		t.Fatalf("SemanticStatus = %q", got.SemanticStatus)
	}
	for _, item := range got.Knowledge {
		if item.Layer != "social_episode" && item.Layer != "social_behavior" {
			t.Fatalf("item = %#v", item)
		}
	}
}

func TestParticipationBehaviorQueryUsesRecentWindow(t *testing.T) {
	query := participationBehaviorQuery([]AmbientObservation{
		{Text: "第一条求职讨论"},
		{Text: "第二条整理项目"},
		{Text: "第三条继续聊"},
	})
	if !strings.Contains(query, "第一条") || !strings.Contains(query, "第三条") {
		t.Fatalf("query = %q", query)
	}
	if query == "第三条继续聊" {
		t.Fatal("query still uses only the last message")
	}
}
