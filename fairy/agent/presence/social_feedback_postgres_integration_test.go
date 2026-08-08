//go:build integration

package presence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/config"
	coredb "fairy/runtime/database"
	"fairy/runtime/model"
	"fairy/transport/session"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresFeedbackRecord struct {
	input  social.SocialFeedbackBatchInput
	result social.SocialFeedbackBatchResult
	err    error
}

type postgresFeedbackHost struct {
	store    *social.Store
	recorded chan postgresFeedbackRecord
	events   []model.StreamEvent

	mu       sync.Mutex
	request  model.CompiledPromptRequest
	warnings []error
}

func (h *postgresFeedbackHost) ResolveInteraction(string) (session.Resolved, error) {
	return session.Resolved{
		Endpoint: session.EndpointIM,
		Facts: session.Facts{
			Audience: session.AudienceMulti, Initiation: session.InitiationAmbient,
			Presentation: session.PresentationChat,
		},
		Principal: session.PrincipalNone,
		Memory:    session.MemoryPublic,
	}, nil
}

func (*postgresFeedbackHost) ActiveCharacter(characterID string) (character.Record, error) {
	return character.Record{
		CharacterID: characterID, Revision: 7, Name: "Fairy",
		Description: "参与公开群聊的桌面伴侣", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, nil
}

func (*postgresFeedbackHost) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{
		Model: "feedback-model",
		Capabilities: config.GatewayCapabilities{
			PromptCacheKey: true, CachedTokensUsage: true,
		},
	}, nil
}

func (h *postgresFeedbackHost) ExecuteRequest(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	h.mu.Lock()
	h.request = request
	events := append([]model.StreamEvent(nil), h.events...)
	h.mu.Unlock()
	return events, nil
}

func (h *postgresFeedbackHost) RecordSocialFeedbackBatch(ctx context.Context, input social.SocialFeedbackBatchInput) (social.SocialFeedbackBatchResult, error) {
	result, err := h.store.RecordSocialFeedbackBatch(ctx, input)
	h.recorded <- postgresFeedbackRecord{input: input, result: result, err: err}
	return result, err
}

func (h *postgresFeedbackHost) WarnFeedback(_ string, _ string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.warnings = append(h.warnings, err)
}

func (h *postgresFeedbackHost) snapshots() (model.CompiledPromptRequest, []error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.request, append([]error(nil), h.warnings...)
}

var _ FeedbackHost = (*postgresFeedbackHost)(nil)

func TestPostgresFeedbackLoopAttributesEvidenceAndRecordsCacheUsage(t *testing.T) {
	ctx := t.Context()
	pool := openIsolatedFeedbackPostgres(t, ctx)
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	historyStore, err := history.NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	socialStore, err := social.NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}

	const characterID = "character-feedback-e2e"
	bootstrap, err := historyStore.OpenOrCreateCharacterConversationContext(ctx, characterID)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := bootstrap.Conversation.ID
	entries, err := socialStore.StoreSocialMemoryEntries(ctx, social.SocialMemoryBatchInput{
		CharacterID: characterID, ConversationID: conversationID,
		Entries: []social.SocialMemoryEntryInput{{
			Kind: social.SocialMemoryBehavior, Situation: "群友表达焦虑时",
			Content: "先确认情绪，再提出一个具体问题", RecallCue: "焦虑、安慰和下一步",
			SourceStartUnixMS: 1, SourceEndUnixMS: 2,
		}},
	})
	if err != nil || len(entries) != 1 {
		t.Fatalf("StoreSocialMemoryEntries() = %#v, %v", entries, err)
	}
	turn, err := historyStore.BeginTurnContext(ctx, conversationID, "我有点焦虑")
	if err != nil {
		t.Fatal(err)
	}
	const reply = "先别急。现在最卡住你的是哪一步？"
	if _, err := historyStore.CompleteTurnContext(ctx, conversationID, turn.ID, reply); err != nil {
		t.Fatal(err)
	}

	cachedTokens, cacheWriteTokens := uint64(900), uint64(50)
	host := &postgresFeedbackHost{
		store: socialStore, recorded: make(chan postgresFeedbackRecord, 2),
		events: []model.StreamEvent{
			{Type: "text_delta", Data: `{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["feedback-message-0"]}]}`},
			{Type: "usage", Usage: &model.Usage{
				PromptTokens: 1200, CompletionTokens: 80,
				CachedInputTokens: &cachedTokens, CacheWriteTokens: &cacheWriteTokens,
			}},
		},
	}
	feedback := newFeedbackEngine(host, 1, 1, time.Hour)
	loop := newExperienceLoop(nil, feedback)
	t.Cleanup(loop.Close)

	entry := entries[0]
	if !loop.CompleteReply(FeedbackRegistration{
		CharacterID: characterID, ConversationID: conversationID, TurnID: turn.ID,
		Candidates: []social.SocialFeedbackCandidate{{
			ID: entry.ID, Kind: entry.Kind, Situation: entry.Situation,
			Content: entry.Content, RecallCue: entry.RecallCue,
		}},
		ReplyText: reply,
	}) {
		t.Fatal("CompleteReply() = false")
	}
	for index := range socialFeedbackObservationLimit {
		loop.Observe(conversationID, AmbientObservation{
			MessageID: fmt.Sprintf("feedback-message-%d", index),
			SenderID:  "group-member", SenderName: "群友",
			Text:            fmt.Sprintf("动态观察文本 %d：这样接话很自然", index),
			TimestampUnixMS: int64(100 + index),
		})
	}

	var recorded postgresFeedbackRecord
	select {
	case recorded = <-host.recorded:
	case <-time.After(3 * time.Second):
		request, warnings := host.snapshots()
		t.Fatalf("feedback worker timed out: stats=%#v request=%#v warnings=%v", loop.Stats().Feedback, request.Shape, warnings)
	}
	if recorded.err != nil {
		t.Fatalf("RecordSocialFeedbackBatch() error = %v", recorded.err)
	}
	loop.Close()
	select {
	case duplicate := <-host.recorded:
		t.Fatalf("feedback persisted more than once: %#v", duplicate.input)
	default:
	}

	stats := loop.Stats().Feedback
	if stats.Registered != 1 || stats.Superseded != 0 || stats.Dropped != 0 || stats.Succeeded != 1 || stats.Failed != 0 || stats.ModelCalls != 1 {
		t.Fatalf("feedback lifecycle stats = %#v", stats)
	}
	if stats.InputTokens != 1200 || stats.CachedObservedInputTokens != 1200 || stats.CachedInputTokens != 900 || stats.CacheWriteTokens != 50 || stats.OutputTokens != 80 {
		t.Fatalf("feedback usage stats = %#v", stats)
	}
	request, warnings := host.snapshots()
	if len(warnings) != 0 {
		t.Fatalf("feedback warnings = %v", warnings)
	}
	assertFeedbackCacheBoundary(t, request, conversationID, entry.ID, reply)

	if recorded.result.NoChange || len(recorded.result.Events) != 1 || len(recorded.input.Evaluations) != 1 {
		t.Fatalf("feedback result = %#v, input = %#v", recorded.result, recorded.input)
	}
	if recorded.input.CharacterID != characterID || recorded.input.ConversationID != conversationID || recorded.input.TurnID != turn.ID || recorded.input.ObservedMessageCount != socialFeedbackObservationLimit || recorded.input.EvaluatorRevision != SocialFeedbackEvaluatorRevision {
		t.Fatalf("feedback attribution = %#v", recorded.input)
	}
	assertStoredFeedback(t, ctx, pool, characterID, conversationID, turn.ID, entry.ID)
}

func assertFeedbackCacheBoundary(t *testing.T, request model.CompiledPromptRequest, conversationID, entryID, reply string) {
	t.Helper()
	if request.Shape.Lane != model.PromptLaneSocialFeedback || request.Shape.Model != "feedback-model" || request.Shape.PromptCacheKey != model.LaneCacheKey(conversationID, model.PromptLaneSocialFeedback) {
		t.Fatalf("feedback request shape = %#v", request.Shape)
	}
	if request.CacheInput == nil || request.CacheInput.CharacterRevision != 7 || request.CacheInput.StablePromptHash == "" || len(request.Input) != 3 {
		t.Fatalf("feedback cache request = %#v", request)
	}
	stableText := request.Input[0].Content + request.Input[1].Content
	dynamicText := request.Input[2].Content
	for _, dynamic := range []string{reply, "feedback-message-0", "动态观察文本 0", entryID} {
		if strings.Contains(stableText, dynamic) {
			t.Fatalf("dynamic feedback data entered stable prefix: %q", dynamic)
		}
	}
	if !strings.Contains(dynamicText, reply) || !strings.Contains(dynamicText, "feedback-message-0") || !strings.Contains(dynamicText, "动态观察文本 0") || strings.Contains(dynamicText, entryID) {
		t.Fatalf("feedback dynamic input exposed the wrong identity: %s", dynamicText)
	}
	expected, err := model.NewCacheKeyInputWithStablePrefix(
		model.PromptLaneSocialFeedback, "feedback-model", conversationID,
		SocialFeedbackInstructions, request.Input[:2],
	)
	if err != nil {
		t.Fatal(err)
	}
	expected.CharacterRevision = 7
	wantKey, err := model.BuildPromptCacheKey(expected)
	if err != nil {
		t.Fatal(err)
	}
	gotKey, err := model.BuildPromptCacheKey(*request.CacheInput)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != wantKey || request.CacheInput.StablePromptHash != expected.StablePromptHash {
		t.Fatalf("feedback cache identity = %q/%q, want %q/%q", gotKey, request.CacheInput.StablePromptHash, wantKey, expected.StablePromptHash)
	}
}

func assertStoredFeedback(t *testing.T, ctx context.Context, pool *coredb.Pool, characterID, conversationID, turnID, entryID string) {
	t.Helper()
	var storedCharacterID, storedConversationID, storedTurnID, storedEntryID string
	var adoption, outcome, credit, evaluatorRevision string
	var evidenceJSON []byte
	var observedCount int
	err := pool.Raw().QueryRow(ctx, `
SELECT character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
       evidence_message_ids, observed_message_count, evaluator_revision
FROM social_memory_feedback_events
WHERE turn_id = $1 AND entry_id = $2`, turnID, entryID).Scan(
		&storedCharacterID, &storedConversationID, &storedTurnID, &storedEntryID,
		&adoption, &outcome, &credit, &evidenceJSON, &observedCount, &evaluatorRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	var evidenceIDs []string
	if err := json.Unmarshal(evidenceJSON, &evidenceIDs); err != nil {
		t.Fatal(err)
	}
	if storedCharacterID != characterID || storedConversationID != conversationID || storedTurnID != turnID || storedEntryID != entryID {
		t.Fatalf("stored feedback scope = %q/%q/%q/%q", storedCharacterID, storedConversationID, storedTurnID, storedEntryID)
	}
	if adoption != social.SocialFeedbackAdopted || outcome != social.SocialFeedbackPositive || credit != social.SocialFeedbackCreditEntry || observedCount != socialFeedbackObservationLimit || evaluatorRevision != SocialFeedbackEvaluatorRevision || !slices.Equal(evidenceIDs, []string{"feedback-message-0"}) {
		t.Fatalf("stored feedback event = %q/%q/%q evidence=%v observed=%d revision=%q", adoption, outcome, credit, evidenceIDs, observedCount, evaluatorRevision)
	}
	if bytes.Contains(evidenceJSON, []byte("动态观察文本")) {
		t.Fatalf("stored feedback evidence leaked observation text: %s", evidenceJSON)
	}

	var evaluationCount, adoptedCount, positiveCount, partialCount, negativeCount int64
	var score int
	var status string
	if err := pool.Raw().QueryRow(ctx, `
SELECT feedback_evaluation_count, feedback_adopted_count, feedback_positive_count,
       feedback_partial_count, feedback_negative_count, feedback_score_basis_points, status
FROM social_memory_entries WHERE id = $1`, entryID).Scan(
		&evaluationCount, &adoptedCount, &positiveCount, &partialCount,
		&negativeCount, &score, &status,
	); err != nil {
		t.Fatal(err)
	}
	wantScore := social.SocialFeedbackScoreBasisPoints(1, 0, 0)
	if evaluationCount != 1 || adoptedCount != 1 || positiveCount != 1 || partialCount != 0 || negativeCount != 0 || score != wantScore || status != "active" {
		t.Fatalf("stored feedback aggregate = evaluations:%d adopted:%d positive:%d partial:%d negative:%d score:%d status:%q", evaluationCount, adoptedCount, positiveCount, partialCount, negativeCount, score, status)
	}
}

func openIsolatedFeedbackPostgres(t testing.TB, ctx context.Context) *coredb.Pool {
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
	schema := fmt.Sprintf("fairy_feedback_test_%d", time.Now().UnixNano())
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
	pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(withFeedbackPostgresSearchPath(t, databaseURL, schema)))
	if err != nil {
		t.Fatalf("open postgres pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func withFeedbackPostgresSearchPath(t testing.TB, rawURL, schema string) string {
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
