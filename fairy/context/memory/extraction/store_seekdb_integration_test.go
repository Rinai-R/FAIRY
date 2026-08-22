//go:build integration

package extraction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"fairy/runtime/seekdb"
	"fairy/runtime/seekdb/seekdbtest"
)

func TestRealSeekDBExtractionCoordinationIsAtomicIsolatedAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openExtractionSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeExtractionSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB extraction schema: %v", err)
	}

	workerA := newExtractionSeekDBStore(t, database, runtimeConfig.QueryLimit, "worker-a", 10*time.Minute)
	workerB := newExtractionSeekDBStore(t, database, runtimeConfig.QueryLimit, "worker-b", 10*time.Minute)

	assertSeekDBEmptyAndMissingExtractionQueues(t, database, workerA)
	assertSeekDBClaimOrderLimitFailureAndCatalog(t, database, workerA, workerB)
	assertSeekDBExpiredLeaseRecovery(t, database, workerA)
	assertSeekDBIncompleteClaimRollsBack(t, database, workerA)
	assertSeekDBConcurrentClaims(t, database, runtimeConfig.QueryLimit)
	assertSeekDBExtractionCancellation(t, database, workerA)
	assertSeekDBExtractionInjectedRollbacks(t, database, workerA)

	const (
		restartConversation = "extraction-restart-conversation"
		restartCharacter    = "extraction-restart-character"
	)
	seedExtractionConversation(t, database, restartConversation, restartCharacter, 1_786_000_900_000)
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: restartConversation,
		turnID:         "extraction-restart-turn",
		sequence:       1,
		state:          "pending",
		createdAt:      1_786_000_900_100,
		withUser:       true,
		withAssistant:  true,
	})
	restartBatch, err := workerA.ClaimExtractionTurnsContext(t.Context(), restartConversation, 1)
	if err != nil || restartBatch == nil {
		t.Fatalf("claim restart batch = (%#v, %v)", restartBatch, err)
	}

	closeExtractionSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	instance, err = seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB extraction runtime: %v", err)
	}
	closed = false
	database = instance.SQL()
	restartedA := newExtractionSeekDBStore(t, database, runtimeConfig.QueryLimit, "worker-a", 10*time.Minute)
	restartedB := newExtractionSeekDBStore(t, database, runtimeConfig.QueryLimit, "worker-b", 10*time.Minute)

	catalog, err := restartedA.ExtractionBatchCatalogContext(t.Context(), restartCharacter)
	if err != nil {
		t.Fatalf("catalog after restart: %v", err)
	}
	if len(catalog.Running) != 1 || catalog.Running[0].ID != restartBatch.BatchID ||
		catalog.Running[0].ConversationID != restartConversation {
		t.Fatalf("running catalog after restart = %#v, want batch %q", catalog.Running, restartBatch.BatchID)
	}
	blocked, err := restartedB.ClaimExtractionTurnsContext(t.Context(), restartConversation, 1)
	if err != nil || blocked != nil {
		t.Fatalf("claim under persisted active lease = (%#v, %v), want nil, nil", blocked, err)
	}
}

func assertSeekDBEmptyAndMissingExtractionQueues(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	const conversationID = "extraction-empty-conversation"
	seedExtractionConversation(t, database, conversationID, "extraction-empty-character", 1_786_000_800_000)
	count, err := store.PendingExtractionTurnCountContext(t.Context(), conversationID)
	if err != nil || count != 0 {
		t.Fatalf("empty pending count = (%d, %v), want 0, nil", count, err)
	}
	batch, err := store.ClaimExtractionTurnsContext(t.Context(), conversationID, 1)
	if err != nil || batch != nil {
		t.Fatalf("empty claim = (%#v, %v), want nil, nil", batch, err)
	}
	batch, err = store.ClaimExtractionTurnsContext(t.Context(), "extraction-missing-conversation", 1)
	if err == nil || batch != nil {
		t.Fatalf("missing conversation claim = (%#v, %v), want nil and error", batch, err)
	}
}

func assertSeekDBClaimOrderLimitFailureAndCatalog(
	t *testing.T,
	database *sql.DB,
	workerA, workerB *Store,
) {
	t.Helper()
	const (
		conversationID = "extraction-primary-conversation"
		characterID    = "extraction-primary-character"
	)
	seedExtractionConversation(t, database, conversationID, characterID, 1_786_000_000_000)
	for sequence := 1; sequence <= 3; sequence++ {
		seedExtractionTurn(t, database, extractionTurnFixture{
			conversationID: conversationID,
			turnID:         fmt.Sprintf("extraction-primary-turn-%d", sequence),
			sequence:       int64(sequence),
			state:          "pending",
			createdAt:      1_786_000_000_000 + int64(sequence),
			withUser:       true,
			withAssistant:  true,
		})
	}

	count, err := workerA.PendingExtractionTurnCountContext(t.Context(), conversationID)
	if err != nil || count != 3 {
		t.Fatalf("initial pending count = (%d, %v), want 3", count, err)
	}
	batch, err := workerA.ClaimExtractionTurnsContext(t.Context(), conversationID, 2)
	if err != nil {
		t.Fatalf("claim extraction turns: %v", err)
	}
	if batch == nil || batch.BatchID == "" || batch.ConversationID != conversationID ||
		batch.CharacterID != characterID || len(batch.Turns) != 2 {
		t.Fatalf("claimed batch = %#v", batch)
	}
	for index, turn := range batch.Turns {
		sequence := index + 1
		if turn.TurnID != fmt.Sprintf("extraction-primary-turn-%d", sequence) ||
			turn.UserMessage != fmt.Sprintf("user-%d", sequence) ||
			turn.AssistantMessage != fmt.Sprintf("assistant-%d", sequence) {
			t.Fatalf("claimed turn[%d] = %#v", index, turn)
		}
	}
	count, err = workerA.PendingExtractionTurnCountContext(t.Context(), conversationID)
	if err != nil || count != 1 {
		t.Fatalf("pending count after claim = (%d, %v), want 1", count, err)
	}
	blocked, err := workerB.ClaimExtractionTurnsContext(t.Context(), conversationID, 1)
	if err != nil || blocked != nil {
		t.Fatalf("second worker claim under active lease = (%#v, %v), want nil, nil", blocked, err)
	}

	catalog, err := workerA.ExtractionBatchCatalogContext(t.Context(), characterID)
	if err != nil {
		t.Fatalf("running catalog: %v", err)
	}
	if len(catalog.Running) != 1 || catalog.Running[0].ID != batch.BatchID ||
		catalog.Running[0].FirstTurnSequence != 1 || catalog.Running[0].LastTurnSequence != 2 ||
		len(catalog.Failed) != 0 {
		t.Fatalf("catalog after claim = %#v", catalog)
	}

	beforeWrongOwner := loadExtractionTurnSnapshot(t, database, "extraction-primary-turn-1")
	if err := workerB.FailExtractionBatchContext(
		t.Context(), batch.BatchID, "wrong_worker", "must not mutate", true,
	); err == nil {
		t.Fatal("wrong worker failure unexpectedly succeeded")
	}
	afterWrongOwner := loadExtractionTurnSnapshot(t, database, "extraction-primary-turn-1")
	if afterWrongOwner != beforeWrongOwner {
		t.Fatalf("wrong owner changed turn: before=%#v after=%#v", beforeWrongOwner, afterWrongOwner)
	}

	if err := workerA.FailExtractionBatchContext(
		t.Context(), batch.BatchID, "model_failed", "model failed\x00with secret", true,
	); err != nil {
		t.Fatalf("retryable batch failure: %v", err)
	}
	for sequence := 1; sequence <= 2; sequence++ {
		snapshot := loadExtractionTurnSnapshot(t, database, fmt.Sprintf("extraction-primary-turn-%d", sequence))
		if snapshot.state != "pending" || snapshot.claimID.Valid || snapshot.owner.Valid ||
			snapshot.lease.Valid || snapshot.attempt != 1 || snapshot.nextAttempt <= snapshot.updatedAt ||
			snapshot.errorCode.String != "model_failed" || snapshot.errorMessage.String != "model failed with secret" {
			t.Fatalf("turn %d after retryable failure = %#v", sequence, snapshot)
		}
	}

	if _, err := database.ExecContext(t.Context(), `
UPDATE conversation_turns SET extraction_next_attempt_at_ms = 0
WHERE conversation_id = ? AND sequence <= 2`, conversationID); err != nil {
		t.Fatalf("make retryable extraction turns due: %v", err)
	}
	reclaimed, err := workerB.ClaimExtractionTurnsContext(t.Context(), conversationID, 2)
	if err != nil || reclaimed == nil || len(reclaimed.Turns) != 2 {
		t.Fatalf("reclaim retryable batch = (%#v, %v)", reclaimed, err)
	}
	if err := workerB.FailExtractionBatchContext(
		t.Context(), reclaimed.BatchID, "permanent", "permanent failure", false,
	); err != nil {
		t.Fatalf("non-retryable batch failure: %v", err)
	}
	catalog, err = workerA.ExtractionBatchCatalogContext(t.Context(), characterID)
	if err != nil {
		t.Fatalf("failed catalog: %v", err)
	}
	if len(catalog.Running) != 0 || len(catalog.Failed) != 2 {
		t.Fatalf("catalog after permanent failure = %#v", catalog)
	}
	if catalog.Failed[0].Error == nil || catalog.Failed[0].Error.Code != "permanent" {
		t.Fatalf("failed catalog error = %#v", catalog.Failed[0])
	}

	retryTurnID := catalog.Failed[0].ID
	if err := workerA.RetryExtractionBatchContext(t.Context(), retryTurnID); err != nil {
		t.Fatalf("retry failed extraction turn: %v", err)
	}
	retried := loadExtractionTurnSnapshot(t, database, retryTurnID)
	if retried.state != "pending" || retried.attempt != 0 || retried.nextAttempt != 0 ||
		retried.errorCode.Valid || retried.errorMessage.Valid {
		t.Fatalf("retried turn = %#v", retried)
	}
	if err := workerA.RetryExtractionBatchContext(t.Context(), retryTurnID); err == nil {
		t.Fatal("retrying a non-failed turn unexpectedly succeeded")
	}
}

func assertSeekDBExpiredLeaseRecovery(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	const (
		conversationID = "extraction-expired-conversation"
		characterID    = "extraction-expired-character"
		otherID        = "extraction-expired-isolated-conversation"
	)
	seedExtractionConversation(t, database, conversationID, characterID, 1_786_000_100_000)
	seedExtractionConversation(t, database, otherID, "extraction-expired-isolated-character", 1_786_000_100_100)
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: conversationID,
		turnID:         "extraction-expired-recoverable",
		sequence:       1,
		state:          "claimed",
		claimID:        "expired-batch-recoverable",
		owner:          "dead-worker",
		lease:          1_786_000_100_100,
		attempt:        1,
		createdAt:      1_786_000_100_001,
		withUser:       true,
		withAssistant:  true,
	})
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: conversationID,
		turnID:         "extraction-expired-max-attempt",
		sequence:       2,
		state:          "claimed",
		claimID:        "expired-batch-max",
		owner:          "dead-worker",
		lease:          1_786_000_100_102,
		attempt:        maxExtractionAttempts,
		createdAt:      1_786_000_100_002,
		withUser:       true,
		withAssistant:  true,
	})
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: otherID,
		turnID:         "extraction-expired-isolated",
		sequence:       1,
		state:          "claimed",
		claimID:        "expired-batch-isolated",
		owner:          "dead-worker",
		lease:          1_786_000_100_200,
		attempt:        1,
		createdAt:      1_786_000_100_101,
		withUser:       true,
		withAssistant:  true,
	})

	batch, err := store.ClaimExtractionTurnsContext(t.Context(), conversationID, 2)
	if err != nil || batch == nil || len(batch.Turns) != 1 ||
		batch.Turns[0].TurnID != "extraction-expired-recoverable" {
		t.Fatalf("claim after lease recovery = (%#v, %v)", batch, err)
	}
	recovered := loadExtractionTurnSnapshot(t, database, "extraction-expired-recoverable")
	if recovered.state != "claimed" || recovered.attempt != 2 ||
		!recovered.claimID.Valid || recovered.claimID.String != batch.BatchID {
		t.Fatalf("recovered turn = %#v", recovered)
	}
	exhausted := loadExtractionTurnSnapshot(t, database, "extraction-expired-max-attempt")
	if exhausted.state != "failed" || exhausted.claimID.Valid || exhausted.owner.Valid || exhausted.lease.Valid ||
		exhausted.errorCode.String != "lease_expired" {
		t.Fatalf("maximum-attempt expired turn = %#v", exhausted)
	}
	isolated := loadExtractionTurnSnapshot(t, database, "extraction-expired-isolated")
	if isolated.state != "claimed" || isolated.claimID.String != "expired-batch-isolated" {
		t.Fatalf("claim recovered another conversation = %#v", isolated)
	}

	const exhaustedOnlyConversation = "extraction-expired-exhausted-only-conversation"
	seedExtractionConversation(
		t, database, exhaustedOnlyConversation,
		"extraction-expired-exhausted-only-character", 1_786_000_100_300,
	)
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: exhaustedOnlyConversation,
		turnID:         "extraction-expired-exhausted-only-turn",
		sequence:       1,
		state:          "claimed",
		claimID:        "extraction-expired-exhausted-only-batch",
		owner:          "dead-worker",
		lease:          1_786_000_100_302,
		attempt:        maxExtractionAttempts,
		createdAt:      1_786_000_100_301,
		withUser:       true,
		withAssistant:  true,
	})
	if batch, err := store.ClaimExtractionTurnsContext(t.Context(), exhaustedOnlyConversation, 1); err != nil || batch != nil {
		t.Fatalf("claim exhausted-only conversation = (%#v, %v), want nil, nil", batch, err)
	}
	exhaustedOnly := loadExtractionTurnSnapshot(t, database, "extraction-expired-exhausted-only-turn")
	if exhaustedOnly.state != "failed" || exhaustedOnly.claimID.Valid || exhaustedOnly.owner.Valid ||
		exhaustedOnly.lease.Valid || exhaustedOnly.errorCode.String != "lease_expired" {
		t.Fatalf("exhausted-only recovery was not committed = %#v", exhaustedOnly)
	}
}

func assertSeekDBIncompleteClaimRollsBack(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	const conversationID = "extraction-incomplete-conversation"
	seedExtractionConversation(t, database, conversationID, "extraction-incomplete-character", 1_786_000_200_000)
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: conversationID,
		turnID:         "extraction-incomplete-turn",
		sequence:       1,
		state:          "pending",
		createdAt:      1_786_000_200_001,
		withUser:       true,
		withAssistant:  false,
	})
	before := loadExtractionTurnSnapshot(t, database, "extraction-incomplete-turn")
	if batch, err := store.ClaimExtractionTurnsContext(t.Context(), conversationID, 1); err == nil || batch != nil {
		t.Fatalf("incomplete turn claim = (%#v, %v), want nil and error", batch, err)
	}
	after := loadExtractionTurnSnapshot(t, database, "extraction-incomplete-turn")
	if after != before {
		t.Fatalf("incomplete claim partially committed: before=%#v after=%#v", before, after)
	}
}

func assertSeekDBConcurrentClaims(t *testing.T, database *sql.DB, queryLimit time.Duration) {
	t.Helper()
	const (
		conversationID = "extraction-concurrent-conversation"
		characterID    = "extraction-concurrent-character"
		writers        = 6
		turns          = 4
	)
	seedExtractionConversation(t, database, conversationID, characterID, 1_786_000_300_000)
	for sequence := 1; sequence <= turns; sequence++ {
		seedExtractionTurn(t, database, extractionTurnFixture{
			conversationID: conversationID,
			turnID:         fmt.Sprintf("extraction-concurrent-turn-%d", sequence),
			sequence:       int64(sequence),
			state:          "pending",
			createdAt:      1_786_000_300_000 + int64(sequence),
			withUser:       true,
			withAssistant:  true,
		})
	}
	stores := make([]*Store, writers)
	for index := range stores {
		stores[index] = newExtractionSeekDBStore(
			t, database, queryLimit, fmt.Sprintf("concurrent-worker-%d", index), 10*time.Minute,
		)
	}
	start := make(chan struct{})
	results := make(chan *ClaimedBatch, writers)
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for _, store := range stores {
		store := store
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			batch, err := store.ClaimExtractionTurnsContext(t.Context(), conversationID, DefaultBatchLimit)
			results <- batch
			errorsByWriter <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	nonEmpty := 0
	var winner *ClaimedBatch
	for batch := range results {
		if batch != nil {
			nonEmpty++
			winner = batch
		}
	}
	if nonEmpty != 1 || winner == nil || len(winner.Turns) != turns {
		t.Fatalf("concurrent winners = %d, winner=%#v", nonEmpty, winner)
	}
	var distinctClaims, claimedTurns int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(DISTINCT extraction_claim_id), COUNT(*)
FROM conversation_turns WHERE conversation_id = ? AND extraction_state = 'claimed'`, conversationID,
	).Scan(&distinctClaims, &claimedTurns); err != nil {
		t.Fatalf("count concurrent claims: %v", err)
	}
	if distinctClaims != 1 || claimedTurns != turns {
		t.Fatalf("concurrent claimed rows = (%d claims, %d turns)", distinctClaims, claimedTurns)
	}

	const isolatedWriters = 3
	isolatedResults := make(chan *ClaimedBatch, isolatedWriters)
	isolatedErrors := make(chan error, isolatedWriters)
	start = make(chan struct{})
	wait = sync.WaitGroup{}
	for index := 0; index < isolatedWriters; index++ {
		conversation := fmt.Sprintf("extraction-parallel-conversation-%d", index)
		character := fmt.Sprintf("extraction-parallel-character-%d", index)
		seedExtractionConversation(t, database, conversation, character, 1_786_000_310_000+int64(index))
		seedExtractionTurn(t, database, extractionTurnFixture{
			conversationID: conversation,
			turnID:         fmt.Sprintf("extraction-parallel-turn-%d", index),
			sequence:       1,
			state:          "pending",
			createdAt:      1_786_000_310_100 + int64(index),
			withUser:       true,
			withAssistant:  true,
		})
		store := stores[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			batch, err := store.ClaimExtractionTurnsContext(t.Context(), conversation, 1)
			isolatedResults <- batch
			isolatedErrors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(isolatedResults)
	close(isolatedErrors)
	for err := range isolatedErrors {
		if err != nil {
			t.Fatalf("isolated concurrent claim: %v", err)
		}
	}
	for batch := range isolatedResults {
		if batch == nil || len(batch.Turns) != 1 {
			t.Fatalf("isolated concurrent result = %#v", batch)
		}
	}
}

func assertSeekDBExtractionCancellation(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	const (
		conversationID = "extraction-cancel-conversation"
		characterID    = "extraction-cancel-character"
	)
	seedExtractionConversation(t, database, conversationID, characterID, 1_786_000_400_000)
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: conversationID,
		turnID:         "extraction-cancel-pending",
		sequence:       1,
		state:          "pending",
		createdAt:      1_786_000_400_001,
		withUser:       true,
		withAssistant:  true,
	})
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: conversationID,
		turnID:         "extraction-cancel-claimed",
		sequence:       2,
		state:          "claimed",
		claimID:        "extraction-cancel-batch",
		owner:          "worker-a",
		lease:          uint64(time.Now().Add(time.Hour).UnixMilli()),
		attempt:        1,
		createdAt:      1_786_000_400_002,
		withUser:       true,
		withAssistant:  true,
	})
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: conversationID,
		turnID:         "extraction-cancel-failed",
		sequence:       3,
		state:          "failed",
		attempt:        maxExtractionAttempts,
		errorCode:      "permanent",
		errorMessage:   "permanent failure",
		createdAt:      1_786_000_400_003,
		withUser:       true,
		withAssistant:  true,
	})
	before := loadExtractionConversationSnapshots(t, database, conversationID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assertCanceled := func(name string, call func() error) {
		t.Helper()
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s error = %v, want context.Canceled", name, err)
		}
	}
	assertCanceled("pending", func() error {
		_, err := store.PendingExtractionTurnCountContext(ctx, conversationID)
		return err
	})
	assertCanceled("claim", func() error {
		_, err := store.ClaimExtractionTurnsContext(ctx, conversationID, 1)
		return err
	})
	assertCanceled("fail", func() error {
		return store.FailExtractionBatchContext(ctx, "extraction-cancel-batch", "cancel", "cancel", false)
	})
	assertCanceled("catalog", func() error {
		_, err := store.ExtractionBatchCatalogContext(ctx, characterID)
		return err
	})
	assertCanceled("retry", func() error {
		return store.RetryExtractionBatchContext(ctx, "extraction-cancel-failed")
	})
	after := loadExtractionConversationSnapshots(t, database, conversationID)
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("canceled operations changed rows: before=%#v after=%#v", before, after)
	}
}

func assertSeekDBExtractionInjectedRollbacks(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	sentinel := errors.New("injected extraction coordinator failure")
	t.Cleanup(func() { store.seekDBWriteHook = nil })

	claimStages := []seekDBWriteStage{
		seekDBStageClaimAfterRecovery,
		seekDBStageClaimAfterClaim,
		seekDBStageClaimBeforeCommit,
	}
	for index, stage := range claimStages {
		conversationID := fmt.Sprintf("extraction-rollback-claim-conversation-%d", index)
		turnID := fmt.Sprintf("extraction-rollback-claim-turn-%d", index)
		seedExtractionConversation(
			t, database, conversationID,
			fmt.Sprintf("extraction-rollback-claim-character-%d", index),
			1_786_000_500_000+int64(index*10),
		)
		fixture := extractionTurnFixture{
			conversationID: conversationID,
			turnID:         turnID,
			sequence:       1,
			state:          "pending",
			createdAt:      1_786_000_500_001 + int64(index*10),
			withUser:       true,
			withAssistant:  true,
		}
		if stage == seekDBStageClaimAfterRecovery {
			fixture.state = "claimed"
			fixture.claimID = fmt.Sprintf("extraction-rollback-expired-batch-%d", index)
			fixture.owner = "expired-worker"
			fixture.lease = uint64(fixture.createdAt + 1)
			fixture.attempt = 1
		}
		seedExtractionTurn(t, database, fixture)
		before := loadExtractionTurnSnapshot(t, database, turnID)
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return sentinel
			}
			return nil
		}
		batch, err := store.ClaimExtractionTurnsContext(t.Context(), conversationID, 1)
		store.seekDBWriteHook = nil
		if batch != nil || !errors.Is(err, sentinel) {
			t.Fatalf("claim hook %q = (%#v, %v), want nil and sentinel", stage, batch, err)
		}
		after := loadExtractionTurnSnapshot(t, database, turnID)
		if after != before {
			t.Fatalf("claim hook %q partially committed: before=%#v after=%#v", stage, before, after)
		}
	}

	failStages := []seekDBWriteStage{
		seekDBStageFailAfterUpdate,
		seekDBStageFailBeforeCommit,
	}
	for index, stage := range failStages {
		conversationID := fmt.Sprintf("extraction-rollback-fail-conversation-%d", index)
		turnID := fmt.Sprintf("extraction-rollback-fail-turn-%d", index)
		batchID := fmt.Sprintf("extraction-rollback-fail-batch-%d", index)
		createdAt := int64(1_786_000_600_000 + index*10)
		seedExtractionConversation(
			t, database, conversationID,
			fmt.Sprintf("extraction-rollback-fail-character-%d", index), createdAt,
		)
		seedExtractionTurn(t, database, extractionTurnFixture{
			conversationID: conversationID,
			turnID:         turnID,
			sequence:       1,
			state:          "claimed",
			claimID:        batchID,
			owner:          "worker-a",
			lease:          uint64(time.Now().Add(time.Hour).UnixMilli()),
			attempt:        1,
			createdAt:      createdAt + 1,
			withUser:       true,
			withAssistant:  true,
		})
		before := loadExtractionTurnSnapshot(t, database, turnID)
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return sentinel
			}
			return nil
		}
		err := store.FailExtractionBatchContext(
			t.Context(), batchID, "injected", "must roll back", true,
		)
		store.seekDBWriteHook = nil
		if !errors.Is(err, sentinel) {
			t.Fatalf("fail hook %q error = %v, want sentinel", stage, err)
		}
		after := loadExtractionTurnSnapshot(t, database, turnID)
		if after != before {
			t.Fatalf("fail hook %q partially committed: before=%#v after=%#v", stage, before, after)
		}
	}

	retryStages := []seekDBWriteStage{
		seekDBStageRetryAfterUpdate,
		seekDBStageRetryBeforeCommit,
	}
	for index, stage := range retryStages {
		conversationID := fmt.Sprintf("extraction-rollback-retry-conversation-%d", index)
		turnID := fmt.Sprintf("extraction-rollback-retry-turn-%d", index)
		createdAt := int64(1_786_000_700_000 + index*10)
		seedExtractionConversation(
			t, database, conversationID,
			fmt.Sprintf("extraction-rollback-retry-character-%d", index), createdAt,
		)
		seedExtractionTurn(t, database, extractionTurnFixture{
			conversationID: conversationID,
			turnID:         turnID,
			sequence:       1,
			state:          "failed",
			attempt:        maxExtractionAttempts,
			errorCode:      "permanent",
			errorMessage:   "permanent failure",
			createdAt:      createdAt + 1,
			withUser:       true,
			withAssistant:  true,
		})
		before := loadExtractionTurnSnapshot(t, database, turnID)
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return sentinel
			}
			return nil
		}
		err := store.RetryExtractionBatchContext(t.Context(), turnID)
		store.seekDBWriteHook = nil
		if !errors.Is(err, sentinel) {
			t.Fatalf("retry hook %q error = %v, want sentinel", stage, err)
		}
		after := loadExtractionTurnSnapshot(t, database, turnID)
		if after != before {
			t.Fatalf("retry hook %q partially committed: before=%#v after=%#v", stage, before, after)
		}
	}
}

type extractionTurnFixture struct {
	conversationID string
	turnID         string
	sequence       int64
	state          string
	claimID        string
	owner          string
	lease          uint64
	attempt        int
	nextAttempt    uint64
	errorCode      string
	errorMessage   string
	createdAt      int64
	withUser       bool
	withAssistant  bool
}

func seedExtractionConversation(
	t *testing.T,
	database *sql.DB,
	conversationID, characterID string,
	createdAt int64,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`, conversationID, characterID, createdAt, createdAt); err != nil {
		t.Fatalf("seed extraction conversation %q: %v", conversationID, err)
	}
}

func seedExtractionTurn(t *testing.T, database *sql.DB, fixture extractionTurnFixture) {
	t.Helper()
	var claimID, owner any
	var lease any
	if fixture.state == "claimed" {
		claimID, owner, lease = fixture.claimID, fixture.owner, fixture.lease
	}
	var errorCode, errorMessage any
	if fixture.errorCode != "" || fixture.errorMessage != "" {
		errorCode, errorMessage = fixture.errorCode, fixture.errorMessage
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms, extraction_error_code, extraction_error_message,
  created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, ?, 'completed', 'user', NULL, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fixture.turnID, fixture.conversationID, fixture.sequence,
		fixture.state, claimID, owner, lease, fixture.attempt, fixture.nextAttempt,
		errorCode, errorMessage, fixture.createdAt, fixture.createdAt,
	); err != nil {
		t.Fatalf("seed extraction turn %q: %v", fixture.turnID, err)
	}
	messageSequence := fixture.sequence*2 - 1
	if fixture.withUser {
		seedExtractionMessage(
			t, database, fixture.conversationID, fixture.turnID,
			fixture.turnID+"-user", messageSequence, "user",
			fmt.Sprintf("user-%d", fixture.sequence), fixture.createdAt,
		)
	}
	if fixture.withAssistant {
		seedExtractionMessage(
			t, database, fixture.conversationID, fixture.turnID,
			fixture.turnID+"-assistant", messageSequence+1, "assistant",
			fmt.Sprintf("assistant-%d", fixture.sequence), fixture.createdAt+1,
		)
	}
}

func seedExtractionMessage(
	t *testing.T,
	database *sql.DB,
	conversationID, turnID, messageID string,
	sequence int64,
	role, content string,
	createdAt int64,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_messages(
  id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, JSON_ARRAY(), ?)`,
		messageID, conversationID, turnID, sequence, role, content, createdAt,
	); err != nil {
		t.Fatalf("seed extraction message %q: %v", messageID, err)
	}
}

type extractionTurnSnapshot struct {
	turnID       string
	state        string
	claimID      sql.NullString
	owner        sql.NullString
	lease        sql.NullInt64
	attempt      uint64
	nextAttempt  uint64
	errorCode    sql.NullString
	errorMessage sql.NullString
	updatedAt    uint64
}

func loadExtractionTurnSnapshot(t *testing.T, database *sql.DB, turnID string) extractionTurnSnapshot {
	t.Helper()
	var snapshot extractionTurnSnapshot
	if err := database.QueryRowContext(t.Context(), `
SELECT id, extraction_state, extraction_claim_id, extraction_lease_owner,
  extraction_lease_expires_at_ms, extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, updated_at_ms
FROM conversation_turns WHERE id = ?`, turnID).Scan(
		&snapshot.turnID, &snapshot.state, &snapshot.claimID, &snapshot.owner,
		&snapshot.lease, &snapshot.attempt, &snapshot.nextAttempt,
		&snapshot.errorCode, &snapshot.errorMessage, &snapshot.updatedAt,
	); err != nil {
		t.Fatalf("load extraction turn %q: %v", turnID, err)
	}
	return snapshot
}

func loadExtractionConversationSnapshots(
	t *testing.T,
	database *sql.DB,
	conversationID string,
) []extractionTurnSnapshot {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `
SELECT id, extraction_state, extraction_claim_id, extraction_lease_owner,
  extraction_lease_expires_at_ms, extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, updated_at_ms
FROM conversation_turns WHERE conversation_id = ? ORDER BY sequence`, conversationID)
	if err != nil {
		t.Fatalf("load extraction conversation %q: %v", conversationID, err)
	}
	defer rows.Close()
	var snapshots []extractionTurnSnapshot
	for rows.Next() {
		var snapshot extractionTurnSnapshot
		if err := rows.Scan(
			&snapshot.turnID, &snapshot.state, &snapshot.claimID, &snapshot.owner,
			&snapshot.lease, &snapshot.attempt, &snapshot.nextAttempt,
			&snapshot.errorCode, &snapshot.errorMessage, &snapshot.updatedAt,
		); err != nil {
			t.Fatalf("scan extraction conversation %q: %v", conversationID, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate extraction conversation %q: %v", conversationID, err)
	}
	return snapshots
}

func newExtractionSeekDBStore(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	workerID string,
	lease time.Duration,
) *Store {
	t.Helper()
	store, err := NewSeekDBStore(database, queryLimit, workerID, lease)
	if err != nil {
		t.Fatalf("new SeekDB extraction store %q: %v", workerID, err)
	}
	return store
}

func openExtractionSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:   library,
		DataDir:       seekdbtest.DataDir(t),
		Database:      seekdb.DefaultDatabase,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB extraction runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveExtractionLoopbackAddress(t *testing.T) string {
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

func closeExtractionSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB extraction runtime: %v", err)
	}
}
