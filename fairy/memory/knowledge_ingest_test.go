package memory

import "testing"

func TestInsertVerifiedKnowledgeIsSearchableWithoutConfirm(t *testing.T) {
	store, _, characterID, _ := seededKnowledgeStore(t)
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turnID := bootstrap.Messages[0].TurnID
	record, err := store.InsertVerifiedKnowledge(
		"公开设定条目",
		"某作品在某年发布续作情报更新。",
		bootstrap.Conversation.ID,
		turnID,
		7200,
		[]AssistantSource{{Title: "来源", URL: "https://example.com", Snippet: "摘要", Rank: 1, FetchedAtUnixMS: 1}},
	)
	if err != nil {
		t.Fatalf("InsertVerifiedKnowledge() error = %v", err)
	}
	if record.Status != "verified" || record.VerificationBasis != "retrieval_ingest" {
		t.Fatalf("record = %#v", record)
	}
	catalog, err := store.KnowledgeCatalog()
	if err != nil {
		t.Fatalf("KnowledgeCatalog() error = %v", err)
	}
	foundCatalog := false
	for _, item := range catalog.Verified {
		if item.ID == record.ID {
			foundCatalog = true
		}
	}
	if !foundCatalog {
		t.Fatalf("verified knowledge missing from catalog: %#v", catalog.Verified)
	}
	ctx, err := store.Retrieve(characterID, "续作情报更新")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	found := false
	for _, item := range ctx.Knowledge {
		if item.ID == record.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("verified knowledge not searchable: %#v", ctx.Knowledge)
	}
}

func TestKnowledgeIngestQueueWritesVerified(t *testing.T) {
	store, _, characterID, _ := seededKnowledgeStore(t)
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turnID := bootstrap.Messages[0].TurnID
	err = store.EnqueueKnowledgeIngestSnapshots([]KnowledgeIngestSnapshot{{
		ConversationID:  bootstrap.Conversation.ID,
		TurnID:          turnID,
		Query:           "某作品续作情报",
		Title:           "续作情报条目",
		URL:             "https://example.com/a",
		Snippet:         "续作将在明年正式发售更新。",
		Rank:            1,
		FetchedAtUnixMS: 2,
	}})
	if err != nil {
		t.Fatalf("EnqueueKnowledgeIngestSnapshots() error = %v", err)
	}
	written, err := store.ProcessKnowledgeIngestJobs(4)
	if err != nil || written != 1 {
		t.Fatalf("ProcessKnowledgeIngestJobs() = %d, %v", written, err)
	}
	ctx, err := store.Retrieve(characterID, "明年正式发售")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(ctx.Knowledge) == 0 {
		t.Fatal("expected ingested knowledge to be retrievable")
	}
}

func TestKnowledgeIngestRejectsStructuralJunk(t *testing.T) {
	store, conversationID, _, turnID := seededKnowledgeStore(t)
	err := store.EnqueueKnowledgeIngestSnapshots([]KnowledgeIngestSnapshot{
		{ConversationID: conversationID, TurnID: turnID, Title: "短", Snippet: "太短", Rank: 1},
		{ConversationID: conversationID, TurnID: turnID, Title: "链接", Snippet: "https://example.com/only", Rank: 2},
	})
	if err != nil {
		t.Fatalf("EnqueueKnowledgeIngestSnapshots() error = %v", err)
	}
	written, err := store.ProcessKnowledgeIngestJobs(4)
	if err != nil || written != 0 {
		t.Fatalf("ProcessKnowledgeIngestJobs() = %d, %v want 0 written", written, err)
	}
	catalog, err := store.KnowledgeCatalog()
	if err != nil {
		t.Fatalf("KnowledgeCatalog() error = %v", err)
	}
	for _, item := range catalog.Verified {
		if item.VerificationBasis == "retrieval_ingest" {
			t.Fatalf("rejected ingest should not write knowledge: %#v", item)
		}
	}
}
