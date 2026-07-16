package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFTSQueryUsesTrigramsAndRejectsShortRuns(t *testing.T) {
	query, err := buildFTSQuery("太甜的饮料推荐")
	if err != nil {
		t.Fatalf("buildFTSQuery() error = %v", err)
	}
	for _, part := range []string{`"太甜的"`, `"甜的饮"`, `"的饮料"`, `"饮料推"`, `"料推荐"`} {
		if !strings.Contains(query, part) {
			t.Fatalf("query = %q, missing %s", query, part)
		}
	}
	empty, err := buildFTSQuery("饮料")
	if err != nil {
		t.Fatalf("short buildFTSQuery() error = %v", err)
	}
	if empty != "" {
		t.Fatalf("short query = %q, want empty", empty)
	}
}

func TestRetrievePersonalMemoryAndKnowledge(t *testing.T) {
	store, conversationID, characterID, turnID := seededKnowledgeStore(t)
	if _, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "用户不喜欢太甜的饮料", 9000); err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	insertKnowledgeFixture(t, store, conversationID, turnID, "verified", "user_confirmed")
	ctx, err := store.Retrieve(characterID, "太甜的饮料推荐")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(ctx.PersonalMemories) != 1 || ctx.PersonalMemories[0].Content != "用户不喜欢太甜的饮料" {
		t.Fatalf("personal memories = %#v", ctx.PersonalMemories)
	}

	knowledge, err := store.Retrieve(characterID, "主题陈述系统内容")
	if err != nil {
		t.Fatalf("Retrieve(knowledge) error = %v", err)
	}
	if len(knowledge.Knowledge) != 1 || knowledge.Knowledge[0].Topic != "主题陈述系统" {
		t.Fatalf("knowledge = %#v", knowledge.Knowledge)
	}
}

func TestRetrieveEmptyQueryReturnsEmptyContext(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	ctx, err := store.Retrieve("6a129284-6358-47b0-ad64-2a5907d36c91", "嗯")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if !ctx.Empty() {
		t.Fatalf("context = %#v", ctx)
	}
}

func TestRetrieveShortQueryHasNoFakeFallback(t *testing.T) {
	store, _, characterID, _ := seededKnowledgeStore(t)
	if _, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "用户喜欢清爽柠檬饮料第一种", 9000); err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	ctx, err := store.Retrieve(characterID, "饮料")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if !ctx.Empty() {
		t.Fatalf("short query must not fake-match: %#v", ctx)
	}
}

