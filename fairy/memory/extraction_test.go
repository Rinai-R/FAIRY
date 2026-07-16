package memory

import (
	"path/filepath"
	"testing"
)

func TestClaimExtractionBatchNone(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	batch, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, DefaultExtractionBatchLimit)
	if err != nil {
		t.Fatalf("ClaimExtractionBatch() error = %v", err)
	}
	if batch != nil {
		t.Fatalf("ClaimExtractionBatch() = %#v, want nil", batch)
	}
}

func TestClaimAndCompleteExtractionBatch(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	persisted, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, persisted.ID, "我在"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	batch, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, DefaultExtractionBatchLimit)
	if err != nil {
		t.Fatalf("ClaimExtractionBatch() error = %v", err)
	}
	if batch == nil || batch.BatchID == "" || len(batch.Turns) != 1 {
		t.Fatalf("ClaimExtractionBatch() = %#v", batch)
	}
	if batch.Turns[0].UserMessage != "你好" || batch.Turns[0].AssistantMessage != "我在" {
		t.Fatalf("turns = %#v", batch.Turns)
	}
	second, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, DefaultExtractionBatchLimit)
	if err != nil {
		t.Fatalf("second ClaimExtractionBatch() error = %v", err)
	}
	if second != nil {
		t.Fatal("second claim should be nil while claimed turns remain claimed")
	}
	if err := store.CompleteExtractionBatch(batch.BatchID); err != nil {
		t.Fatalf("CompleteExtractionBatch() error = %v", err)
	}
	third, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, DefaultExtractionBatchLimit)
	if err != nil {
		t.Fatalf("third ClaimExtractionBatch() error = %v", err)
	}
	if third != nil {
		t.Fatal("processed turns should not be reclaimable")
	}
}

func TestFailExtractionBatch(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	persisted, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, persisted.ID, "我在"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	batch, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, 1)
	if err != nil || batch == nil {
		t.Fatalf("ClaimExtractionBatch() = %#v, %v", batch, err)
	}
	if err := store.FailExtractionBatch(batch.BatchID, "MODEL_FAILED", "抽取失败", true); err != nil {
		t.Fatalf("FailExtractionBatch() error = %v", err)
	}
	catalog, err := store.ExtractionBatchCatalog("character-1")
	if err != nil {
		t.Fatalf("ExtractionBatchCatalog() error = %v", err)
	}
	if len(catalog.Failed) != 1 || catalog.Failed[0].ID != batch.BatchID {
		t.Fatalf("failed catalog = %#v", catalog.Failed)
	}
}

func TestCommitMemoryMutationsCreateAndSupersede(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	persisted, err := store.BeginTurn(bootstrap.Conversation.ID, "我喜欢安静")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, persisted.ID, "好，我记住了"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	batch, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, 1)
	if err != nil || batch == nil {
		t.Fatalf("ClaimExtractionBatch() = %#v, %v", batch, err)
	}
	results, err := store.CommitMemoryMutations(batch.BatchID, batch.CharacterID, nil, []MemoryMutation{{
		Operation:             "create",
		Kind:                  "preference",
		Scope:                 MemoryScope{Type: "global"},
		Content:               "用户喜欢安静",
		ConfidenceBasisPoints: 9000,
	}})
	if err != nil {
		t.Fatalf("CommitMemoryMutations() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != "applied" || results[0].MemoryID == "" {
		t.Fatalf("results = %#v", results)
	}
	catalog, err := store.PersonalMemoryCatalog(batch.CharacterID)
	if err != nil {
		t.Fatalf("PersonalMemoryCatalog() error = %v", err)
	}
	if len(catalog.Global) != 1 || catalog.Global[0].Content != "用户喜欢安静" {
		t.Fatalf("global memories = %#v", catalog.Global)
	}

	persisted2, err := store.BeginTurn(bootstrap.Conversation.ID, "其实更喜欢咖啡")
	if err != nil {
		t.Fatalf("BeginTurn(2) error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, persisted2.ID, "好"); err != nil {
		t.Fatalf("CompleteTurn(2) error = %v", err)
	}
	batch2, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, 1)
	if err != nil || batch2 == nil {
		t.Fatalf("ClaimExtractionBatch(2) = %#v, %v", batch2, err)
	}
	allowed := []string{catalog.Global[0].ID}
	results2, err := store.CommitMemoryMutations(batch2.BatchID, batch2.CharacterID, allowed, []MemoryMutation{{
		Operation:             "supersede",
		MemoryID:              catalog.Global[0].ID,
		Kind:                  "preference",
		Scope:                 MemoryScope{Type: "global"},
		Content:               "用户喜欢咖啡",
		ConfidenceBasisPoints: 9300,
	}})
	if err != nil {
		t.Fatalf("CommitMemoryMutations(supersede) error = %v", err)
	}
	if len(results2) != 1 || results2[0].Status != "applied" {
		t.Fatalf("results2 = %#v", results2)
	}
	catalog2, err := store.PersonalMemoryCatalog(batch.CharacterID)
	if err != nil {
		t.Fatalf("PersonalMemoryCatalog(2) error = %v", err)
	}
	if len(catalog2.Global) != 1 || catalog2.Global[0].Content != "用户喜欢咖啡" {
		t.Fatalf("global memories after supersede = %#v", catalog2.Global)
	}
}

func TestCommitMemoryMutationsDuplicateIsNoChange(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	persisted, err := store.BeginTurn(bootstrap.Conversation.ID, "我喜欢安静")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, persisted.ID, "好"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	existing, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "用户喜欢 安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	batch, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, 1)
	if err != nil || batch == nil {
		t.Fatalf("ClaimExtractionBatch() = %#v, %v", batch, err)
	}
	results, err := store.CommitMemoryMutations(batch.BatchID, batch.CharacterID, nil, []MemoryMutation{{
		Operation:             "create",
		Kind:                  "preference",
		Scope:                 MemoryScope{Type: "global"},
		Content:               "用户喜欢  安静",
		ConfidenceBasisPoints: 9000,
	}})
	if err != nil {
		t.Fatalf("CommitMemoryMutations() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != "no_change" || results[0].ExistingMemoryID != existing.ID {
		t.Fatalf("results = %#v", results)
	}
}
