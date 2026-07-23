//go:build integration

package memory

import (
	"context"
	"testing"

	pgstore "fairy/postgres"
)

func TestPostgresSocialMemoryScopesRetrievalAndFeedback(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-2")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-1", ConversationID: first.Conversation.ID,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryEpisode, Situation: "群里讨论找实习", Content: "大家认为项目经历要能经得住追问", RecallCue: "找实习、项目经历和面试焦虑", SourceStartUnixMS: 10, SourceEndUnixMS: 20},
			{Kind: SocialMemoryExpression, Situation: "缓和实习焦虑时", Content: "先短句接住情绪，再说一个具体观察", RecallCue: "实习焦虑和安慰", SourceStartUnixMS: 10, SourceEndUnixMS: 20},
		},
	})
	if err != nil || len(entries) != 2 {
		t.Fatalf("StoreSocialMemoryEntries() = %#v, %v", entries, err)
	}
	episodeContext, err := store.RetrieveSocialMemoryContext(ctx, "character-1", first.Conversation.ID, "项目经历")
	if err != nil || len(episodeContext.Entries) != 1 || episodeContext.Entries[0].Kind != SocialMemoryEpisode {
		t.Fatalf("episode RetrieveSocialMemoryContext() = %#v, %v", episodeContext, err)
	}
	expressionContext, err := store.RetrieveSocialMemoryContext(ctx, "character-1", first.Conversation.ID, "实习焦虑")
	if err != nil || len(expressionContext.Entries) != 1 || expressionContext.Entries[0].Kind != SocialMemoryExpression {
		t.Fatalf("expression RetrieveSocialMemoryContext() = %#v, %v", expressionContext, err)
	}
	other, err := store.RetrieveSocialMemoryContext(ctx, "character-2", second.Conversation.ID, "项目经历")
	if err != nil || len(other.Entries) != 0 {
		t.Fatalf("cross-conversation retrieval = %#v, %v", other, err)
	}
	if _, err := store.RetrieveSocialMemoryContext(ctx, "character-2", first.Conversation.ID, "项目经历"); err == nil {
		t.Fatal("character mismatch retrieval succeeded")
	}
	turn, err := store.BeginTurnContext(ctx, first.Conversation.ID, "我最近找实习有点焦虑")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, first.Conversation.ID, turn.ID, "先别急，能写清楚项目已经比想象中强了"); err != nil {
		t.Fatal(err)
	}
	feedback, err := store.RecordSocialReplyFeedback(ctx, SocialReplyFeedbackInput{
		CharacterID: "character-1", ConversationID: first.Conversation.ID, TurnID: turn.ID,
		EntryIDs: []string{entries[1].ID}, Outcome: SocialFeedbackNegative, ObservedMessageCount: 2,
	})
	if err != nil || feedback.Outcome != SocialFeedbackNegative {
		t.Fatalf("RecordSocialReplyFeedback() = %#v, %v", feedback, err)
	}
	var useCount, negativeCount int64
	if err := pool.Raw().QueryRow(ctx, "SELECT use_count, negative_count FROM social_memory_entries WHERE id = $1", entries[1].ID).Scan(&useCount, &negativeCount); err != nil {
		t.Fatal(err)
	}
	if useCount != 1 || negativeCount != 1 {
		t.Fatalf("feedback counters = use:%d negative:%d", useCount, negativeCount)
	}
	if _, err := store.RecordSocialReplyFeedback(ctx, SocialReplyFeedbackInput{
		CharacterID: "character-1", ConversationID: first.Conversation.ID, TurnID: turn.ID,
		EntryIDs: []string{entries[0].ID}, Outcome: SocialFeedbackPositive, ObservedMessageCount: 1,
	}); err == nil {
		t.Fatal("duplicate feedback for one turn succeeded")
	}
}

func TestPostgresSocialMemoryBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-1", ConversationID: conversation.Conversation.ID,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryEpisode, Situation: "有效情境", Content: "有效内容", RecallCue: "有效召回线索", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
			{Kind: "invalid", Situation: "无效情境", Content: "无效内容", RecallCue: "无效召回线索", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
		},
	})
	if err == nil {
		t.Fatal("invalid batch succeeded")
	}
	var count int
	if err := pool.Raw().QueryRow(ctx, "SELECT COUNT(*) FROM social_memory_entries").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial social memory batch wrote %d rows", count)
	}
}

func TestPostgresSocialMemorySuppressesAfterNegativeThreshold(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-1", ConversationID: conversation.Conversation.ID,
		Entries: []SocialMemoryEntryInput{{
			Kind: SocialMemoryBehavior, Situation: "被点名时", Content: "先短回再补一句", RecallCue: "被点名短回",
			SourceStartUnixMS: 10, SourceEndUnixMS: 20,
		}},
	})
	if err != nil || len(entries) != 1 {
		t.Fatalf("StoreSocialMemoryEntries() = %#v, %v", entries, err)
	}
	for index := 0; index < SocialNegativeSuppressThreshold; index++ {
		turn, beginErr := store.BeginTurnContext(ctx, conversation.Conversation.ID, "被点名了")
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if _, err := store.CompleteTurnContext(ctx, conversation.Conversation.ID, turn.ID, "先回一句"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordSocialReplyFeedback(ctx, SocialReplyFeedbackInput{
			CharacterID: "character-1", ConversationID: conversation.Conversation.ID, TurnID: turn.ID,
			EntryIDs: []string{entries[0].ID}, Outcome: SocialFeedbackNegative, ObservedMessageCount: 1,
		}); err != nil {
			t.Fatalf("negative feedback %d: %v", index+1, err)
		}
	}
	var status string
	var negativeCount, positiveCount int64
	if err := pool.Raw().QueryRow(ctx, "SELECT status, negative_count, positive_count FROM social_memory_entries WHERE id = $1", entries[0].ID).Scan(&status, &negativeCount, &positiveCount); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" || negativeCount != int64(SocialNegativeSuppressThreshold) || positiveCount != 0 {
		t.Fatalf("entry status/counters = %s neg=%d pos=%d", status, negativeCount, positiveCount)
	}
	retrieved, err := store.RetrieveSocialMemoryContext(ctx, "character-1", conversation.Conversation.ID, "被点名")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range retrieved.Entries {
		if entry.ID == entries[0].ID {
			t.Fatalf("suppressed entry still retrieved: %#v", retrieved)
		}
	}
}

func TestPostgresSocialReplyFeedbackAllowsEmptyEntries(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, conversation.Conversation.ID, "随便聊聊")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, conversation.Conversation.ID, turn.ID, "先听听大家怎么说"); err != nil {
		t.Fatal(err)
	}
	feedback, err := store.RecordSocialReplyFeedback(ctx, SocialReplyFeedbackInput{
		CharacterID: "character-1", ConversationID: conversation.Conversation.ID, TurnID: turn.ID,
		Outcome: SocialFeedbackUnknown, ObservedMessageCount: 0,
	})
	if err != nil || len(feedback.EntryIDs) != 0 || feedback.Outcome != SocialFeedbackUnknown {
		t.Fatalf("empty-entry feedback = %#v, %v", feedback, err)
	}
}
