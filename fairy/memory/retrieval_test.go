package memory

import (
	"path/filepath"
	"strings"
	"testing"

	"fairy/memory/semantic"
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
	if ctx.SemanticStatus != "unavailable" || ctx.PersonalMemories[0].Layer != "preference" {
		t.Fatalf("retrieval metadata = semantic %q memory %#v", ctx.SemanticStatus, ctx.PersonalMemories[0])
	}

	knowledge, err := store.Retrieve(characterID, "主题陈述系统内容")
	if err != nil {
		t.Fatalf("Retrieve(knowledge) error = %v", err)
	}
	if len(knowledge.Knowledge) != 1 || knowledge.Knowledge[0].Topic != "主题陈述系统" {
		t.Fatalf("knowledge = %#v", knowledge.Knowledge)
	}
	if knowledge.SemanticStatus != "unavailable" || knowledge.Knowledge[0].Layer != "knowledge" {
		t.Fatalf("knowledge metadata = semantic %q knowledge %#v", knowledge.SemanticStatus, knowledge.Knowledge[0])
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
	if ctx.SemanticStatus != "unavailable" {
		t.Fatalf("semantic status = %q, want unavailable", ctx.SemanticStatus)
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
	if ctx.SemanticStatus != "unavailable" {
		t.Fatalf("semantic status = %q, want unavailable", ctx.SemanticStatus)
	}
}

func TestRetrieveRelationshipMemoryIsCharacterScoped(t *testing.T) {
	store, _, characterID, _ := seededKnowledgeStore(t)
	otherCharacterID := "11111111-1111-4111-8111-111111111111"
	if _, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "character", CharacterID: characterID}, "亚托莉知道用户喜欢安静陪伴", 9000); err != nil {
		t.Fatalf("CreatePersonalMemory(character) error = %v", err)
	}
	current, err := store.Retrieve(characterID, "安静陪伴测试")
	if err != nil {
		t.Fatalf("Retrieve(current) error = %v", err)
	}
	if len(current.PersonalMemories) != 1 || current.PersonalMemories[0].Layer != "relationship" {
		t.Fatalf("current memories = %#v", current.PersonalMemories)
	}
	other, err := store.Retrieve(otherCharacterID, "安静陪伴测试")
	if err != nil {
		t.Fatalf("Retrieve(other) error = %v", err)
	}
	if len(other.PersonalMemories) != 0 {
		t.Fatalf("relationship leaked across characters: %#v", other.PersonalMemories)
	}
}

func TestRetrieveWithSemanticUsesVectorRowsForShortQuery(t *testing.T) {
	store, _, characterID, _ := seededKnowledgeStore(t)
	memoryRecord, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "用户喜欢咖啡", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	workerEmbedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	if result, err := store.ProcessEmbeddingJobs(workerEmbedder, 10); err != nil || result.Succeeded != 1 {
		t.Fatalf("ProcessEmbeddingJobs() = %#v, %v", result, err)
	}

	queryEmbedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	ctx, err := store.RetrieveWithSemantic(characterID, "咖啡", queryEmbedder)
	if err != nil {
		t.Fatalf("RetrieveWithSemantic() error = %v", err)
	}
	if ctx.SemanticStatus != string(semantic.StatusUsed) {
		t.Fatalf("semantic status = %q, want used", ctx.SemanticStatus)
	}
	if len(ctx.PersonalMemories) != 1 || ctx.PersonalMemories[0].ID != memoryRecord.ID || ctx.PersonalMemories[0].Content != memoryRecord.Content {
		t.Fatalf("semantic personal memories = %#v", ctx.PersonalMemories)
	}
}

func TestRetrieveWithSemanticKeepsRelationshipScope(t *testing.T) {
	store, _, characterID, _ := seededKnowledgeStore(t)
	otherCharacterID := "11111111-1111-4111-8111-111111111111"
	memoryRecord, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "character", CharacterID: characterID}, "亚托莉记得安静陪伴", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	workerEmbedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	if result, err := store.ProcessEmbeddingJobs(workerEmbedder, 10); err != nil || result.Succeeded != 1 {
		t.Fatalf("ProcessEmbeddingJobs() = %#v, %v", result, err)
	}

	queryEmbedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	current, err := store.RetrieveWithSemantic(characterID, "陪伴", queryEmbedder)
	if err != nil {
		t.Fatalf("RetrieveWithSemantic(current) error = %v", err)
	}
	if len(current.PersonalMemories) != 1 || current.PersonalMemories[0].ID != memoryRecord.ID {
		t.Fatalf("current semantic memories = %#v", current.PersonalMemories)
	}
	other, err := store.RetrieveWithSemantic(otherCharacterID, "陪伴", queryEmbedder)
	if err != nil {
		t.Fatalf("RetrieveWithSemantic(other) error = %v", err)
	}
	if len(other.PersonalMemories) != 0 {
		t.Fatalf("semantic relationship leaked across characters: %#v", other.PersonalMemories)
	}
}

func TestRetrieveWithSemanticReturnsKnowledgeVectorHit(t *testing.T) {
	store, conversationID, characterID, turnID := seededKnowledgeStore(t)
	candidateID := insertKnowledgeFixture(t, store, conversationID, turnID, "candidate", "unverified")
	knowledgeRecord, err := store.ConfirmKnowledgeCandidate(candidateID)
	if err != nil {
		t.Fatalf("ConfirmKnowledgeCandidate() error = %v", err)
	}
	workerEmbedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	if result, err := store.ProcessEmbeddingJobs(workerEmbedder, 10); err != nil || result.Succeeded != 1 {
		t.Fatalf("ProcessEmbeddingJobs() = %#v, %v", result, err)
	}

	queryEmbedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	ctx, err := store.RetrieveWithSemantic(characterID, "系统", queryEmbedder)
	if err != nil {
		t.Fatalf("RetrieveWithSemantic() error = %v", err)
	}
	if ctx.SemanticStatus != string(semantic.StatusUsed) {
		t.Fatalf("semantic status = %q, want used", ctx.SemanticStatus)
	}
	if len(ctx.Knowledge) != 1 || ctx.Knowledge[0].ID != knowledgeRecord.ID {
		t.Fatalf("semantic knowledge = %#v", ctx.Knowledge)
	}
}
