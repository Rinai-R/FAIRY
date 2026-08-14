//go:build integration

package personal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/runtime/embedding"
	"fairy/runtime/seekdb"
)

const personalIntegrationEmbeddingSpace = "fairy-personal-integration-1024"

func TestRealSeekDBPersonalStoreIsScopedAtomicAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openPersonalSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closePersonalSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB personal schema: %v", err)
	}

	seedPersonalIntegrationAuthority(t, database)
	textStore := newPersonalSeekDBStore(t, database, runtimeConfig.QueryLimit, nil)
	vectorStore := newPersonalSeekDBStore(
		t, database, runtimeConfig.QueryLimit, personalIntegrationEmbedder{},
	)
	if textStore.usesPostgres() || !textStore.usesSeekDB() || textStore.pool != nil || textStore.seekDB != database {
		t.Fatalf("SeekDB personal store exposed another authority: %#v", textStore)
	}

	records := assertPersonalSeekDBCRUDCatalogAndEmbeddings(t, database, textStore, vectorStore)
	assertPersonalSeekDBProjectionIsScoped(t, textStore, vectorStore)
	assertPersonalSeekDBHybridRetrieval(t, textStore, vectorStore)
	assertPersonalSeekDBPortraitIsScopedAndBounded(t, vectorStore)
	assertPersonalSeekDBSummary(t, database, textStore)
	assertPersonalSeekDBProviderFailureWritesNothing(t, database, textStore, runtimeConfig.QueryLimit)
	assertPersonalSeekDBCancellationWritesNothing(t, database, textStore)
	assertPersonalSeekDBConcurrentRevisionIsAtomic(t, database, vectorStore)

	beforeRestart, err := textStore.SummaryContext(t.Context())
	if err != nil {
		t.Fatalf("summary before restart: %v", err)
	}
	closePersonalSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true

	instance, err = seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB personal runtime: %v", err)
	}
	closed = false
	database = instance.SQL()
	restarted := newPersonalSeekDBStore(t, database, runtimeConfig.QueryLimit, personalIntegrationEmbedder{})
	afterRestart, err := restarted.SummaryContext(t.Context())
	if err != nil {
		t.Fatalf("summary after restart: %v", err)
	}
	if afterRestart != beforeRestart {
		t.Fatalf("summary after restart = %#v, want %#v", afterRestart, beforeRestart)
	}
	for id, wantStatus := range map[string]string{
		records.nullableGlobal.ID: "active",
		records.vectorOriginal.ID: "superseded",
		records.vectorRevision.ID: "tombstone",
		records.legacyOriginal.ID: "superseded",
		records.legacyAssigned.ID: "active",
		records.legacyPending.ID:  "active",
	} {
		var status string
		if err := database.QueryRowContext(t.Context(),
			"SELECT status FROM personal_memories WHERE id = ?", id,
		).Scan(&status); err != nil {
			t.Fatalf("load memory %q after restart: %v", id, err)
		}
		if status != wantStatus {
			t.Fatalf("memory %q status after restart = %q, want %q", id, status, wantStatus)
		}
	}
	catalog, err := restarted.PersonalMemoryCatalogContext(t.Context(), personalCharacterA)
	if err != nil {
		t.Fatalf("catalog after restart: %v", err)
	}
	if !personalRecordsContain(catalog.Global, records.nullableGlobal.ID) ||
		!personalRecordsContain(catalog.Character, records.legacyAssigned.ID) ||
		!personalRecordsContain(catalog.NeedsReview, records.legacyPending.ID) {
		t.Fatalf("catalog after restart lost authoritative records: %#v", catalog)
	}
}

const (
	personalCharacterA    = "personal-character-a"
	personalCharacterB    = "personal-character-b"
	personalConversationA = "personal-conversation-a"
	personalConversationB = "personal-conversation-b"
	personalConversationC = "personal-conversation-c"
	personalTurnA         = "personal-turn-a"
	personalTurnB         = "personal-turn-b"
	personalTurnC         = "personal-turn-c"
)

type personalIntegrationRecords struct {
	nullableGlobal Record
	vectorOriginal Record
	vectorRevision Record
	legacyOriginal Record
	legacyAssigned Record
	legacyPending  Record
}

func assertPersonalSeekDBCRUDCatalogAndEmbeddings(
	t *testing.T,
	database *sql.DB,
	textStore, vectorStore *Store,
) personalIntegrationRecords {
	t.Helper()
	ctx := t.Context()
	nullableGlobal, err := textStore.CreatePersonalMemoryContext(
		ctx, "profile", Scope{Type: "global"}, "用户习惯先听完再回应", 9100,
	)
	if err != nil {
		t.Fatalf("create nullable global memory: %v", err)
	}
	assertPersonalEmbeddingNull(t, database, nullableGlobal.ID)

	vectorOriginal, err := vectorStore.CreatePersonalMemoryContext(
		ctx, "preference", Scope{Type: "global"}, "用户喜欢清晨散步", 9200,
	)
	if err != nil {
		t.Fatalf("create vector memory: %v", err)
	}
	assertPersonalEmbedding1024(t, database, vectorOriginal.ID, vectorOriginal.Content)
	vectorRevision, err := vectorStore.RevisePersonalMemoryContext(
		ctx, vectorOriginal.ID, "用户更喜欢雨后清晨散步", 9400,
	)
	if err != nil {
		t.Fatalf("revise vector memory: %v", err)
	}
	if vectorRevision.SupersedesID == nil || *vectorRevision.SupersedesID != vectorOriginal.ID {
		t.Fatalf("vector revision = %#v", vectorRevision)
	}
	assertPersonalEmbedding1024(t, database, vectorRevision.ID, vectorRevision.Content)
	assertPersonalStatus(t, database, vectorOriginal.ID, "superseded")
	if err := vectorStore.TombstonePersonalMemoryContext(ctx, vectorRevision.ID); err != nil {
		t.Fatalf("tombstone vector revision: %v", err)
	}
	assertPersonalStatus(t, database, vectorRevision.ID, "tombstone")

	legacyOriginal, err := textStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "unassigned_legacy"}, "旧关系记忆等待归属", 7300,
	)
	if err != nil {
		t.Fatalf("create legacy relationship: %v", err)
	}
	assertPersonalEmbeddingNull(t, database, legacyOriginal.ID)
	legacyAssigned, err := vectorStore.AssignLegacyRelationshipContext(ctx, legacyOriginal.ID, personalCharacterA)
	if err != nil {
		t.Fatalf("assign legacy relationship: %v", err)
	}
	if legacyAssigned.Scope != (Scope{Type: "character", CharacterID: personalCharacterA}) ||
		legacyAssigned.SupersedesID == nil || *legacyAssigned.SupersedesID != legacyOriginal.ID {
		t.Fatalf("assigned legacy relationship = %#v", legacyAssigned)
	}
	assertPersonalStatus(t, database, legacyOriginal.ID, "superseded")
	assertPersonalEmbedding1024(t, database, legacyAssigned.ID, legacyAssigned.Content)

	legacyPending, err := textStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "unassigned_legacy"}, "仍需人工归属的关系", 7000,
	)
	if err != nil {
		t.Fatalf("create pending legacy relationship: %v", err)
	}
	otherCharacter, err := textStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "character", CharacterID: personalCharacterB},
		"只属于角色 B 的关系", 9000,
	)
	if err != nil {
		t.Fatalf("create other-character relationship: %v", err)
	}

	catalog, err := textStore.PersonalMemoryCatalogContext(ctx, personalCharacterA)
	if err != nil {
		t.Fatalf("load personal catalog: %v", err)
	}
	if !personalRecordsContain(catalog.Global, nullableGlobal.ID) ||
		!personalRecordsContain(catalog.Character, legacyAssigned.ID) ||
		!personalRecordsContain(catalog.NeedsReview, legacyPending.ID) {
		t.Fatalf("catalog misses expected records: %#v", catalog)
	}
	for _, absent := range []string{
		vectorOriginal.ID, vectorRevision.ID, legacyOriginal.ID, otherCharacter.ID,
	} {
		if personalRecordsContain(catalog.Global, absent) ||
			personalRecordsContain(catalog.Character, absent) ||
			personalRecordsContain(catalog.NeedsReview, absent) {
			t.Fatalf("catalog leaked status/scope-isolated memory %q: %#v", absent, catalog)
		}
	}
	return personalIntegrationRecords{
		nullableGlobal: nullableGlobal, vectorOriginal: vectorOriginal,
		vectorRevision: vectorRevision, legacyOriginal: legacyOriginal,
		legacyAssigned: legacyAssigned, legacyPending: legacyPending,
	}
}

func assertPersonalSeekDBProjectionIsScoped(t *testing.T, textStore, vectorStore *Store) {
	t.Helper()
	ctx := t.Context()
	global, err := textStore.CreatePersonalMemoryContext(
		ctx, "experience", Scope{Type: "global"}, "星空巡礼时一起看见了流星", 9000,
	)
	if err != nil {
		t.Fatal(err)
	}
	character, err := vectorStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "character", CharacterID: personalCharacterA},
		"星空巡礼是我们共同的约定", 9300,
	)
	if err != nil {
		t.Fatal(err)
	}
	other, err := textStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "character", CharacterID: personalCharacterB},
		"星空巡礼只属于角色 B", 9300,
	)
	if err != nil {
		t.Fatal(err)
	}
	tombstone, err := textStore.CreatePersonalMemoryContext(
		ctx, "preference", Scope{Type: "global"}, "星空巡礼已失效的偏好", 8000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := textStore.TombstonePersonalMemoryContext(ctx, tombstone.ID); err != nil {
		t.Fatal(err)
	}
	remaining := MaxContentRunes
	retrieved, err := textStore.RetrieveExtractionProjectionContext(
		ctx, personalCharacterA, []string{"星空巡礼"}, &remaining,
	)
	if err != nil {
		t.Fatalf("retrieve extraction projection: %v", err)
	}
	if !personalRetrievedContain(retrieved, global.ID) || !personalRetrievedContain(retrieved, character.ID) {
		t.Fatalf("projection misses scoped records: %#v", retrieved)
	}
	if personalRetrievedContain(retrieved, other.ID) || personalRetrievedContain(retrieved, tombstone.ID) {
		t.Fatalf("projection leaked status/scope-isolated records: %#v", retrieved)
	}
	for _, record := range retrieved {
		if record.Scope.Type == "unassigned_legacy" ||
			record.Scope.Type == "character" && record.Scope.CharacterID != personalCharacterA {
			t.Fatalf("projection leaked invalid scope: %#v", record)
		}
	}
}

func assertPersonalSeekDBHybridRetrieval(t *testing.T, textStore, vectorStore *Store) {
	t.Helper()
	ctx := t.Context()
	global, err := textStore.CreatePersonalMemoryContext(
		ctx, "profile", Scope{Type: "global"}, "hybrid-global-aurora-token 公开场合先听完再回应", 9100,
	)
	if err != nil {
		t.Fatalf("create hybrid global memory: %v", err)
	}
	characterA, err := vectorStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "character", CharacterID: personalCharacterA},
		"hybrid-character-aurora-token 只属于角色 A 的关系", 9300,
	)
	if err != nil {
		t.Fatalf("create hybrid character A memory: %v", err)
	}
	characterB, err := vectorStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "character", CharacterID: personalCharacterB},
		"hybrid-character-aurora-token 只属于角色 B 的关系", 9900,
	)
	if err != nil {
		t.Fatalf("create hybrid character B memory: %v", err)
	}
	pending, err := textStore.CreatePersonalMemoryContext(
		ctx, "relationship", Scope{Type: "unassigned_legacy"},
		"hybrid-review-aurora-token 待审记忆不得进入召回", 8000,
	)
	if err != nil {
		t.Fatalf("create hybrid needs-review memory: %v", err)
	}

	textGlobal, err := textStore.RetrieveContext(ctx, personalCharacterA, "hybrid-global-aurora-token")
	if err != nil {
		t.Fatalf("text-only global retrieve: %v", err)
	}
	if textGlobal.SemanticStatus != string(embedding.SemanticStatusUnavailable) {
		t.Fatalf("text-only semantic status = %q, want unavailable", textGlobal.SemanticStatus)
	}
	if !personalRetrievedContain(textGlobal.PersonalMemories, global.ID) {
		t.Fatalf("text-only retrieve missed global memory: %#v", textGlobal)
	}

	textCharacter, err := textStore.RetrieveContext(ctx, personalCharacterA, "hybrid-character-aurora-token")
	if err != nil {
		t.Fatalf("text-only character retrieve: %v", err)
	}
	if !personalRetrievedContain(textCharacter.PersonalMemories, characterA.ID) {
		t.Fatalf("text-only retrieve missed character A: %#v", textCharacter)
	}
	if personalRetrievedContain(textCharacter.PersonalMemories, characterB.ID) ||
		personalRetrievedContain(textCharacter.PersonalMemories, pending.ID) {
		t.Fatalf("text-only retrieve leaked character/status records: %#v", textCharacter)
	}

	textPending, err := textStore.RetrieveContext(ctx, personalCharacterA, "hybrid-review-aurora-token")
	if err != nil {
		t.Fatalf("text-only needs-review retrieve: %v", err)
	}
	if personalRetrievedContain(textPending.PersonalMemories, pending.ID) {
		t.Fatalf("text-only retrieve leaked needs-review memory: %#v", textPending)
	}

	vectorOnly, err := vectorStore.RetrieveContext(ctx, personalCharacterA, "zzzzvectorquery")
	if err != nil {
		t.Fatalf("vector-only personal retrieve: %v", err)
	}
	if vectorOnly.SemanticStatus != string(embedding.SemanticStatusUsed) {
		t.Fatalf("vector retrieve semantic status = %q, want used", vectorOnly.SemanticStatus)
	}
	if !personalRetrievedContain(vectorOnly.PersonalMemories, characterA.ID) {
		t.Fatalf("vector retrieve missed in-scope character memory: %#v", vectorOnly)
	}
	if personalRetrievedContain(vectorOnly.PersonalMemories, characterB.ID) ||
		personalRetrievedContain(vectorOnly.PersonalMemories, pending.ID) {
		t.Fatalf("vector retrieve leaked character/status records: %#v", vectorOnly)
	}

	textOnlyVectorQuery, err := textStore.RetrieveContext(ctx, personalCharacterA, "zzzzvectorquery")
	if err != nil {
		t.Fatalf("text-only vector query: %v", err)
	}
	if textOnlyVectorQuery.SemanticStatus != string(embedding.SemanticStatusUnavailable) {
		t.Fatalf("text-only vector-query status = %q, want unavailable", textOnlyVectorQuery.SemanticStatus)
	}
	if personalRetrievedContain(textOnlyVectorQuery.PersonalMemories, characterA.ID) {
		t.Fatalf("text-only mode invented a vector hit: %#v", textOnlyVectorQuery)
	}
}

func assertPersonalSeekDBPortraitIsScopedAndBounded(t *testing.T, store *Store) {
	t.Helper()
	ctx := t.Context()
	for index, input := range []struct {
		kind       string
		scope      Scope
		content    string
		confidence uint16
	}{
		{kind: "profile", scope: Scope{Type: "global"}, content: "画像资料：重视事实", confidence: 9800},
		{kind: "preference", scope: Scope{Type: "global"}, content: "画像偏好一：安静交流", confidence: 9700},
		{kind: "preference", scope: Scope{Type: "global"}, content: "画像偏好二：直接表达", confidence: 9600},
		{kind: "preference", scope: Scope{Type: "global"}, content: "画像偏好三：不得突破每类上限", confidence: 9500},
		{kind: "relationship", scope: Scope{Type: "character", CharacterID: personalCharacterA}, content: "画像关系：与角色 A 互相信任", confidence: 9800},
		{kind: "relationship", scope: Scope{Type: "character", CharacterID: personalCharacterB}, content: "画像关系：角色 B 不得泄漏", confidence: 9999},
	} {
		if _, err := store.CreatePersonalMemoryContext(
			ctx, input.kind, input.scope,
			fmt.Sprintf("%s #%d", input.content, index), input.confidence,
		); err != nil {
			t.Fatalf("seed portrait item %d: %v", index, err)
		}
	}
	portrait, err := store.CompanionPortraitContext(ctx, personalCharacterA)
	if err != nil {
		t.Fatalf("load companion portrait: %v", err)
	}
	if len(portrait.PersonalMemories) == 0 || len(portrait.PersonalMemories) > maxPortraitMemories {
		t.Fatalf("portrait size = %d, want 1..%d: %#v", len(portrait.PersonalMemories), maxPortraitMemories, portrait)
	}
	perKind := make(map[string]int)
	for _, record := range portrait.PersonalMemories {
		perKind[record.Kind]++
		if perKind[record.Kind] > maxPortraitPerKind {
			t.Fatalf("portrait exceeded %q per-kind limit: %#v", record.Kind, portrait)
		}
		if record.Scope.Type == "unassigned_legacy" ||
			record.Scope.Type == "character" && record.Scope.CharacterID != personalCharacterA ||
			strings.Contains(record.Content, "角色 B 不得泄漏") {
			t.Fatalf("portrait leaked scope-isolated record: %#v", record)
		}
	}
}

func assertPersonalSeekDBSummary(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	want := Summary{ReadOnly: true}
	queries := []struct {
		target *int64
		query  string
	}{
		{&want.Conversations, "SELECT COUNT(*) FROM conversations"},
		{&want.ActiveGlobalMemories, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'global' AND review_status = 'ready' AND status = 'active'"},
		{&want.ActiveCharacterMemories, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'character' AND review_status = 'ready' AND status = 'active'"},
		{&want.NeedsReviewMemories, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'unassigned_legacy' AND review_status = 'needs_review' AND status = 'active'"},
		{&want.PendingExtractionTurns, "SELECT COUNT(*) FROM conversation_turns WHERE status = 'completed' AND extraction_state = 'pending'"},
		{&want.RunningBatches, "SELECT COUNT(DISTINCT extraction_claim_id) FROM conversation_turns WHERE status = 'completed' AND extraction_state = 'claimed'"},
		{&want.FailedBatches, "SELECT COUNT(*) FROM conversation_turns WHERE status = 'completed' AND extraction_state = 'failed'"},
	}
	for _, item := range queries {
		if err := database.QueryRowContext(t.Context(), item.query).Scan(item.target); err != nil {
			t.Fatalf("load expected summary: %v", err)
		}
	}
	got, err := store.SummaryContext(t.Context())
	if err != nil {
		t.Fatalf("load SeekDB summary: %v", err)
	}
	if got != want {
		t.Fatalf("SeekDB summary = %#v, want %#v", got, want)
	}
}

func assertPersonalSeekDBProviderFailureWritesNothing(
	t *testing.T,
	database *sql.DB,
	textStore *Store,
	queryLimit time.Duration,
) {
	t.Helper()
	failing := newPersonalSeekDBStore(
		t, database, queryLimit,
		personalIntegrationFailingEmbedder{err: errors.New("personal integration provider failed")},
	)
	const createContent = "provider 失败不得落库"
	if _, err := failing.CreatePersonalMemoryContext(
		t.Context(), "preference", Scope{Type: "global"}, createContent, 9000,
	); err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("create with failing provider error = %v", err)
	}
	assertPersonalContentCount(t, database, createContent, 0)

	base, err := textStore.CreatePersonalMemoryContext(
		t.Context(), "experience", Scope{Type: "global"}, "provider 修订前仍保持 active", 9000,
	)
	if err != nil {
		t.Fatal(err)
	}
	const revisionContent = "provider 失败后的修订不得落库"
	if _, err := failing.RevisePersonalMemoryContext(
		t.Context(), base.ID, revisionContent, 9100,
	); err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("revise with failing provider error = %v", err)
	}
	assertPersonalStatus(t, database, base.ID, "active")
	assertPersonalContentCount(t, database, revisionContent, 0)
}

func assertPersonalSeekDBCancellationWritesNothing(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const content = "取消请求不得写入"
	if _, err := store.CreatePersonalMemoryContext(
		ctx, "profile", Scope{Type: "global"}, content, 8000,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled create error = %v, want context.Canceled", err)
	}
	assertPersonalContentCount(t, database, content, 0)
	if _, err := store.PersonalMemoryCatalogContext(ctx, personalCharacterA); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog error = %v, want context.Canceled", err)
	}
	if _, err := store.SummaryContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled summary error = %v, want context.Canceled", err)
	}
}

func assertPersonalSeekDBConcurrentRevisionIsAtomic(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	base, err := store.CreatePersonalMemoryContext(
		t.Context(), "profile", Scope{Type: "global"}, "并发修订的原始记忆", 9000,
	)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type outcome struct {
		record Record
		err    error
	}
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 1; index <= 2; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			record, err := store.RevisePersonalMemoryContext(
				context.Background(), base.ID, fmt.Sprintf("并发修订候选 %d", index), uint16(9100+index),
			)
			outcomes <- outcome{record: record, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)
	succeeded := 0
	winnerID := ""
	for result := range outcomes {
		if result.err == nil {
			succeeded++
			winnerID = result.record.ID
		}
	}
	if succeeded != 1 || winnerID == "" {
		t.Fatalf("concurrent revision successes = %d, winner=%q", succeeded, winnerID)
	}
	assertPersonalStatus(t, database, base.ID, "superseded")
	var activeRevisions int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM personal_memories
WHERE supersedes_id = ? AND status = 'active'`, base.ID).Scan(&activeRevisions); err != nil {
		t.Fatal(err)
	}
	if activeRevisions != 1 {
		t.Fatalf("active concurrent revisions = %d, want 1", activeRevisions)
	}
}

func seedPersonalIntegrationAuthority(t testing.TB, database *sql.DB) {
	t.Helper()
	for _, conversation := range []struct {
		id, characterID string
		updatedAt       int64
	}{
		{id: personalConversationA, characterID: personalCharacterA, updatedAt: 1_786_100_000_100},
		{id: personalConversationB, characterID: personalCharacterB, updatedAt: 1_786_100_000_200},
		{id: personalConversationC, characterID: personalCharacterA, updatedAt: 1_786_100_000_300},
	} {
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`,
			conversation.id, conversation.characterID,
			conversation.updatedAt, conversation.updatedAt,
		); err != nil {
			t.Fatalf("seed personal conversation %q: %v", conversation.id, err)
		}
	}
	turns := []struct {
		id, conversationID, state string
		sequence                  int64
		claimID, owner            any
		lease                     any
		attempt, nextAttempt      int64
		errorCode, errorMessage   any
		createdAt                 int64
	}{
		{id: personalTurnA, conversationID: personalConversationA, state: "pending", sequence: 1, attempt: 0, nextAttempt: 0, createdAt: 1_786_100_000_101},
		{id: personalTurnB, conversationID: personalConversationB, state: "failed", sequence: 1, attempt: 3, nextAttempt: 0, errorCode: "permanent", errorMessage: "permanent failure", createdAt: 1_786_100_000_201},
		{id: personalTurnC, conversationID: personalConversationC, state: "claimed", sequence: 1, claimID: "personal-claimed-batch", owner: "personal-worker", lease: uint64(time.Now().Add(time.Hour).UnixMilli()), attempt: 1, nextAttempt: 0, createdAt: 1_786_100_000_301},
	}
	for _, turn := range turns {
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, ?, 'completed', 'user', NULL, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			turn.id, turn.conversationID, turn.sequence, turn.state,
			turn.claimID, turn.owner, turn.lease, turn.attempt, turn.nextAttempt,
			turn.errorCode, turn.errorMessage, turn.createdAt, turn.createdAt,
		); err != nil {
			t.Fatalf("seed personal turn %q: %v", turn.id, err)
		}
	}
}

func assertPersonalEmbeddingNull(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	var spaceNull, hashNull, vectorNull bool
	if err := database.QueryRowContext(t.Context(), `
SELECT embedding_space_id IS NULL, embedding_content_hash IS NULL, embedding IS NULL
FROM personal_memories WHERE id = ?`, id).Scan(&spaceNull, &hashNull, &vectorNull); err != nil {
		t.Fatalf("load nullable embedding for %q: %v", id, err)
	}
	if !spaceNull || !hashNull || !vectorNull {
		t.Fatalf("memory %q embedding null tuple = (%v, %v, %v)", id, spaceNull, hashNull, vectorNull)
	}
}

func assertPersonalEmbedding1024(t *testing.T, database *sql.DB, id, content string) {
	t.Helper()
	vector := personalIntegrationVectorLiteral()
	var spaceID, hashHex string
	var present bool
	var distance float64
	if err := database.QueryRowContext(t.Context(), `
SELECT embedding_space_id, HEX(embedding_content_hash), embedding IS NOT NULL,
       COSINE_DISTANCE(embedding, ?)
FROM personal_memories WHERE id = ?`, vector, id).Scan(
		&spaceID, &hashHex, &present, &distance,
	); err != nil {
		t.Fatalf("load embedding tuple for %q: %v", id, err)
	}
	if spaceID != personalIntegrationEmbeddingSpace ||
		hashHex != strings.ToUpper(embedding.ContentHash(content)) || !present ||
		math.Abs(distance) > 1e-6 {
		t.Fatalf("memory %q embedding tuple = (%q, %q, %v, %f)", id, spaceID, hashHex, present, distance)
	}
}

func assertPersonalStatus(t *testing.T, database *sql.DB, id, want string) {
	t.Helper()
	var got string
	if err := database.QueryRowContext(t.Context(),
		"SELECT status FROM personal_memories WHERE id = ?", id,
	).Scan(&got); err != nil {
		t.Fatalf("load memory %q status: %v", id, err)
	}
	if got != want {
		t.Fatalf("memory %q status = %q, want %q", id, got, want)
	}
}

func assertPersonalContentCount(t *testing.T, database *sql.DB, content string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM personal_memories WHERE content = ?", content,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("personal memory count for %q = %d, want %d", content, got, want)
	}
}

func personalRecordsContain(records []Record, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func personalRetrievedContain(records []Retrieved, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

type personalIntegrationEmbedder struct{}

func (personalIntegrationEmbedder) Ready() bool { return true }
func (personalIntegrationEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (personalIntegrationEmbedder) ModelID() string { return personalIntegrationEmbeddingSpace }
func (personalIntegrationEmbedder) Dims() int       { return embedding.Dimensions }
func (personalIntegrationEmbedder) Embed(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedding.Dimensions)
		results[index][0] = 1
	}
	return results, nil
}

type personalIntegrationFailingEmbedder struct{ err error }

func (personalIntegrationFailingEmbedder) Ready() bool { return true }
func (personalIntegrationFailingEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (personalIntegrationFailingEmbedder) ModelID() string { return personalIntegrationEmbeddingSpace }
func (personalIntegrationFailingEmbedder) Dims() int       { return embedding.Dimensions }
func (embedder personalIntegrationFailingEmbedder) Embed([]string) ([][]float32, error) {
	return nil, embedder.err
}

func personalIntegrationVectorLiteral() string {
	values := make([]string, embedding.Dimensions)
	values[0] = "1"
	for index := 1; index < len(values); index++ {
		values[index] = "0"
	}
	return "[" + strings.Join(values, ",") + "]"
}

func newPersonalSeekDBStore(
	t testing.TB,
	database *sql.DB,
	queryLimit time.Duration,
	embedder embedding.SemanticEmbedder,
) *Store {
	t.Helper()
	store, err := NewSeekDBStore(database, queryLimit, embedder)
	if err != nil {
		t.Fatalf("new SeekDB personal store: %v", err)
	}
	return store
}

func openPersonalSeekDB(t testing.TB) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	config := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-personal"),
		Address:       reservePersonalLoopbackAddress(t),
		Database:      seekdb.DefaultDatabase,
		User:          seekdb.DefaultUser,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB personal runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reservePersonalLoopbackAddress(t testing.TB) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func closePersonalSeekDB(t testing.TB, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB personal runtime: %v", err)
	}
}
