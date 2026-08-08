//go:build integration

package social

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	history "fairy/context/history/transcript"
	coredb "fairy/runtime/database"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// socialIntegrationStores keeps the test boundary honest: social persistence
// and conversation history are separate production domains, composed only by
// the integration fixture that needs both.
type socialIntegrationStores struct {
	*Store
	history *history.Store
}

func newSocialIntegrationStores(pool *coredb.Pool) (*socialIntegrationStores, error) {
	socialStore, err := NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	historyStore, err := history.NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	return &socialIntegrationStores{Store: socialStore, history: historyStore}, nil
}

func (s *socialIntegrationStores) OpenOrCreateCharacterConversationContext(ctx context.Context, characterID string) (history.ConversationBootstrap, error) {
	return s.history.OpenOrCreateCharacterConversationContext(ctx, characterID)
}

func (s *socialIntegrationStores) BeginTurnContext(ctx context.Context, conversationID string, userMessage string) (history.PersistedTurn, error) {
	return s.history.BeginTurnContext(ctx, conversationID, userMessage)
}

func (s *socialIntegrationStores) CompleteTurnContext(ctx context.Context, conversationID string, turnID string, assistantMessage string) (history.MessageRecord, error) {
	return s.history.CompleteTurnContext(ctx, conversationID, turnID, assistantMessage)
}

func TestPostgresSocialMemoryScopesRetrievalAndFeedback(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedSocialPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := newSocialIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	emptyGroupContext, err := store.RetrieveSocialMemoryContext(canceledCtx, "character-1", "conversation-without-query", "!?")
	if err != nil || !emptyGroupContext.Empty() {
		t.Fatalf("empty-query RetrieveSocialMemoryContext() = %#v, %v", emptyGroupContext, err)
	}
	emptyCharacterContext, err := store.RetrieveCharacterSocialMemoryContext(canceledCtx, "character-1", "ab")
	if err != nil || !emptyCharacterContext.Empty() {
		t.Fatalf("empty-query RetrieveCharacterSocialMemoryContext() = %#v, %v", emptyCharacterContext, err)
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
	naturalGroupContext, err := store.RetrieveSocialMemoryContext(ctx, "character-1", first.Conversation.ID, "我最近有点实习焦虑")
	if err != nil || len(naturalGroupContext.Entries) != 1 || naturalGroupContext.Entries[0].Kind != SocialMemoryExpression {
		t.Fatalf("natural-query RetrieveSocialMemoryContext() = %#v, %v", naturalGroupContext, err)
	}
	repeatedGroupContext, err := store.RetrieveSocialMemoryContext(ctx, "character-1", first.Conversation.ID, "我最近有点实习焦虑")
	if err != nil || len(repeatedGroupContext.Entries) != 1 || repeatedGroupContext.Entries[0].ID != naturalGroupContext.Entries[0].ID {
		t.Fatalf("repeated natural-query RetrieveSocialMemoryContext() = %#v, %v", repeatedGroupContext, err)
	}
	naturalQueryContext, err := store.RetrieveCharacterSocialMemoryContext(ctx, "character-1", "我最近有点实习焦虑")
	if err != nil || len(naturalQueryContext.Entries) == 0 {
		t.Fatalf("natural-query RetrieveCharacterSocialMemoryContext() = %#v, %v", naturalQueryContext, err)
	}
	other, err := store.RetrieveSocialMemoryContext(ctx, "character-2", second.Conversation.ID, "项目经历")
	if err != nil || len(other.Entries) != 0 {
		t.Fatalf("cross-conversation retrieval = %#v, %v", other, err)
	}
	third, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-1", ConversationID: third.Conversation.ID,
		Entries: []SocialMemoryEntryInput{{Kind: SocialMemoryBehavior, Situation: "另一个群讨论找实习", Content: "先问清楚项目背景再给建议", RecallCue: "找实习、项目背景", SourceStartUnixMS: 30, SourceEndUnixMS: 40}},
	}); err != nil {
		t.Fatal(err)
	}
	isolatedGroupContext, err := store.RetrieveSocialMemoryContext(ctx, "character-1", first.Conversation.ID, "我最近有点找实习项目背景")
	if err != nil || len(isolatedGroupContext.Entries) == 0 {
		t.Fatalf("isolated natural-query RetrieveSocialMemoryContext() = %#v, %v", isolatedGroupContext, err)
	}
	for _, entry := range isolatedGroupContext.Entries {
		if entry.ConversationID != first.Conversation.ID || entry.CharacterID != "character-1" {
			t.Fatalf("group retrieval crossed scope: %#v", entry)
		}
	}
	allCharacter, err := store.RetrieveCharacterSocialMemoryContext(ctx, "character-1", "找实习")
	if err != nil || len(allCharacter.Entries) != 2 {
		t.Fatalf("cross-group character retrieval = %#v, %v", allCharacter, err)
	}
	otherCharacter, err := store.RetrieveCharacterSocialMemoryContext(ctx, "character-2", "找实习")
	if err != nil || len(otherCharacter.Entries) != 0 {
		t.Fatalf("cross-character retrieval = %#v, %v", otherCharacter, err)
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
	feedbackInput := SocialFeedbackBatchInput{
		CharacterID: "character-1", ConversationID: first.Conversation.ID, TurnID: turn.ID,
		ObservedMessageCount: 2, EvaluatorRevision: "social-feedback-v2",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: entries[1].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-message"},
		}},
	}
	feedback, err := store.RecordSocialFeedbackBatch(ctx, feedbackInput)
	if err != nil || feedback.NoChange || len(feedback.Events) != 1 {
		t.Fatalf("RecordSocialFeedbackBatch() = %#v, %v", feedback, err)
	}
	var evaluationCount, negativeCount int64
	if err := pool.Raw().QueryRow(ctx, "SELECT feedback_evaluation_count, feedback_negative_count FROM social_memory_entries WHERE id = $1", entries[1].ID).Scan(&evaluationCount, &negativeCount); err != nil {
		t.Fatal(err)
	}
	if evaluationCount != 1 || negativeCount != 1 {
		t.Fatalf("feedback counters = evaluations:%d negative:%d", evaluationCount, negativeCount)
	}
	if repeated, err := store.RecordSocialFeedbackBatch(ctx, feedbackInput); err != nil || !repeated.NoChange {
		t.Fatalf("idempotent feedback: %v", err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT feedback_evaluation_count, feedback_negative_count FROM social_memory_entries WHERE id = $1", entries[1].ID).Scan(&evaluationCount, &negativeCount); err != nil {
		t.Fatal(err)
	}
	if evaluationCount != 1 || negativeCount != 1 {
		t.Fatalf("duplicate feedback changed counters = evaluations:%d negative:%d", evaluationCount, negativeCount)
	}
}

func TestPostgresSocialFeedbackBatchIsAttributedIdempotentAndAtomic(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedSocialPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newSocialIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-feedback")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-feedback", ConversationID: conversation.Conversation.ID,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryBehavior, Situation: "群友焦虑时", Content: "先接住情绪", RecallCue: "焦虑安慰", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
			{Kind: SocialMemoryEpisode, Situation: "讨论面试时", Content: "大家在意具体例子", RecallCue: "面试例子", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
		},
	})
	if err != nil || len(entries) != 2 {
		t.Fatalf("StoreSocialMemoryEntries() = %#v, %v", entries, err)
	}
	completeTurn := func(prompt, reply string) string {
		t.Helper()
		turn, turnErr := store.BeginTurnContext(ctx, conversation.Conversation.ID, prompt)
		if turnErr != nil {
			t.Fatal(turnErr)
		}
		if _, turnErr = store.CompleteTurnContext(ctx, conversation.Conversation.ID, turn.ID, reply); turnErr != nil {
			t.Fatal(turnErr)
		}
		return turn.ID
	}
	turnID := completeTurn("我有点焦虑", "先别急，我们看一件具体的事")
	initial := SocialFeedbackBatchInput{
		CharacterID: "character-feedback", ConversationID: conversation.Conversation.ID, TurnID: turnID,
		ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v1",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: entries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"message-positive"},
		}},
	}
	result, err := store.RecordSocialFeedbackBatch(ctx, initial)
	if err != nil || result.NoChange || len(result.Events) != 1 {
		t.Fatalf("initial RecordSocialFeedbackBatch() = %#v, %v", result, err)
	}
	repeated, err := store.RecordSocialFeedbackBatch(ctx, initial)
	if err != nil || !repeated.NoChange || len(repeated.Events) != 1 || repeated.Events[0].ID != result.Events[0].ID {
		t.Fatalf("idempotent RecordSocialFeedbackBatch() = %#v, %v", repeated, err)
	}
	conflicting := initial
	conflicting.Evaluations = []SocialFeedbackEvaluation{
		{EntryID: entries[1].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive, Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"message-positive"}},
		{EntryID: entries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative, Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"message-positive"}},
	}
	if _, err := store.RecordSocialFeedbackBatch(ctx, conflicting); err == nil {
		t.Fatal("conflicting social feedback batch succeeded")
	}
	var rolledBackEventCount int
	if err := pool.Raw().QueryRow(ctx, "SELECT COUNT(*) FROM social_memory_feedback_events WHERE turn_id = $1 AND entry_id = $2", turnID, entries[1].ID).Scan(&rolledBackEventCount); err != nil {
		t.Fatal(err)
	}
	if rolledBackEventCount != 0 {
		t.Fatalf("conflicting batch left %d partial events", rolledBackEventCount)
	}

	secondTurnID := completeTurn("再聊聊", "继续")
	neutral, err := store.RecordSocialFeedbackBatch(ctx, SocialFeedbackBatchInput{
		CharacterID: "character-feedback", ConversationID: conversation.Conversation.ID, TurnID: secondTurnID,
		ObservedMessageCount: 2, EvaluatorRevision: "social-feedback-v1",
		Evaluations: []SocialFeedbackEvaluation{
			{EntryID: entries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative, Credit: SocialFeedbackCreditExecution, EvidenceMessageIDs: []string{"message-correction"}},
			{EntryID: entries[1].ID, Adoption: SocialFeedbackNotAdopted, Outcome: SocialFeedbackUnknown, Credit: SocialFeedbackCreditUnknown},
		},
	})
	if err != nil || neutral.NoChange || len(neutral.Events) != 2 {
		t.Fatalf("neutral RecordSocialFeedbackBatch() = %#v, %v", neutral, err)
	}
	type aggregate struct {
		evaluations, adopted, positive, partial, negative int64
		score                                             int
	}
	readAggregate := func(entryID string) aggregate {
		t.Helper()
		var got aggregate
		if err := pool.Raw().QueryRow(ctx, `
SELECT feedback_evaluation_count, feedback_adopted_count, feedback_positive_count,
       feedback_partial_count, feedback_negative_count, feedback_score_basis_points
FROM social_memory_entries WHERE id = $1`, entryID).Scan(
			&got.evaluations, &got.adopted, &got.positive, &got.partial, &got.negative, &got.score,
		); err != nil {
			t.Fatal(err)
		}
		return got
	}
	firstAggregate := readAggregate(entries[0].ID)
	if firstAggregate != (aggregate{evaluations: 2, adopted: 2, positive: 1, score: 3333}) {
		t.Fatalf("first aggregate = %#v", firstAggregate)
	}
	secondAggregate := readAggregate(entries[1].ID)
	if secondAggregate != (aggregate{evaluations: 1}) {
		t.Fatalf("second aggregate = %#v", secondAggregate)
	}
	var eventCount int
	if err := pool.Raw().QueryRow(ctx, "SELECT COUNT(*) FROM social_memory_feedback_events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 3 {
		t.Fatalf("feedback event count = %d, want 3", eventCount)
	}
}

func TestPostgresSocialMemoryBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedSocialPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newSocialIntegrationStores(pool)
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
	pool := openIsolatedSocialPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newSocialIntegrationStores(pool)
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
		if _, err := store.RecordSocialFeedbackBatch(ctx, SocialFeedbackBatchInput{
			CharacterID: "character-1", ConversationID: conversation.Conversation.ID, TurnID: turn.ID,
			ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v2",
			Evaluations: []SocialFeedbackEvaluation{{
				EntryID: entries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative,
				Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-negative"},
			}},
		}); err != nil {
			t.Fatalf("negative feedback %d: %v", index+1, err)
		}
	}
	var status string
	var negativeCount, positiveCount int64
	var quarantinedUntil int64
	if err := pool.Raw().QueryRow(ctx, "SELECT status, feedback_negative_count, feedback_positive_count, feedback_quarantined_until_ms FROM social_memory_entries WHERE id = $1", entries[0].ID).Scan(&status, &negativeCount, &positiveCount, &quarantinedUntil); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" || negativeCount != int64(SocialNegativeSuppressThreshold) || positiveCount != 0 || quarantinedUntil <= 0 {
		t.Fatalf("entry status/counters = %s neg=%d pos=%d quarantine=%d", status, negativeCount, positiveCount, quarantinedUntil)
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
	if _, err := pool.Raw().Exec(ctx, "UPDATE social_memory_entries SET feedback_quarantined_until_ms = 0 WHERE id = $1", entries[0].ID); err != nil {
		t.Fatal(err)
	}
	trial, err := store.RetrieveSocialMemoryContext(ctx, "character-1", conversation.Conversation.ID, "被点名")
	if err != nil || len(trial.Entries) != 1 || trial.Entries[0].ID != entries[0].ID || trial.Entries[0].Status != "suppressed" {
		t.Fatalf("expired quarantine trial = %#v, %v", trial, err)
	}
	extendTurn, err := store.BeginTurnContext(ctx, conversation.Conversation.ID, "再次被点名")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, conversation.Conversation.ID, extendTurn.ID, "再试一次"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordSocialFeedbackBatch(ctx, SocialFeedbackBatchInput{
		CharacterID: "character-1", ConversationID: conversation.Conversation.ID, TurnID: extendTurn.ID,
		ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v2",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: entries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-negative"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var extendedUntil int64
	if err := pool.Raw().QueryRow(ctx, "SELECT feedback_quarantined_until_ms FROM social_memory_entries WHERE id = $1", entries[0].ID).Scan(&extendedUntil); err != nil {
		t.Fatal(err)
	}
	if extendedUntil <= 0 {
		t.Fatalf("negative trial did not extend quarantine: %d", extendedUntil)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE social_memory_entries SET feedback_quarantined_until_ms = 0 WHERE id = $1", entries[0].ID); err != nil {
		t.Fatal(err)
	}
	recoverTurn, err := store.BeginTurnContext(ctx, conversation.Conversation.ID, "正向试用")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, conversation.Conversation.ID, recoverTurn.ID, "这次更合适"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordSocialFeedbackBatch(ctx, SocialFeedbackBatchInput{
		CharacterID: "character-1", ConversationID: conversation.Conversation.ID, TurnID: recoverTurn.ID,
		ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v2",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: entries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPartial,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-positive"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var recoveredStatus string
	var recoveredUntil *int64
	if err := pool.Raw().QueryRow(ctx, "SELECT status, feedback_quarantined_until_ms FROM social_memory_entries WHERE id = $1", entries[0].ID).Scan(&recoveredStatus, &recoveredUntil); err != nil {
		t.Fatal(err)
	}
	if recoveredStatus != "active" || recoveredUntil != nil {
		t.Fatalf("recovered entry = status:%s quarantine:%v", recoveredStatus, recoveredUntil)
	}
}

func TestPostgresSocialFeedbackOrderingKeepsRelevancePrimaryAndScoreAsTieBreaker(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedSocialPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newSocialIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	recordFeedback := func(conversationID string, evaluations []SocialFeedbackEvaluation) {
		t.Helper()
		turn, turnErr := store.BeginTurnContext(ctx, conversationID, "排序测试")
		if turnErr != nil {
			t.Fatal(turnErr)
		}
		if _, turnErr = store.CompleteTurnContext(ctx, conversationID, turn.ID, "排序回复"); turnErr != nil {
			t.Fatal(turnErr)
		}
		if _, turnErr = store.RecordSocialFeedbackBatch(ctx, SocialFeedbackBatchInput{
			CharacterID: "character-order", ConversationID: conversationID, TurnID: turn.ID,
			ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v2", Evaluations: evaluations,
		}); turnErr != nil {
			t.Fatal(turnErr)
		}
	}

	relevanceConversation, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-order")
	if err != nil {
		t.Fatal(err)
	}
	relevanceEntries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-order", ConversationID: relevanceConversation.Conversation.ID,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryBehavior, Situation: "项目经历需要经得住追问", Content: "项目经历需要经得住追问", RecallCue: "项目经历需要经得住追问", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
			{Kind: SocialMemoryEpisode, Situation: "讨论项目经历中的具体例子", Content: "群友会交换面试例子", RecallCue: "项目经历例子", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recordFeedback(relevanceConversation.Conversation.ID, []SocialFeedbackEvaluation{
		{EntryID: relevanceEntries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative, Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"ranking-message"}},
		{EntryID: relevanceEntries[1].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive, Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"ranking-message"}},
	})
	relevanceResult, err := store.RetrieveSocialMemoryContext(ctx, "character-order", relevanceConversation.Conversation.ID, "项目经历需要经得住追问")
	if err != nil || len(relevanceResult.Entries) < 2 || relevanceResult.Entries[0].ID != relevanceEntries[0].ID {
		t.Fatalf("relevance-primary result = %#v, %v", relevanceResult, err)
	}

	tieConversation, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-order")
	if err != nil {
		t.Fatal(err)
	}
	tieEntries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: "character-order", ConversationID: tieConversation.Conversation.ID,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryBehavior, Situation: "群聊接话", Content: "先短句回应", RecallCue: "群聊接话", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
			{Kind: SocialMemoryEpisode, Situation: "群聊接话", Content: "先短句回应", RecallCue: "群聊接话", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recordFeedback(tieConversation.Conversation.ID, []SocialFeedbackEvaluation{
		{EntryID: tieEntries[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackUnknown, Credit: SocialFeedbackCreditUnknown},
		{EntryID: tieEntries[1].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive, Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"ranking-message"}},
	})
	tieResult, err := store.RetrieveSocialMemoryContext(ctx, "character-order", tieConversation.Conversation.ID, "群聊接话")
	if err != nil || len(tieResult.Entries) != 2 || tieResult.Entries[0].ID != tieEntries[1].ID {
		t.Fatalf("score tie-break result = %#v, %v", tieResult, err)
	}
}

func openIsolatedSocialPostgresStore(t testing.TB, ctx context.Context) *coredb.Pool {
	t.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("fairy_social_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanup, cleanupErr := pgxpool.New(cleanupCtx, databaseURL)
		if cleanupErr != nil {
			t.Logf("open cleanup pool: %v", cleanupErr)
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(withSocialPostgresSearchPath(t, databaseURL, schema)))
	if err != nil {
		t.Fatalf("open postgres store pool: %v", err)
	}
	return pool
}

func withSocialPostgresSearchPath(t testing.TB, rawURL string, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}
