//go:build integration

package extraction

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/context/memory/personal"
	"fairy/runtime/embedding"
	"fairy/runtime/seekdb"
)

const extractionSettlementEmbeddingSpace = "fairy-extraction-settlement-1024"

func TestRealSeekDBExtractionSettlementIsAtomicIsolatedAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openExtractionSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeExtractionSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB extraction settlement schema: %v", err)
	}

	personalStore := newExtractionSettlementPersonalStore(t, database, runtimeConfig.QueryLimit, nil)
	store := newExtractionSettlementStore(
		t, database, runtimeConfig.QueryLimit, "settlement-worker", personalStore,
	)

	assertSeekDBSettlementMutationParityAndCoverage(t, database, personalStore, store)
	assertSeekDBSettlementMergesRepeatedCoverageWithAppliedPrecedence(t, database, store)
	assertSeekDBSettlementEmptyMutations(t, database, store)
	assertSeekDBSettlementDuplicateParitySkipsProvider(t, database, personalStore, store)
	assertSeekDBSettlementRevalidatesBlockedProviderPhantom(t, database, personalStore, store)
	assertSeekDBSettlementRejectsBlockedProviderTombstone(t, database, personalStore, store)
	assertSeekDBSettlementUsesNewestDuplicateWinner(t, database, personalStore, store)
	assertSeekDBSettlementUnifiesHistoricalAndCreatedDuplicateWinners(t, database, personalStore, store)
	assertSeekDBSettlementIgnoresNormalizedHashCollisions(t, database, personalStore, store)
	assertSeekDBSettlementRejectsUntrustedInputs(t, database, personalStore, store)
	assertSeekDBSettlementLocksClaimedMessageEvidence(t, database, store)
	assertSeekDBSettlementHooksRollBackEverything(t, database, personalStore, store)
	restartFixture := assertSeekDBSettlementReplaySkipsProvider(t, database, personalStore, store)
	assertSeekDBSettlementConcurrentCommitHasOneWinner(t, database, store)
	assertSeekDBSettlementSerializesDifferentClaimsOnOneTuple(t, database, personalStore, store)
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("verify idempotent current SeekDB schema before settlement restart: %v", err)
	}
	if status, err := seekdb.CheckSchema(
		t.Context(), database, seekdb.CurrentSchemaRevision(),
	); err != nil || status.State != seekdb.SchemaCurrent {
		t.Fatalf("SeekDB schema readiness before settlement restart = (%#v, %v)", status, err)
	}

	closeExtractionSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	var err error
	instance, err = seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB extraction settlement runtime: %v", err)
	}
	closed = false
	database = instance.SQL()
	if status, err := seekdb.CheckSchema(
		t.Context(), database, seekdb.CurrentSchemaRevision(),
	); err != nil || status.State != seekdb.SchemaCurrent {
		t.Fatalf("SeekDB schema readiness after settlement restart = (%#v, %v)", status, err)
	}
	failing := &extractionSettlementEmbedder{err: errors.New("provider must not run after restart")}
	restartedPersonal := newExtractionSettlementPersonalStore(
		t, database, runtimeConfig.QueryLimit, failing,
	)
	restarted := newExtractionSettlementStore(
		t, database, runtimeConfig.QueryLimit, "settlement-worker", restartedPersonal,
	)
	assertSeekDBSettlementSurvivesRestart(t, database, restarted, failing, restartFixture)
}

func assertSeekDBSettlementLocksClaimedMessageEvidence(
	t *testing.T,
	database *sql.DB,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-message-lock",
		"messagelock original user", "messagelock original assistant",
	)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	locked := make(chan struct{})
	release := make(chan struct{})
	store.seekDBWriteHook = func(stage seekDBWriteStage) error {
		if stage == seekDBStageSettleAfterEvidenceLock {
			close(locked)
			<-release
		}
		return nil
	}
	t.Cleanup(func() { store.seekDBWriteHook = nil })

	settled := make(chan error, 1)
	go func() {
		_, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, nil)
		settled <- err
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("settlement did not lock claimed message evidence")
	}

	updated := make(chan error, 1)
	go func() {
		_, err := database.ExecContext(t.Context(), `
UPDATE conversation_messages
SET content = ?
WHERE conversation_id = ? AND turn_id = ? AND role = 'user'`,
			"messagelock concurrent user", fixture.conversationID, fixture.turnID,
		)
		updated <- err
	}()
	select {
	case err := <-updated:
		close(release)
		t.Fatalf("message update completed before settlement released its lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-settled; err != nil {
		t.Fatalf("commit while message update waits: %v", err)
	}
	store.seekDBWriteHook = nil
	if err := <-updated; err != nil {
		t.Fatalf("message update after settlement commit: %v", err)
	}
	assertExtractionSettlementTurnProcessed(t, database, fixture.turnID)
	var content string
	if err := database.QueryRowContext(t.Context(), `
SELECT content FROM conversation_messages
WHERE conversation_id = ? AND turn_id = ? AND role = 'user'`,
		fixture.conversationID, fixture.turnID,
	).Scan(&content); err != nil {
		t.Fatalf("load updated message after settlement: %v", err)
	}
	if content != "messagelock concurrent user" {
		t.Fatalf("message content after lock release = %q", content)
	}
}

func assertSeekDBSettlementMergesRepeatedCoverageWithAppliedPrecedence(
	t *testing.T,
	database *sql.DB,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-coverage-merge",
		"coveragemerge user evidence", "coveragemerge assistant evidence",
	)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	content := "coveragemerge durable relationship"
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, []Mutation{
		{
			Operation: OperationAdd, SourceTurnID: fixture.turnID,
			Kind: "relationship", Scope: fixture.scope,
			Content: content, ConfidenceBasisPoints: 9100,
		},
		{
			Operation: OperationAdd, SourceTurnID: fixture.turnID,
			Kind: "relationship", Scope: fixture.scope,
			Content: "coveragemerge   durable relationship", ConfidenceBasisPoints: 9100,
		},
	})
	if err != nil {
		t.Fatalf("commit repeated coverage actions: %v", err)
	}
	if len(results) != 2 || results[0].Status != "applied" || results[0].MemoryID == "" ||
		results[1] != (MutationResult{Status: "no_change", ExistingMemoryID: results[0].MemoryID}) {
		t.Fatalf("repeated coverage results = %#v", results)
	}
	if count := extractionSettlementCoverageCount(t, database, fixture.conversationID); count != 1 {
		t.Fatalf("repeated coverage row count = %d, want 1 durable Turn-memory coverage", count)
	}
	var status string
	if err := database.QueryRowContext(t.Context(), `
SELECT result_status
FROM memory_context_coverages
WHERE conversation_id = ? AND turn_id = ? AND memory_id = ?`,
		fixture.conversationID, fixture.turnID, results[0].MemoryID,
	).Scan(&status); err != nil {
		t.Fatalf("load repeated coverage status: %v", err)
	}
	if status != "applied" {
		t.Fatalf("repeated coverage status = %q, want applied precedence", status)
	}
	assertExtractionSettlementTurnProcessed(t, database, fixture.turnID)
}

func assertSeekDBSettlementMutationParityAndCoverage(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	addCase := seedExtractionSettlementCase(
		t, database, "settlement-add", "settlementadd user evidence", "settlementadd assistant evidence",
	)
	addBatch := claimExtractionSettlementBatch(t, store, addCase.conversationID, 1)
	if len(addBatch.ExistingMemories) != 0 {
		t.Fatalf("ADD batch projection = %#v, want empty", addBatch.ExistingMemories)
	}
	addMutations := []Mutation{
		{
			Operation: OperationAdd, SourceTurnID: addCase.turnID,
			Kind: "relationship", Scope: addCase.scope,
			Content: "settlementadd first durable relationship", ConfidenceBasisPoints: 9100,
		},
		{
			Operation: OperationAdd, SourceTurnID: addCase.turnID,
			Kind: "relationship", Scope: addCase.scope,
			Content: "settlementadd second durable relationship", ConfidenceBasisPoints: 9200,
		},
	}
	addResults, err := store.CommitClaimedMemoryMutationsContext(t.Context(), addBatch, addMutations)
	if err != nil {
		t.Fatalf("commit two ADD mutations: %v", err)
	}
	if len(addResults) != 2 || addResults[0].Status != "applied" || addResults[0].MemoryID == "" ||
		addResults[1].Status != "applied" || addResults[1].MemoryID == "" ||
		addResults[0].MemoryID == addResults[1].MemoryID {
		t.Fatalf("two ADD results = %#v", addResults)
	}
	assertExtractionSettlementTurnProcessed(t, database, addCase.turnID)
	if count := extractionSettlementCoverageCount(t, database, addCase.conversationID); count != 2 {
		t.Fatalf("raw ADD coverage count = %d, want 2", count)
	}
	coverage, err := store.LoadCommittedMemoryCoverageContext(t.Context(), addCase.conversationID)
	if err != nil {
		t.Fatalf("load aggregated ADD coverage: %v", err)
	}
	assertSingleExtractionSettlementCoverage(
		t, coverage, addCase, []string{addResults[0].MemoryID, addResults[1].MemoryID}, "applied",
	)
	for _, result := range addResults {
		memory := loadExtractionSettlementMemory(t, database, result.MemoryID)
		if memory.status != "active" || memory.supersedesID != "" ||
			!slices.Equal(memory.evidenceIDs, addCase.evidenceIDs) {
			t.Fatalf("ADD memory %q = %#v", result.MemoryID, memory)
		}
	}

	operationsCase := seedExtractionSettlementCase(
		t, database, "settlement-operations", "settlementops user evidence", "settlementops assistant evidence",
	)
	targets := make([]personal.Record, 3)
	for index, suffix := range []string{"replace", "delete", "none"} {
		targets[index] = createExtractionSettlementAlias(
			t, personalStore, operationsCase.scope,
			fmt.Sprintf("settlementops existing %s relationship", suffix),
		)
	}
	operationsBatch := claimExtractionSettlementBatch(t, store, operationsCase.conversationID, 1)
	assertExtractionSettlementProjection(t, operationsBatch, targets...)
	operationsMutations := []Mutation{
		{
			Operation: OperationReplace, SourceTurnID: operationsCase.turnID,
			MemoryID: targets[0].ID, Kind: "relationship", Scope: operationsCase.scope,
			Content: "settlementops revised relationship", ConfidenceBasisPoints: 9500,
		},
		{Operation: OperationDelete, SourceTurnID: operationsCase.turnID, MemoryID: targets[1].ID},
		{Operation: OperationNone, SourceTurnID: operationsCase.turnID, MemoryID: targets[2].ID},
	}
	results, err := store.CommitClaimedMemoryMutationsContext(
		t.Context(), operationsBatch, operationsMutations,
	)
	if err != nil {
		t.Fatalf("commit REPLACE/DELETE/NONE mutations: %v", err)
	}
	if len(results) != 3 || results[0].Status != "applied" || results[0].MemoryID == "" ||
		results[1] != (MutationResult{Status: "applied", ExistingMemoryID: targets[1].ID}) ||
		results[2] != (MutationResult{Status: "no_change", ExistingMemoryID: targets[2].ID}) {
		t.Fatalf("REPLACE/DELETE/NONE results = %#v", results)
	}
	assertExtractionSettlementMemoryStatus(t, database, targets[0].ID, "superseded")
	assertExtractionSettlementMemoryStatus(t, database, targets[1].ID, "tombstone")
	assertExtractionSettlementMemoryStatus(t, database, targets[2].ID, "active")
	replacement := loadExtractionSettlementMemory(t, database, results[0].MemoryID)
	if replacement.status != "active" || replacement.supersedesID != targets[0].ID ||
		!slices.Equal(replacement.evidenceIDs, operationsCase.evidenceIDs) {
		t.Fatalf("replacement memory = %#v", replacement)
	}
	if count := extractionSettlementCoverageCount(t, database, operationsCase.conversationID); count != 3 {
		t.Fatalf("raw operation coverage count = %d, want 3", count)
	}
	coverage, err = store.LoadCommittedMemoryCoverageContext(t.Context(), operationsCase.conversationID)
	if err != nil {
		t.Fatalf("load aggregated operation coverage: %v", err)
	}
	assertSingleExtractionSettlementCoverage(
		t, coverage, operationsCase,
		[]string{results[0].MemoryID, targets[1].ID, targets[2].ID}, "applied",
	)
}

func assertSeekDBSettlementEmptyMutations(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-empty", "settlementempty user evidence", "settlementempty assistant evidence",
	)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, nil)
	if err != nil {
		t.Fatalf("commit empty mutation list: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("empty mutation results = %#v", results)
	}
	assertExtractionSettlementTurnProcessed(t, database, fixture.turnID)
	if count := extractionSettlementCoverageCount(t, database, fixture.conversationID); count != 0 {
		t.Fatalf("empty settlement coverage count = %d, want 0", count)
	}
}

func assertSeekDBSettlementDuplicateParitySkipsProvider(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()

	noChangeCase := seedExtractionSettlementCase(
		t, database, "settlement-no-change",
		"settlementnochange user evidence", "settlementnochange assistant evidence",
	)
	noChangeAlias := createExtractionSettlementAlias(
		t, personalStore, noChangeCase.scope, "settlementnochange durable relationship",
	)
	noChangeBatch := claimExtractionSettlementBatch(t, store, noChangeCase.conversationID, 1)
	assertExtractionSettlementProjection(t, noChangeBatch, noChangeAlias)
	failing := &extractionSettlementEmbedder{err: errors.New("pure no-change must skip provider")}
	personalStore.ReplaceSemanticEmbedder(failing)
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), noChangeBatch, []Mutation{{
		Operation: OperationAdd, SourceTurnID: noChangeCase.turnID,
		Kind: "relationship", Scope: noChangeCase.scope,
		Content: noChangeAlias.Content, ConfidenceBasisPoints: noChangeAlias.ConfidenceBasisPoints,
	}})
	personalStore.ReplaceSemanticEmbedder(nil)
	if err != nil {
		t.Fatalf("commit pure duplicate no-change with failing provider: %v", err)
	}
	if len(results) != 1 ||
		results[0] != (MutationResult{Status: "no_change", ExistingMemoryID: noChangeAlias.ID}) {
		t.Fatalf("pure duplicate no-change results = %#v", results)
	}
	if failing.calls.Load() != 0 {
		t.Fatalf("pure duplicate no-change provider calls = %d, want 0", failing.calls.Load())
	}
	assertExtractionSettlementMemoryStatus(t, database, noChangeAlias.ID, "active")
	assertExtractionSettlementTurnProcessed(t, database, noChangeCase.turnID)
	if count := extractionSettlementMemoryCount(t, database, noChangeCase.conversationID); count != 1 {
		t.Fatalf("pure duplicate no-change memory count = %d, want 1", count)
	}
	if count := extractionSettlementCoverageCount(t, database, noChangeCase.conversationID); count != 1 {
		t.Fatalf("pure duplicate no-change coverage count = %d, want 1", count)
	}

	parityCase := seedExtractionSettlementCase(
		t, database, "settlement-parity",
		"settlementparity user evidence", "settlementparity assistant evidence",
	)
	parityAlias := createExtractionSettlementAlias(
		t, personalStore, parityCase.scope, "settlementparity original relationship",
	)
	parityBatch := claimExtractionSettlementBatch(t, store, parityCase.conversationID, 1)
	assertExtractionSettlementProjection(t, parityBatch, parityAlias)
	successProvider := &extractionSettlementEmbedder{}
	personalStore.ReplaceSemanticEmbedder(successProvider)
	results, err = store.CommitClaimedMemoryMutationsContext(t.Context(), parityBatch, []Mutation{
		{
			Operation: OperationAdd, SourceTurnID: parityCase.turnID,
			Kind: "relationship", Scope: parityCase.scope,
			Content: parityAlias.Content, ConfidenceBasisPoints: parityAlias.ConfidenceBasisPoints,
		},
		{
			Operation: OperationReplace, SourceTurnID: parityCase.turnID,
			MemoryID: parityAlias.ID, Kind: "relationship", Scope: parityCase.scope,
			Content: "settlementparity revised relationship", ConfidenceBasisPoints: 9500,
		},
	})
	personalStore.ReplaceSemanticEmbedder(nil)
	if err != nil {
		t.Fatalf("commit no-change then REPLACE parity batch: %v", err)
	}
	if len(results) != 2 ||
		results[0] != (MutationResult{Status: "no_change", ExistingMemoryID: parityAlias.ID}) ||
		results[1].Status != "applied" || results[1].MemoryID == "" {
		t.Fatalf("no-change then REPLACE parity results = %#v", results)
	}
	if successProvider.calls.Load() != 1 {
		t.Fatalf("no-change then REPLACE provider calls = %d, want one batch call", successProvider.calls.Load())
	}
	assertExtractionSettlementMemoryStatus(t, database, parityAlias.ID, "superseded")
	replacement := loadExtractionSettlementMemory(t, database, results[1].MemoryID)
	if replacement.status != "active" || replacement.supersedesID != parityAlias.ID ||
		!slices.Equal(replacement.evidenceIDs, parityCase.evidenceIDs) {
		t.Fatalf("no-change then REPLACE memory = %#v", replacement)
	}
	assertExtractionSettlementTurnProcessed(t, database, parityCase.turnID)
	if count := extractionSettlementMemoryCount(t, database, parityCase.conversationID); count != 2 {
		t.Fatalf("no-change then REPLACE memory count = %d, want 2", count)
	}
	if count := extractionSettlementCoverageCount(t, database, parityCase.conversationID); count != 2 {
		t.Fatalf("no-change then REPLACE coverage count = %d, want 2", count)
	}
}

func assertSeekDBSettlementRevalidatesBlockedProviderPhantom(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-phantom",
		"windowprobe user evidence", "windowprobe assistant evidence",
	)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	if len(batch.ExistingMemories) != 0 {
		t.Fatalf("blocked-provider phantom initial projection = %#v, want empty", batch.ExistingMemories)
	}
	mutationContent := "durable uniquetuple alpha"
	mutations := []Mutation{{
		Operation: OperationAdd, SourceTurnID: fixture.turnID,
		Kind: "relationship", Scope: fixture.scope,
		Content: mutationContent, ConfidenceBasisPoints: 9300,
	}}
	blocker := newExtractionSettlementBlockingEmbedder()
	personalStore.ReplaceSemanticEmbedder(blocker)
	type outcome struct {
		results []MutationResult
		err     error
	}
	completed := make(chan outcome, 1)
	go func() {
		results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, mutations)
		completed <- outcome{results: results, err: err}
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		blocker.Unblock()
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatal("settlement provider did not enter blocked Embed")
	}
	manualStore := newExtractionSettlementPersonalStore(t, database, 15*time.Second, nil)
	manual, err := manualStore.CreatePersonalMemoryContext(
		t.Context(), "relationship", fixture.scope, "durable   uniquetuple alpha", 9400,
	)
	if err != nil {
		blocker.Unblock()
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatalf("manual duplicate write during blocked provider: %v", err)
	}
	blocker.Unblock()
	var settled outcome
	select {
	case settled = <-completed:
	case <-time.After(10 * time.Second):
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatal("settlement did not finish after provider release")
	}
	personalStore.ReplaceSemanticEmbedder(nil)
	if settled.err != nil {
		t.Fatalf("blocked-provider phantom settlement: %v", settled.err)
	}
	if len(settled.results) != 1 ||
		settled.results[0] != (MutationResult{Status: "no_change", ExistingMemoryID: manual.ID}) {
		t.Fatalf("blocked-provider phantom results = %#v, manual id = %q", settled.results, manual.ID)
	}
	if blocker.calls.Load() != 1 {
		t.Fatalf("blocked-provider phantom provider calls = %d, want 1", blocker.calls.Load())
	}
	exact := activeExactExtractionSettlementMemories(
		t, database, "relationship", fixture.scope, mutationContent,
	)
	if len(exact) != 1 || exact[0] != manual.ID {
		t.Fatalf("blocked-provider phantom active exact memories = %#v, want only %q", exact, manual.ID)
	}
	if count := extractionSettlementMemoryCount(t, database, fixture.conversationID); count != 1 {
		t.Fatalf("blocked-provider phantom memory rows = %d, want manual row only", count)
	}
	assertExtractionSettlementTurnProcessed(t, database, fixture.turnID)
	if count := extractionSettlementCoverageCount(t, database, fixture.conversationID); count != 1 {
		t.Fatalf("blocked-provider phantom coverage count = %d, want 1", count)
	}

	revisionCase := seedExtractionSettlementCase(
		t, database, "settlement-phantom-revise",
		"revisionwindow user evidence", "revisionwindow assistant evidence",
	)
	obsolete := createExtractionSettlementAlias(
		t, personalStore, revisionCase.scope, "obsolete manual relationship",
	)
	revisionBatch := claimExtractionSettlementBatch(t, store, revisionCase.conversationID, 1)
	if len(revisionBatch.ExistingMemories) != 0 {
		t.Fatalf("blocked-provider revision initial projection = %#v, want empty", revisionBatch.ExistingMemories)
	}
	revisionContent := "revised sharedtuple beta"
	revisionBlocker := newExtractionSettlementBlockingEmbedder()
	personalStore.ReplaceSemanticEmbedder(revisionBlocker)
	revisionCompleted := make(chan outcome, 1)
	go func() {
		results, err := store.CommitClaimedMemoryMutationsContext(
			t.Context(), revisionBatch, []Mutation{{
				Operation: OperationAdd, SourceTurnID: revisionCase.turnID,
				Kind: "relationship", Scope: revisionCase.scope,
				Content: revisionContent, ConfidenceBasisPoints: 9300,
			}},
		)
		revisionCompleted <- outcome{results: results, err: err}
	}()
	select {
	case <-revisionBlocker.entered:
	case <-time.After(5 * time.Second):
		revisionBlocker.Unblock()
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatal("revision phantom settlement provider did not enter blocked Embed")
	}
	revision, err := manualStore.RevisePersonalMemoryContext(
		t.Context(), obsolete.ID, "revised   sharedtuple beta", 9500,
	)
	if err != nil {
		revisionBlocker.Unblock()
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatalf("manual revision during blocked provider: %v", err)
	}
	revisionBlocker.Unblock()
	select {
	case settled = <-revisionCompleted:
	case <-time.After(10 * time.Second):
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatal("revision phantom settlement did not finish after provider release")
	}
	personalStore.ReplaceSemanticEmbedder(nil)
	if settled.err != nil {
		t.Fatalf("blocked-provider revision phantom settlement: %v", settled.err)
	}
	if len(settled.results) != 1 ||
		settled.results[0] != (MutationResult{Status: "no_change", ExistingMemoryID: revision.ID}) {
		t.Fatalf("blocked-provider revision phantom results = %#v, revision id = %q", settled.results, revision.ID)
	}
	if revisionBlocker.calls.Load() != 1 {
		t.Fatalf("blocked-provider revision phantom calls = %d, want 1", revisionBlocker.calls.Load())
	}
	assertExtractionSettlementMemoryStatus(t, database, obsolete.ID, "superseded")
	if exact := activeExactExtractionSettlementMemories(
		t, database, "relationship", revisionCase.scope, revisionContent,
	); len(exact) != 1 || exact[0] != revision.ID {
		t.Fatalf("blocked-provider revision phantom active exact memories = %#v, want only %q", exact, revision.ID)
	}
	assertExtractionSettlementTurnProcessed(t, database, revisionCase.turnID)
	if count := extractionSettlementCoverageCount(t, database, revisionCase.conversationID); count != 1 {
		t.Fatalf("blocked-provider revision phantom coverage count = %d, want 1", count)
	}
}

func assertSeekDBSettlementUsesNewestDuplicateWinner(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-winner",
		"winnerhistory duplicate evidence", "winnerhistory duplicate response",
	)
	content := "winnerhistory duplicate relationship"
	first := createExtractionSettlementAlias(t, personalStore, fixture.scope, content)
	second := createExtractionSettlementAlias(t, personalStore, fixture.scope, "winnerhistory   duplicate relationship")
	lexicalFirst, newest := first, second
	if strings.Compare(first.ID, second.ID) > 0 {
		lexicalFirst, newest = second, first
	}
	var firstCreated, secondCreated int64
	if err := database.QueryRowContext(t.Context(), `
SELECT created_at_ms FROM personal_memories WHERE id = ?`, first.ID).Scan(&firstCreated); err != nil {
		t.Fatalf("load first historical duplicate timestamp: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `
SELECT created_at_ms FROM personal_memories WHERE id = ?`, second.ID).Scan(&secondCreated); err != nil {
		t.Fatalf("load second historical duplicate timestamp: %v", err)
	}
	newestUpdatedAt := max(firstCreated, secondCreated) + 10_000
	if _, err := database.ExecContext(t.Context(), `
UPDATE personal_memories
SET updated_at_ms = CASE WHEN id = ? THEN ? ELSE created_at_ms END
WHERE id IN (?, ?)`, newest.ID, newestUpdatedAt, first.ID, second.ID); err != nil {
		t.Fatalf("order historical duplicate winners: %v", err)
	}
	if newest.ID == lexicalFirst.ID {
		t.Fatalf("historical duplicate setup did not separate newest and lexical winner: %#v %#v", newest, lexicalFirst)
	}
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	if len(batch.ExistingMemories) != 2 || batch.ExistingMemories[0].ID != newest.ID {
		t.Fatalf("historical duplicate projection = %#v, want newest %q first", batch.ExistingMemories, newest.ID)
	}
	provider := &extractionSettlementEmbedder{}
	personalStore.ReplaceSemanticEmbedder(provider)
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, []Mutation{
		{
			Operation: OperationAdd, SourceTurnID: fixture.turnID,
			Kind: "relationship", Scope: fixture.scope,
			Content: content, ConfidenceBasisPoints: 9000,
		},
		{
			Operation: OperationReplace, SourceTurnID: fixture.turnID,
			MemoryID: newest.ID, Kind: "relationship", Scope: fixture.scope,
			Content: "winnerhistory revised relationship", ConfidenceBasisPoints: 9600,
		},
	})
	personalStore.ReplaceSemanticEmbedder(nil)
	if err != nil {
		t.Fatalf("settle historical duplicate winner: %v", err)
	}
	if len(results) != 2 ||
		results[0] != (MutationResult{Status: "no_change", ExistingMemoryID: newest.ID}) ||
		results[1].Status != "applied" || results[1].MemoryID == "" {
		t.Fatalf("historical duplicate winner results = %#v, newest = %q", results, newest.ID)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("historical duplicate winner provider calls = %d, want 1", provider.calls.Load())
	}
	assertExtractionSettlementMemoryStatus(t, database, newest.ID, "superseded")
	assertExtractionSettlementMemoryStatus(t, database, lexicalFirst.ID, "active")
	replacement := loadExtractionSettlementMemory(t, database, results[1].MemoryID)
	if replacement.status != "active" || replacement.supersedesID != newest.ID {
		t.Fatalf("historical duplicate replacement = %#v", replacement)
	}
}

func assertSeekDBSettlementUnifiesHistoricalAndCreatedDuplicateWinners(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-unified-winner",
		"unifiedwinner same normalized relationship and separate replace source",
		"unifiedwinner exact duplicate evidence",
	)
	content := "unifiedwinner same normalized relationship"
	historicalA := createExtractionSettlementAlias(t, personalStore, fixture.scope, content)
	historicalE := createExtractionSettlementAlias(
		t, personalStore, fixture.scope, "unifiedwinner   same normalized relationship",
	)
	replaceTarget := createExtractionSettlementAlias(
		t, personalStore, fixture.scope, "unifiedwinner separate replace source",
	)
	for _, rename := range []struct {
		from string
		to   string
	}{
		{from: historicalA.ID, to: "0"},
		{from: historicalE.ID, to: "00"},
	} {
		if _, err := database.ExecContext(t.Context(), `
UPDATE personal_memories SET id = ? WHERE id = ?`, rename.to, rename.from); err != nil {
			t.Fatalf("assign deterministic historical duplicate id %q: %v", rename.to, err)
		}
	}
	historicalA.ID = "0"
	historicalE.ID = "00"
	if strings.Compare(historicalA.ID, historicalE.ID) >= 0 {
		t.Fatalf("historical duplicate ids are not A < E: %q, %q", historicalA.ID, historicalE.ID)
	}
	tieTime := fixture.createdAt + 500_000
	if _, err := database.ExecContext(t.Context(), `
UPDATE personal_memories SET updated_at_ms = ? WHERE id IN (?, ?)`,
		tieTime, historicalA.ID, historicalE.ID,
	); err != nil {
		t.Fatalf("tie historical duplicate timestamps: %v", err)
	}
	previousNow := store.now
	store.now = func() time.Time { return time.UnixMilli(tieTime) }
	defer func() { store.now = previousNow }()

	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	projected := make(map[string]struct{}, len(batch.ExistingMemories))
	for _, memory := range batch.ExistingMemories {
		projected[memory.ID] = struct{}{}
	}
	for _, memoryID := range []string{historicalA.ID, historicalE.ID, replaceTarget.ID} {
		if _, exists := projected[memoryID]; !exists {
			t.Fatalf("unified winner projection = %#v, missing %q", batch.ExistingMemories, memoryID)
		}
	}
	provider := &extractionSettlementEmbedder{}
	personalStore.ReplaceSemanticEmbedder(provider)
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, []Mutation{
		{
			Operation: OperationReplace, SourceTurnID: fixture.turnID,
			MemoryID: historicalA.ID, Kind: "relationship", Scope: fixture.scope,
			Content: "unifiedwinner  same normalized relationship", ConfidenceBasisPoints: 9000,
		},
		{
			Operation: OperationAdd, SourceTurnID: fixture.turnID,
			Kind: "relationship", Scope: fixture.scope,
			Content: content, ConfidenceBasisPoints: 9000,
		},
		{
			Operation: OperationReplace, SourceTurnID: fixture.turnID,
			MemoryID: replaceTarget.ID, Kind: "relationship", Scope: fixture.scope,
			Content: content, ConfidenceBasisPoints: 9000,
		},
	})
	personalStore.ReplaceSemanticEmbedder(nil)
	if err != nil {
		t.Fatalf("settle unified historical and created duplicate winners: %v", err)
	}
	if len(results) != 3 || results[0].Status != "applied" || results[0].MemoryID == "" ||
		results[1] != (MutationResult{Status: "no_change", ExistingMemoryID: historicalE.ID}) ||
		results[2] != (MutationResult{Status: "no_change", ExistingMemoryID: historicalE.ID}) {
		t.Fatalf("unified historical and created duplicate results = %#v, historical winner = %q", results, historicalE.ID)
	}
	createdN := results[0].MemoryID
	if strings.Compare(historicalE.ID, createdN) >= 0 {
		t.Fatalf("unified winner fixture requires E.ID < N.ID, got %q >= %q", historicalE.ID, createdN)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("unified winner provider calls = %d, want 1", provider.calls.Load())
	}
	var historicalTime, createdTime int64
	if err := database.QueryRowContext(t.Context(), `
SELECT updated_at_ms FROM personal_memories WHERE id = ?`, historicalE.ID).Scan(&historicalTime); err != nil {
		t.Fatalf("load unified historical winner timestamp: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `
SELECT updated_at_ms FROM personal_memories WHERE id = ?`, createdN).Scan(&createdTime); err != nil {
		t.Fatalf("load unified created winner timestamp: %v", err)
	}
	if historicalTime != tieTime || createdTime != tieTime {
		t.Fatalf("unified winner timestamp tie = (%d, %d), want %d", historicalTime, createdTime, tieTime)
	}
	assertExtractionSettlementMemoryStatus(t, database, historicalA.ID, "superseded")
	assertExtractionSettlementMemoryStatus(t, database, historicalE.ID, "active")
	assertExtractionSettlementMemoryStatus(t, database, replaceTarget.ID, "active")
	if exact := activeExactExtractionSettlementMemories(
		t, database, "relationship", fixture.scope, content,
	); !slices.Equal(exact, []string{historicalE.ID, createdN}) {
		t.Fatalf("unified exact winner order = %#v, want [%q %q]", exact, historicalE.ID, createdN)
	}
	if count := extractionSettlementCoverageCount(t, database, fixture.conversationID); count != 2 {
		t.Fatalf("unified winner coverage count = %d, want 2", count)
	}
	coverageStatuses := make(map[string]string, 2)
	rows, err := database.QueryContext(t.Context(), `
SELECT memory_id, result_status
FROM memory_context_coverages
WHERE conversation_id = ? AND turn_id = ?`, fixture.conversationID, fixture.turnID)
	if err != nil {
		t.Fatalf("load unified winner coverage: %v", err)
	}
	for rows.Next() {
		var memoryID, status string
		if err := rows.Scan(&memoryID, &status); err != nil {
			rows.Close()
			t.Fatalf("scan unified winner coverage: %v", err)
		}
		coverageStatuses[memoryID] = status
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate unified winner coverage: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close unified winner coverage: %v", err)
	}
	if len(coverageStatuses) != 2 || coverageStatuses[createdN] != "applied" ||
		coverageStatuses[historicalE.ID] != "no_change" {
		t.Fatalf("unified winner coverage = %#v", coverageStatuses)
	}

	next := seedExtractionSettlementCaseForCharacter(
		t, database, "settlement-unified-winner-next", fixture.characterID,
		"unifiedwinner same normalized relationship", "unifiedwinner followup evidence",
	)
	nextBatch := claimExtractionSettlementBatch(t, store, next.conversationID, 1)
	if len(nextBatch.ExistingMemories) < 2 || nextBatch.ExistingMemories[0].ID != historicalE.ID {
		t.Fatalf("next authoritative unified winner projection = %#v, want %q first", nextBatch.ExistingMemories, historicalE.ID)
	}
	failing := &extractionSettlementEmbedder{err: errors.New("next unified winner ADD must skip provider")}
	personalStore.ReplaceSemanticEmbedder(failing)
	nextResults, err := store.CommitClaimedMemoryMutationsContext(t.Context(), nextBatch, []Mutation{{
		Operation: OperationAdd, SourceTurnID: next.turnID,
		Kind: "relationship", Scope: next.scope,
		Content: content, ConfidenceBasisPoints: 9000,
	}})
	personalStore.ReplaceSemanticEmbedder(nil)
	if err != nil {
		t.Fatalf("settle next authoritative unified winner: %v", err)
	}
	if len(nextResults) != 1 ||
		nextResults[0] != (MutationResult{Status: "no_change", ExistingMemoryID: historicalE.ID}) {
		t.Fatalf("next authoritative unified winner results = %#v", nextResults)
	}
	if failing.calls.Load() != 0 {
		t.Fatalf("next authoritative unified winner provider calls = %d, want 0", failing.calls.Load())
	}
	if count := extractionSettlementCoverageCount(t, database, next.conversationID); count != 1 {
		t.Fatalf("next authoritative unified winner coverage count = %d, want 1", count)
	}
}

func assertSeekDBSettlementRejectsBlockedProviderTombstone(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-tombstone-window",
		"tombstonewindow user evidence", "tombstonewindow assistant evidence",
	)
	alias := createExtractionSettlementAlias(
		t, personalStore, fixture.scope, "tombstonewindow durable relationship",
	)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	assertExtractionSettlementProjection(t, batch, alias)
	before := loadExtractionTurnSnapshot(t, database, fixture.turnID)
	blocker := newExtractionSettlementBlockingEmbedder()
	personalStore.ReplaceSemanticEmbedder(blocker)
	type outcome struct {
		results []MutationResult
		err     error
	}
	completed := make(chan outcome, 1)
	go func() {
		results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, []Mutation{
			{Operation: OperationDelete, SourceTurnID: fixture.turnID, MemoryID: alias.ID},
			{
				Operation: OperationAdd, SourceTurnID: fixture.turnID,
				Kind: "relationship", Scope: fixture.scope,
				Content: alias.Content, ConfidenceBasisPoints: alias.ConfidenceBasisPoints,
			},
		})
		completed <- outcome{results: results, err: err}
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		blocker.Unblock()
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatal("tombstone-window provider did not enter blocked Embed")
	}
	manualStore := newExtractionSettlementPersonalStore(t, database, 15*time.Second, nil)
	if err := manualStore.TombstonePersonalMemoryContext(t.Context(), alias.ID); err != nil {
		blocker.Unblock()
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatalf("manual tombstone during blocked provider: %v", err)
	}
	blocker.Unblock()
	var settled outcome
	select {
	case settled = <-completed:
	case <-time.After(10 * time.Second):
		personalStore.ReplaceSemanticEmbedder(nil)
		t.Fatal("tombstone-window settlement did not finish after provider release")
	}
	personalStore.ReplaceSemanticEmbedder(nil)
	if !errors.Is(settled.err, ErrExtractionClaimConflict) {
		t.Fatalf("tombstone-window settlement error = %v, want extraction claim conflict", settled.err)
	}
	if len(settled.results) != 0 {
		t.Fatalf("tombstone-window settlement results = %#v, want none", settled.results)
	}
	if blocker.calls.Load() != 1 {
		t.Fatalf("tombstone-window provider calls = %d, want 1", blocker.calls.Load())
	}
	assertExtractionSettlementRejectedWithoutWrites(t, database, fixture, before, 1)
	assertExtractionSettlementMemoryStatus(t, database, alias.ID, "tombstone")
	if exact := activeExactExtractionSettlementMemories(
		t, database, "relationship", fixture.scope, alias.Content,
	); len(exact) != 0 {
		t.Fatalf("tombstone-window active exact memories = %#v, want none", exact)
	}
}

func assertSeekDBSettlementIgnoresNormalizedHashCollisions(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-collision",
		"collisionwindow user evidence", "collisionwindow assistant evidence",
	)
	decoy := createExtractionSettlementAlias(
		t, personalStore, fixture.scope, "unrelated collision decoy relationship",
	)
	targetContent := "exact collision target relationship"
	digest := sha256.Sum256([]byte(personal.NormalizeContent(targetContent)))
	decoyDigest := sha256.Sum256([]byte(personal.NormalizeContent(decoy.Content)))
	restored := false
	defer func() {
		if restored {
			return
		}
		if _, err := database.ExecContext(t.Context(), `
UPDATE personal_memories SET normalized_content_hash = ? WHERE id = ?`, decoyDigest[:], decoy.ID); err != nil {
			t.Errorf("restore simulated normalized-content hash collision during cleanup: %v", err)
		}
	}()
	if _, err := database.ExecContext(t.Context(), `
UPDATE personal_memories SET normalized_content_hash = ? WHERE id = ?`, digest[:], decoy.ID); err != nil {
		t.Fatalf("simulate normalized-content hash collision: %v", err)
	}
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	if len(batch.ExistingMemories) != 0 {
		t.Fatalf("hash collision batch projection = %#v, want empty", batch.ExistingMemories)
	}
	provider := &extractionSettlementEmbedder{}
	personalStore.ReplaceSemanticEmbedder(provider)
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, []Mutation{{
		Operation: OperationAdd, SourceTurnID: fixture.turnID,
		Kind: "relationship", Scope: fixture.scope,
		Content: targetContent, ConfidenceBasisPoints: 8800,
	}})
	personalStore.ReplaceSemanticEmbedder(nil)
	if err != nil {
		t.Fatalf("settle exact content under normalized hash collision: %v", err)
	}
	if len(results) != 1 || results[0].Status != "applied" || results[0].MemoryID == "" {
		t.Fatalf("hash collision results = %#v", results)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("hash collision provider calls = %d, want 1", provider.calls.Load())
	}
	if exact := activeExactExtractionSettlementMemories(
		t, database, "relationship", fixture.scope, targetContent,
	); len(exact) != 1 || exact[0] != results[0].MemoryID {
		t.Fatalf("hash collision exact active memories = %#v", exact)
	}
	assertExtractionSettlementMemoryStatus(t, database, decoy.ID, "active")
	if _, err := database.ExecContext(t.Context(), `
UPDATE personal_memories SET normalized_content_hash = ? WHERE id = ?`, decoyDigest[:], decoy.ID); err != nil {
		t.Fatalf("restore simulated normalized-content hash collision: %v", err)
	}
	restored = true
	var storedHash []byte
	if err := database.QueryRowContext(t.Context(), `
SELECT normalized_content_hash FROM personal_memories WHERE id = ?`, decoy.ID).Scan(&storedHash); err != nil {
		t.Fatalf("load restored normalized-content hash: %v", err)
	}
	if !slices.Equal(storedHash, decoyDigest[:]) {
		t.Fatalf("restored normalized-content hash = %x, want %x", storedHash, decoyDigest)
	}
}

func assertSeekDBSettlementRejectsUntrustedInputs(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()

	tamperCase := seedExtractionSettlementCase(
		t, database, "settlement-tamper", "settlementtamper user evidence", "settlementtamper assistant evidence",
	)
	tamperAlias := createExtractionSettlementAlias(
		t, personalStore, tamperCase.scope, "settlementtamper authoritative relationship",
	)
	tampered := claimExtractionSettlementBatch(t, store, tamperCase.conversationID, 1)
	assertExtractionSettlementProjection(t, tampered, tamperAlias)
	tampered.ExistingMemories = slices.Clone(tampered.ExistingMemories)
	tampered.ExistingMemories[0].Content += " changed"
	before := loadExtractionTurnSnapshot(t, database, tamperCase.turnID)
	_, err := store.CommitClaimedMemoryMutationsContext(t.Context(), tampered, []Mutation{{
		Operation: OperationNone, SourceTurnID: tamperCase.turnID, MemoryID: tamperAlias.ID,
	}})
	if !errors.Is(err, ErrExtractionClaimConflict) {
		t.Fatalf("tampered projection error = %v, want extraction claim conflict", err)
	}
	assertExtractionSettlementRejectedWithoutWrites(t, database, tamperCase, before, 1)
	assertExtractionSettlementMemoryStatus(t, database, tamperAlias.ID, "active")

	evidenceCase := seedExtractionSettlementCase(
		t, database, "settlement-evidence-drift",
		"settlementevidence original user", "settlementevidence original assistant",
	)
	evidenceBatch := claimExtractionSettlementBatch(t, store, evidenceCase.conversationID, 1)
	evidenceBatch.Turns = slices.Clone(evidenceBatch.Turns)
	evidenceBatch.Turns[0].UserMessage = "settlementevidence tampered user"
	before = loadExtractionTurnSnapshot(t, database, evidenceCase.turnID)
	_, err = store.CommitClaimedMemoryMutationsContext(t.Context(), evidenceBatch, nil)
	if !errors.Is(err, ErrExtractionClaimConflict) {
		t.Fatalf("tampered Turn evidence error = %v, want extraction claim conflict", err)
	}
	assertExtractionSettlementRejectedWithoutWrites(t, database, evidenceCase, before, 0)

	externalCase := seedExtractionSettlementCase(
		t, database, "settlement-external", "settlementexternal user evidence", "settlementexternal assistant evidence",
	)
	externalTurnID := seedAdditionalExtractionSettlementTurn(
		t, database, externalCase, "settlement-external-turn-2", 2,
		"settlementexternal second user", "settlementexternal second assistant",
	)
	externalBatch := claimExtractionSettlementBatch(t, store, externalCase.conversationID, 1)
	before = loadExtractionTurnSnapshot(t, database, externalCase.turnID)
	_, err = store.CommitClaimedMemoryMutationsContext(t.Context(), externalBatch, []Mutation{{
		Operation: OperationAdd, SourceTurnID: externalTurnID,
		Kind: "relationship", Scope: externalCase.scope,
		Content: "settlementexternal invalid source", ConfidenceBasisPoints: 8000,
	}})
	if err == nil || !strings.Contains(err.Error(), "source turn is not provided") {
		t.Fatalf("batch-external source error = %v", err)
	}
	assertExtractionSettlementRejectedWithoutWrites(t, database, externalCase, before, 0)
	if snapshot := loadExtractionTurnSnapshot(t, database, externalTurnID); snapshot.state != "pending" {
		t.Fatalf("batch-external source turn = %#v, want pending", snapshot)
	}

	staleCase := seedExtractionSettlementCase(
		t, database, "settlement-stale", "settlementstale user evidence", "settlementstale assistant evidence",
	)
	staleAlias := createExtractionSettlementAlias(
		t, personalStore, staleCase.scope, "settlementstale authoritative relationship",
	)
	staleBatch := claimExtractionSettlementBatch(t, store, staleCase.conversationID, 1)
	assertExtractionSettlementProjection(t, staleBatch, staleAlias)
	if err := personalStore.TombstonePersonalMemoryContext(t.Context(), staleAlias.ID); err != nil {
		t.Fatalf("make supplied alias stale: %v", err)
	}
	before = loadExtractionTurnSnapshot(t, database, staleCase.turnID)
	_, err = store.CommitClaimedMemoryMutationsContext(t.Context(), staleBatch, []Mutation{{
		Operation: OperationNone, SourceTurnID: staleCase.turnID, MemoryID: staleAlias.ID,
	}})
	if !errors.Is(err, ErrExtractionClaimConflict) {
		t.Fatalf("stale alias error = %v, want extraction claim conflict", err)
	}
	assertExtractionSettlementRejectedWithoutWrites(t, database, staleCase, before, 1)
	assertExtractionSettlementMemoryStatus(t, database, staleAlias.ID, "tombstone")
}

func assertSeekDBSettlementHooksRollBackEverything(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	sentinel := errors.New("injected settlement failure")
	stages := []seekDBWriteStage{
		seekDBStageSettleAfterEvidenceLock,
		seekDBStageSettleAfterSupersede,
		seekDBStageSettleAfterInsert,
		seekDBStageSettleAfterEvidence,
		seekDBStageSettleAfterCoverage,
		seekDBStageSettleAfterProcessed,
		seekDBStageSettleBeforeCommit,
	}
	for index, stage := range stages {
		prefix := fmt.Sprintf("settlement-hook-%d", index)
		keyword := fmt.Sprintf("settlementhook%d", index)
		fixture := seedExtractionSettlementCase(
			t, database, prefix, keyword+" user evidence", keyword+" assistant evidence",
		)
		alias := createExtractionSettlementAlias(
			t, personalStore, fixture.scope, keyword+" original relationship",
		)
		batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
		assertExtractionSettlementProjection(t, batch, alias)
		before := loadExtractionTurnSnapshot(t, database, fixture.turnID)
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return sentinel
			}
			return nil
		}
		_, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, []Mutation{{
			Operation: OperationReplace, SourceTurnID: fixture.turnID,
			MemoryID: alias.ID, Kind: "relationship", Scope: fixture.scope,
			Content: keyword + " replacement relationship", ConfidenceBasisPoints: 9400,
		}})
		store.seekDBWriteHook = nil
		if !errors.Is(err, sentinel) {
			t.Fatalf("settlement hook %q error = %v, want sentinel", stage, err)
		}
		assertExtractionSettlementRejectedWithoutWrites(t, database, fixture, before, 1)
		assertExtractionSettlementMemoryStatus(t, database, alias.ID, "active")
		var replacements int64
		if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM personal_memories WHERE supersedes_id = ?`, alias.ID).Scan(&replacements); err != nil {
			t.Fatalf("count hook %q replacements: %v", stage, err)
		}
		if replacements != 0 {
			t.Fatalf("settlement hook %q committed %d replacements", stage, replacements)
		}
	}
}

type extractionSettlementReplayFixture struct {
	batch      *BatchInput
	mutations  []Mutation
	caseData   extractionSettlementCase
	memoryID   string
	evidenceID []string
}

func assertSeekDBSettlementReplaySkipsProvider(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) extractionSettlementReplayFixture {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-replay", "settlementreplay user evidence", "settlementreplay assistant evidence",
	)
	successProvider := &extractionSettlementEmbedder{}
	personalStore.ReplaceSemanticEmbedder(successProvider)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	mutations := []Mutation{{
		Operation: OperationAdd, SourceTurnID: fixture.turnID,
		Kind: "relationship", Scope: fixture.scope,
		Content: "settlementreplay durable relationship", ConfidenceBasisPoints: 9600,
	}}
	results, err := store.CommitClaimedMemoryMutationsContext(t.Context(), batch, mutations)
	if err != nil {
		t.Fatalf("commit replay fixture: %v", err)
	}
	if len(results) != 1 || results[0].Status != "applied" || results[0].MemoryID == "" {
		t.Fatalf("replay fixture results = %#v", results)
	}
	if successProvider.calls.Load() != 1 {
		t.Fatalf("successful settlement provider calls = %d, want 1", successProvider.calls.Load())
	}
	memoryCount := extractionSettlementMemoryCount(t, database, fixture.conversationID)
	coverageCount := extractionSettlementCoverageCount(t, database, fixture.conversationID)
	failing := &extractionSettlementEmbedder{err: errors.New("provider must not run during replay")}
	personalStore.ReplaceSemanticEmbedder(failing)
	_, err = store.CommitClaimedMemoryMutationsContext(t.Context(), batch, mutations)
	if !errors.Is(err, ErrExtractionClaimConflict) {
		t.Fatalf("successful settlement replay error = %v, want extraction claim conflict", err)
	}
	if failing.calls.Load() != 0 {
		t.Fatalf("successful settlement replay provider calls = %d, want 0", failing.calls.Load())
	}
	if got := extractionSettlementMemoryCount(t, database, fixture.conversationID); got != memoryCount {
		t.Fatalf("successful settlement replay memory count = %d, want %d", got, memoryCount)
	}
	if got := extractionSettlementCoverageCount(t, database, fixture.conversationID); got != coverageCount {
		t.Fatalf("successful settlement replay coverage count = %d, want %d", got, coverageCount)
	}
	personalStore.ReplaceSemanticEmbedder(nil)
	return extractionSettlementReplayFixture{
		batch: cloneBatchInput(batch), mutations: slices.Clone(mutations), caseData: fixture,
		memoryID: results[0].MemoryID, evidenceID: slices.Clone(fixture.evidenceIDs),
	}
}

func assertSeekDBSettlementConcurrentCommitHasOneWinner(
	t *testing.T,
	database *sql.DB,
	store *Store,
) {
	t.Helper()
	fixture := seedExtractionSettlementCase(
		t, database, "settlement-concurrent", "settlementconcurrent user evidence", "settlementconcurrent assistant evidence",
	)
	batch := claimExtractionSettlementBatch(t, store, fixture.conversationID, 1)
	mutations := []Mutation{{
		Operation: OperationAdd, SourceTurnID: fixture.turnID,
		Kind: "relationship", Scope: fixture.scope,
		Content: "settlementconcurrent durable relationship", ConfidenceBasisPoints: 9000,
	}}
	type outcome struct {
		results []MutationResult
		err     error
	}
	ctx := t.Context()
	start := make(chan struct{})
	outcomes := make([]outcome, 2)
	var group sync.WaitGroup
	group.Add(len(outcomes))
	for index := range outcomes {
		go func(index int) {
			defer group.Done()
			<-start
			outcomes[index].results, outcomes[index].err =
				store.CommitClaimedMemoryMutationsContext(ctx, batch, mutations)
		}(index)
	}
	close(start)
	group.Wait()
	successes, conflicts := 0, 0
	for _, outcome := range outcomes {
		switch {
		case outcome.err == nil:
			successes++
			if len(outcome.results) != 1 || outcome.results[0].Status != "applied" ||
				outcome.results[0].MemoryID == "" {
				t.Fatalf("concurrent settlement success result = %#v", outcome.results)
			}
		case errors.Is(outcome.err, ErrExtractionClaimConflict):
			conflicts++
		default:
			t.Fatalf("concurrent settlement error = %v, want typed conflict", outcome.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent settlement outcomes = %#v, want one success and one conflict", outcomes)
	}
	assertExtractionSettlementTurnProcessed(t, database, fixture.turnID)
	if count := extractionSettlementMemoryCount(t, database, fixture.conversationID); count != 1 {
		t.Fatalf("concurrent settlement memory count = %d, want 1", count)
	}
	if count := extractionSettlementCoverageCount(t, database, fixture.conversationID); count != 1 {
		t.Fatalf("concurrent settlement coverage count = %d, want 1", count)
	}
}

func assertSeekDBSettlementSerializesDifferentClaimsOnOneTuple(
	t *testing.T,
	database *sql.DB,
	personalStore *personal.Store,
	store *Store,
) {
	t.Helper()
	first := seedExtractionSettlementCase(
		t, database, "settlement-cross-claim-a",
		"crossclaima user evidence", "crossclaima assistant evidence",
	)
	second := seedExtractionSettlementCaseForCharacter(
		t, database, "settlement-cross-claim-b", first.characterID,
		"crossclaimb user evidence", "crossclaimb assistant evidence",
	)
	firstBatch := claimExtractionSettlementBatch(t, store, first.conversationID, 1)
	secondBatch := claimExtractionSettlementBatch(t, store, second.conversationID, 1)
	if len(firstBatch.ExistingMemories) != 0 || len(secondBatch.ExistingMemories) != 0 {
		t.Fatalf("cross-claim initial projections = (%#v, %#v), want empty", firstBatch.ExistingMemories, secondBatch.ExistingMemories)
	}
	content := "crossclaim shared durable tuple"
	provider := &extractionSettlementEmbedder{}
	personalStore.ReplaceSemanticEmbedder(provider)
	type outcome struct {
		results []MutationResult
		err     error
	}
	inputs := []struct {
		batch  *BatchInput
		turnID string
	}{
		{batch: firstBatch, turnID: first.turnID},
		{batch: secondBatch, turnID: second.turnID},
	}
	start := make(chan struct{})
	outcomes := make([]outcome, len(inputs))
	var group sync.WaitGroup
	group.Add(len(inputs))
	for index, input := range inputs {
		go func(index int, input struct {
			batch  *BatchInput
			turnID string
		}) {
			defer group.Done()
			<-start
			outcomes[index].results, outcomes[index].err = store.CommitClaimedMemoryMutationsContext(
				t.Context(), input.batch, []Mutation{{
					Operation: OperationAdd, SourceTurnID: input.turnID,
					Kind: "relationship", Scope: first.scope,
					Content: content, ConfidenceBasisPoints: 9000,
				}},
			)
		}(index, input)
	}
	close(start)
	group.Wait()
	personalStore.ReplaceSemanticEmbedder(nil)
	appliedID := ""
	noChangeID := ""
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("cross-claim settlement error = %v", outcome.err)
		}
		if len(outcome.results) != 1 {
			t.Fatalf("cross-claim settlement results = %#v", outcome.results)
		}
		switch outcome.results[0].Status {
		case "applied":
			if appliedID != "" || outcome.results[0].MemoryID == "" {
				t.Fatalf("cross-claim applied results = %#v", outcomes)
			}
			appliedID = outcome.results[0].MemoryID
		case "no_change":
			if noChangeID != "" || outcome.results[0].ExistingMemoryID == "" {
				t.Fatalf("cross-claim no-change results = %#v", outcomes)
			}
			noChangeID = outcome.results[0].ExistingMemoryID
		default:
			t.Fatalf("cross-claim result = %#v", outcome.results[0])
		}
	}
	if appliedID == "" || noChangeID != appliedID {
		t.Fatalf("cross-claim winner ids = applied %q, no-change %q", appliedID, noChangeID)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("cross-claim provider calls = %d, want 2 pre-guard preparations", provider.calls.Load())
	}
	if exact := activeExactExtractionSettlementMemories(
		t, database, "relationship", first.scope, content,
	); len(exact) != 1 || exact[0] != appliedID {
		t.Fatalf("cross-claim active exact memories = %#v, want only %q", exact, appliedID)
	}
	assertExtractionSettlementTurnProcessed(t, database, first.turnID)
	assertExtractionSettlementTurnProcessed(t, database, second.turnID)
	if count := extractionSettlementCoverageCount(t, database, first.conversationID); count != 1 {
		t.Fatalf("first cross-claim coverage count = %d, want 1", count)
	}
	if count := extractionSettlementCoverageCount(t, database, second.conversationID); count != 1 {
		t.Fatalf("second cross-claim coverage count = %d, want 1", count)
	}
}

func assertSeekDBSettlementSurvivesRestart(
	t *testing.T,
	database *sql.DB,
	store *Store,
	failing *extractionSettlementEmbedder,
	fixture extractionSettlementReplayFixture,
) {
	t.Helper()
	assertExtractionSettlementTurnProcessed(t, database, fixture.caseData.turnID)
	memory := loadExtractionSettlementMemory(t, database, fixture.memoryID)
	if memory.status != "active" || memory.supersedesID != "" ||
		!slices.Equal(memory.evidenceIDs, fixture.evidenceID) {
		t.Fatalf("settled memory after restart = %#v", memory)
	}
	coverage, err := store.LoadCommittedMemoryCoverageContext(
		t.Context(), fixture.caseData.conversationID,
	)
	if err != nil {
		t.Fatalf("load settlement coverage after restart: %v", err)
	}
	assertSingleExtractionSettlementCoverage(
		t, coverage, fixture.caseData, []string{fixture.memoryID}, "applied",
	)
	_, err = store.CommitClaimedMemoryMutationsContext(t.Context(), fixture.batch, fixture.mutations)
	if !errors.Is(err, ErrExtractionClaimConflict) {
		t.Fatalf("settlement replay after restart error = %v, want extraction claim conflict", err)
	}
	if failing.calls.Load() != 0 {
		t.Fatalf("settlement replay after restart provider calls = %d, want 0", failing.calls.Load())
	}
}

type extractionSettlementCase struct {
	conversationID string
	characterID    string
	turnID         string
	userMessage    string
	assistant      string
	scope          personal.Scope
	evidenceIDs    []string
	createdAt      int64
}

func seedExtractionSettlementCase(
	t *testing.T,
	database *sql.DB,
	prefix, userMessage, assistantMessage string,
) extractionSettlementCase {
	t.Helper()
	return seedExtractionSettlementCaseForCharacter(
		t, database, prefix, prefix+"-character", userMessage, assistantMessage,
	)
}

func seedExtractionSettlementCaseForCharacter(
	t *testing.T,
	database *sql.DB,
	prefix, characterID, userMessage, assistantMessage string,
) extractionSettlementCase {
	t.Helper()
	fixture := extractionSettlementCase{
		conversationID: prefix + "-conversation",
		characterID:    characterID,
		turnID:         prefix + "-turn-1",
		userMessage:    userMessage,
		assistant:      assistantMessage,
		createdAt:      1_797_000_000_000,
	}
	fixture.scope = personal.Scope{Type: "character", CharacterID: fixture.characterID}
	fixture.evidenceIDs = []string{prefix + "-evidence-a", prefix + "-evidence-b"}
	seedExtractionConversation(
		t, database, fixture.conversationID, fixture.characterID, fixture.createdAt,
	)
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: fixture.conversationID,
		turnID:         fixture.turnID, sequence: 1, state: "pending", createdAt: fixture.createdAt + 1,
		withUser: true, withAssistant: true,
	})
	setExtractionSettlementMessages(
		t, database, fixture.conversationID, fixture.turnID, userMessage, assistantMessage,
	)
	for index, evidenceID := range fixture.evidenceIDs {
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES (?, ?, ?)`, fixture.turnID, evidenceID, fixture.createdAt+int64(index)+2); err != nil {
			t.Fatalf("seed extraction settlement evidence %q: %v", evidenceID, err)
		}
	}
	return fixture
}

func seedAdditionalExtractionSettlementTurn(
	t *testing.T,
	database *sql.DB,
	fixture extractionSettlementCase,
	turnID string,
	sequence int64,
	userMessage, assistantMessage string,
) string {
	t.Helper()
	seedExtractionTurn(t, database, extractionTurnFixture{
		conversationID: fixture.conversationID,
		turnID:         turnID, sequence: sequence, state: "pending",
		createdAt: fixture.createdAt + sequence*10, withUser: true, withAssistant: true,
	})
	setExtractionSettlementMessages(
		t, database, fixture.conversationID, turnID, userMessage, assistantMessage,
	)
	return turnID
}

func setExtractionSettlementMessages(
	t *testing.T,
	database *sql.DB,
	conversationID, turnID, userMessage, assistantMessage string,
) {
	t.Helper()
	result, err := database.ExecContext(t.Context(), `
UPDATE conversation_messages
SET content = CASE role WHEN 'user' THEN ? WHEN 'assistant' THEN ? ELSE content END
WHERE conversation_id = ? AND turn_id = ? AND role IN ('user', 'assistant')`,
		userMessage, assistantMessage, conversationID, turnID,
	)
	if err != nil {
		t.Fatalf("set extraction settlement messages for %q: %v", turnID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 2 {
		t.Fatalf("set extraction settlement messages for %q changed (%d, %v), want 2", turnID, changed, err)
	}
}

func createExtractionSettlementAlias(
	t *testing.T,
	store *personal.Store,
	scope personal.Scope,
	content string,
) personal.Record {
	t.Helper()
	record, err := store.CreatePersonalMemoryContext(
		t.Context(), "relationship", scope, content, 9000,
	)
	if err != nil {
		t.Fatalf("create extraction settlement alias %q: %v", content, err)
	}
	return record
}

func claimExtractionSettlementBatch(
	t *testing.T,
	store *Store,
	conversationID string,
	limit int,
) *BatchInput {
	t.Helper()
	batch, err := store.ClaimExtractionBatchContext(t.Context(), conversationID, limit)
	if err != nil {
		t.Fatalf("claim and enrich extraction settlement batch %q: %v", conversationID, err)
	}
	if batch == nil || batch.BatchID == "" || batch.ConversationID != conversationID ||
		len(batch.Turns) != limit {
		t.Fatalf("claimed extraction settlement batch = %#v", batch)
	}
	return batch
}

func assertExtractionSettlementProjection(
	t *testing.T,
	batch *BatchInput,
	want ...personal.Record,
) {
	t.Helper()
	if len(batch.ExistingMemories) != len(want) {
		t.Fatalf("extraction settlement projection = %#v, want ids from %#v", batch.ExistingMemories, want)
	}
	got := make(map[string]personal.Retrieved, len(batch.ExistingMemories))
	for _, item := range batch.ExistingMemories {
		got[item.ID] = item
	}
	for _, record := range want {
		item, exists := got[record.ID]
		if !exists || item.Kind != record.Kind || item.Scope != record.Scope ||
			item.Content != record.Content ||
			item.ConfidenceBasisPoints != record.ConfidenceBasisPoints ||
			item.UpdatedAtUnixMS != record.UpdatedAtUnixMS {
			t.Fatalf("projection item for %q = %#v, want %#v", record.ID, item, record)
		}
	}
}

func assertExtractionSettlementRejectedWithoutWrites(
	t *testing.T,
	database *sql.DB,
	fixture extractionSettlementCase,
	wantTurn extractionTurnSnapshot,
	wantMemoryCount int64,
) {
	t.Helper()
	gotTurn := loadExtractionTurnSnapshot(t, database, fixture.turnID)
	if gotTurn != wantTurn {
		t.Fatalf("rejected settlement changed turn: before=%#v after=%#v", wantTurn, gotTurn)
	}
	if gotTurn.state != "claimed" || !gotTurn.claimID.Valid || !gotTurn.owner.Valid {
		t.Fatalf("rejected settlement lost durable claim: %#v", gotTurn)
	}
	if got := extractionSettlementMemoryCount(t, database, fixture.conversationID); got != wantMemoryCount {
		t.Fatalf("rejected settlement memory count = %d, want %d", got, wantMemoryCount)
	}
	if got := extractionSettlementCoverageCount(t, database, fixture.conversationID); got != 0 {
		t.Fatalf("rejected settlement coverage count = %d, want 0", got)
	}
}

func assertExtractionSettlementTurnProcessed(t *testing.T, database *sql.DB, turnID string) {
	t.Helper()
	snapshot := loadExtractionTurnSnapshot(t, database, turnID)
	if snapshot.state != "processed" || snapshot.claimID.Valid || snapshot.owner.Valid || snapshot.lease.Valid ||
		snapshot.nextAttempt != 0 || snapshot.errorCode.Valid || snapshot.errorMessage.Valid {
		t.Fatalf("processed extraction settlement turn = %#v", snapshot)
	}
}

type extractionSettlementMemory struct {
	status       string
	supersedesID string
	evidenceIDs  []string
}

func loadExtractionSettlementMemory(
	t *testing.T,
	database *sql.DB,
	memoryID string,
) extractionSettlementMemory {
	t.Helper()
	var status string
	var supersedes sql.NullString
	var evidence []byte
	if err := database.QueryRowContext(t.Context(), `
SELECT status, supersedes_id, evidence_ids
FROM personal_memories WHERE id = ?`, memoryID).Scan(&status, &supersedes, &evidence); err != nil {
		t.Fatalf("load extraction settlement memory %q: %v", memoryID, err)
	}
	memory := extractionSettlementMemory{status: status}
	if supersedes.Valid {
		memory.supersedesID = supersedes.String
	}
	if err := json.Unmarshal(evidence, &memory.evidenceIDs); err != nil {
		t.Fatalf("decode extraction settlement memory %q evidence %q: %v", memoryID, evidence, err)
	}
	return memory
}

func assertExtractionSettlementMemoryStatus(
	t *testing.T,
	database *sql.DB,
	memoryID, want string,
) {
	t.Helper()
	if got := loadExtractionSettlementMemory(t, database, memoryID).status; got != want {
		t.Fatalf("extraction settlement memory %q status = %q, want %q", memoryID, got, want)
	}
}

func extractionSettlementMemoryCount(
	t *testing.T,
	database *sql.DB,
	conversationID string,
) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM personal_memories WHERE source_conversation_id = ?`, conversationID).Scan(&count); err != nil {
		t.Fatalf("count extraction settlement memories for %q: %v", conversationID, err)
	}
	return count
}

func activeExactExtractionSettlementMemories(
	t *testing.T,
	database *sql.DB,
	kind string,
	scope personal.Scope,
	content string,
) []string {
	t.Helper()
	digest := sha256.Sum256([]byte(personal.NormalizeContent(content)))
	query := `
SELECT id, content
FROM personal_memories FORCE INDEX (personal_memories_duplicate_revalidation_idx)
WHERE kind = ? AND scope_kind = ? AND character_id IS NULL
  AND review_status = 'ready' AND status = 'active' AND normalized_content_hash = ?
ORDER BY updated_at_ms DESC, id ASC`
	arguments := []any{kind, scope.Type, digest[:]}
	if scope.Type == "character" {
		query = `
SELECT id, content
FROM personal_memories FORCE INDEX (personal_memories_duplicate_revalidation_idx)
WHERE kind = ? AND scope_kind = ? AND character_id = ?
  AND review_status = 'ready' AND status = 'active' AND normalized_content_hash = ?
ORDER BY updated_at_ms DESC, id ASC`
		arguments = []any{kind, scope.Type, scope.CharacterID, digest[:]}
	}
	rows, err := database.QueryContext(t.Context(), query, arguments...)
	if err != nil {
		t.Fatalf("query exact active extraction settlement memories: %v", err)
	}
	defer rows.Close()
	normalized := personal.NormalizeContent(content)
	ids := make([]string, 0)
	for rows.Next() {
		var id, stored string
		if err := rows.Scan(&id, &stored); err != nil {
			t.Fatalf("scan exact active extraction settlement memory: %v", err)
		}
		if personal.NormalizeContent(stored) == normalized {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate exact active extraction settlement memories: %v", err)
	}
	return ids
}

func extractionSettlementCoverageCount(
	t *testing.T,
	database *sql.DB,
	conversationID string,
) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM memory_context_coverages WHERE conversation_id = ?`, conversationID).Scan(&count); err != nil {
		t.Fatalf("count extraction settlement coverage for %q: %v", conversationID, err)
	}
	return count
}

func assertSingleExtractionSettlementCoverage(
	t *testing.T,
	coverage []Coverage,
	fixture extractionSettlementCase,
	allowedMemoryIDs []string,
	wantStatus string,
) {
	t.Helper()
	if len(coverage) != 1 {
		t.Fatalf("aggregated extraction settlement coverage = %#v, want one Turn", coverage)
	}
	record := coverage[0]
	wantTokens := max(1, (len([]rune(fixture.userMessage))+len([]rune(fixture.assistant))+3)/4)
	if record.ConversationID != fixture.conversationID || record.TurnID != fixture.turnID ||
		!slices.Contains(allowedMemoryIDs, record.MemoryID) || record.ResultStatus != wantStatus ||
		record.TurnSequence != 1 || record.StartMessageSequence != 1 || record.EndMessageSequence != 2 ||
		record.CoveredTokens != uint64(wantTokens) || record.CreatedAtUnixMS <= 0 {
		t.Fatalf("aggregated extraction settlement coverage = %#v, want one-count token projection %d", record, wantTokens)
	}
}

type extractionSettlementEmbedder struct {
	calls atomic.Int64
	err   error
}

type extractionSettlementBlockingEmbedder struct {
	calls     atomic.Int64
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	blockOnce sync.Once
}

func newExtractionSettlementBlockingEmbedder() *extractionSettlementBlockingEmbedder {
	return &extractionSettlementBlockingEmbedder{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (*extractionSettlementBlockingEmbedder) Ready() bool { return true }
func (*extractionSettlementBlockingEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (*extractionSettlementBlockingEmbedder) ModelID() string {
	return extractionSettlementEmbeddingSpace
}
func (*extractionSettlementBlockingEmbedder) Dims() int { return embedding.Dimensions }
func (embedder *extractionSettlementBlockingEmbedder) Embed(texts []string) ([][]float32, error) {
	return embedder.EmbedContext(context.Background(), texts)
}
func (embedder *extractionSettlementBlockingEmbedder) EmbedContext(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	embedder.calls.Add(1)
	embedder.enterOnce.Do(func() { close(embedder.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-embedder.release:
	}
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedding.Dimensions)
		results[index][0] = 1
	}
	return results, nil
}

func (embedder *extractionSettlementBlockingEmbedder) Unblock() {
	embedder.blockOnce.Do(func() { close(embedder.release) })
}

func (*extractionSettlementEmbedder) Ready() bool { return true }
func (*extractionSettlementEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (*extractionSettlementEmbedder) ModelID() string { return extractionSettlementEmbeddingSpace }
func (*extractionSettlementEmbedder) Dims() int       { return embedding.Dimensions }
func (*extractionSettlementEmbedder) Embed([]string) ([][]float32, error) {
	return nil, errors.New("legacy embedding entrypoint must not run during context-aware settlement")
}
func (embedder *extractionSettlementEmbedder) EmbedContext(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	embedder.calls.Add(1)
	if embedder.err != nil {
		return nil, embedder.err
	}
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedding.Dimensions)
		results[index][0] = 1
	}
	return results, nil
}

func newExtractionSettlementPersonalStore(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	embedder embedding.SemanticEmbedder,
) *personal.Store {
	t.Helper()
	store, err := personal.NewSeekDBStore(database, queryLimit, embedder)
	if err != nil {
		t.Fatalf("new SeekDB extraction settlement personal store: %v", err)
	}
	return store
}

func newExtractionSettlementStore(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	workerID string,
	personalStore *personal.Store,
) *Store {
	t.Helper()
	store, err := NewSeekDBStoreWithPersonal(
		database, queryLimit, workerID, 10*time.Minute, personalStore,
	)
	if err != nil {
		t.Fatalf("new full SeekDB extraction settlement store: %v", err)
	}
	return store
}
