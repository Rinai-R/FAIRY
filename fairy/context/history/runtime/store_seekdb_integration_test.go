//go:build integration

package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBRuntimeStateIsConcurrentIsolatedAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openRuntimeStateSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeRuntimeStateSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB runtime state schema: %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}

	const (
		primaryConversation  = "conversation-runtime-primary"
		isolatedConversation = "conversation-runtime-isolated"
		emptyConversation    = "conversation-runtime-empty"
		primaryTurn          = "turn-runtime-primary"
		isolatedTurn         = "turn-runtime-isolated"
		emptyTurn            = "turn-runtime-empty"
	)
	seedRuntimeStateConversation(t, database, primaryConversation, "character-runtime-primary", 1_786_500_000_000)
	seedRuntimeStateConversation(t, database, isolatedConversation, "character-runtime-isolated", 1_786_500_000_100)
	seedRuntimeStateConversation(t, database, emptyConversation, "character-runtime-empty", 1_786_500_000_200)
	seedRuntimeStateTurn(t, database, primaryConversation, primaryTurn, 1, 1_786_500_001_000)
	seedRuntimeStateTurn(t, database, isolatedConversation, isolatedTurn, 1, 1_786_500_001_100)
	seedRuntimeStateTurn(t, database, emptyConversation, emptyTurn, 1, 1_786_500_001_200)

	state := "planning"
	code := "MODEL_COMPLETED"
	first, err := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
		ConversationID: primaryConversation,
		TurnID:         primaryTurn,
		EventType:      "model",
		State:          &state,
		Code:           &code,
		MetadataJSON:   `{"nested":{"ok":true},"index":0}`,
	})
	if err != nil {
		t.Fatalf("append first runtime event: %v", err)
	}
	if first.ID == "" || first.Sequence != 1 || first.CreatedAtUnixMS <= 0 ||
		first.MetadataJSON != `{"index":0,"nested":{"ok":true}}` ||
		first.State == nil || *first.State != state || first.Code == nil || *first.Code != code {
		t.Fatalf("first runtime event = %#v", first)
	}

	const concurrentEvents = 24
	start := make(chan struct{})
	errorsByWriter := make(chan error, concurrentEvents)
	var wait sync.WaitGroup
	for index := range concurrentEvents {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, appendErr := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
				ConversationID: primaryConversation,
				TurnID:         primaryTurn,
				EventType:      "transition",
				MetadataJSON:   fmt.Sprintf(`{"index":%d}`, index+1),
			})
			errorsByWriter <- appendErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for appendErr := range errorsByWriter {
		if appendErr != nil {
			t.Fatalf("concurrent runtime event append: %v", appendErr)
		}
	}

	primaryEvents := assertRuntimeStateEventSequence(
		t, store, primaryConversation, primaryTurn, concurrentEvents+1,
	)
	seenIDs := make(map[string]struct{}, len(primaryEvents))
	for _, event := range primaryEvents {
		if _, duplicate := seenIDs[event.ID]; duplicate {
			t.Fatalf("duplicate runtime event id %q", event.ID)
		}
		seenIDs[event.ID] = struct{}{}
	}

	isolatedEvent, err := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
		ConversationID: isolatedConversation,
		TurnID:         isolatedTurn,
		EventType:      "terminal",
		MetadataJSON:   `{"status":"completed"}`,
	})
	if err != nil || isolatedEvent.Sequence != 1 {
		t.Fatalf("isolated runtime event = (%#v, %v)", isolatedEvent, err)
	}
	assertRuntimeStateEventSequence(t, store, isolatedConversation, isolatedTurn, 1)
	emptyEvents, err := store.ListTurnRuntimeEventsContext(t.Context(), emptyConversation, emptyTurn)
	if err != nil || len(emptyEvents) != 0 {
		t.Fatalf("empty turn runtime events = (%#v, %v)", emptyEvents, err)
	}
	if _, err := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
		ConversationID: isolatedConversation,
		TurnID:         primaryTurn,
		EventType:      "model",
		MetadataJSON:   `{}`,
	}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("mismatched runtime event error = %v, want %v", err, ErrTurnNotFound)
	}
	if _, err := store.ListTurnRuntimeEventsContext(t.Context(), isolatedConversation, primaryTurn); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("mismatched runtime event list error = %v, want %v", err, ErrTurnNotFound)
	}

	hashA := runtimeStateHash("a")
	hashB := runtimeStateHash("b")
	hashC := runtimeStateHash("c")
	primaryRespond := LaneContinuationRecord{
		ConversationID:     primaryConversation,
		Lane:               PromptLaneRespond,
		PreviousResponseID: "response-primary-1",
		RequestShapeHash:   hashA,
		InputPrefixHash:    hashB,
		ResponseItemHash:   hashC,
		WindowRevision:     1,
	}
	primaryRespond = saveAndLoadRuntimeContinuation(t, store, primaryRespond)
	primaryCompact := LaneContinuationRecord{
		ConversationID:     primaryConversation,
		Lane:               PromptLaneCompact,
		PreviousResponseID: "response-primary-compact",
		RequestShapeHash:   runtimeStateHash("compact-shape"),
		InputPrefixHash:    runtimeStateHash("compact-input"),
		ResponseItemHash:   runtimeStateHash("compact-response"),
		WindowRevision:     4,
	}
	primaryCompact = saveAndLoadRuntimeContinuation(t, store, primaryCompact)
	isolatedRespond := LaneContinuationRecord{
		ConversationID:     isolatedConversation,
		Lane:               PromptLaneRespond,
		PreviousResponseID: "response-isolated",
		RequestShapeHash:   runtimeStateHash("isolated-shape"),
		InputPrefixHash:    runtimeStateHash("isolated-input"),
		ResponseItemHash:   runtimeStateHash("isolated-response"),
		WindowRevision:     7,
	}
	isolatedRespond = saveAndLoadRuntimeContinuation(t, store, isolatedRespond)

	replacedRespond := primaryRespond
	replacedRespond.PreviousResponseID = "response-primary-2"
	replacedRespond.RequestShapeHash = runtimeStateHash("replaced-shape")
	replacedRespond.InputPrefixHash = runtimeStateHash("replaced-input")
	replacedRespond.ResponseItemHash = runtimeStateHash("replaced-response")
	replacedRespond.WindowRevision = 2
	replacedRespond.UpdatedAtUnixMS = 0
	replacedRespond = saveAndLoadRuntimeContinuation(t, store, replacedRespond)
	if err := store.ClearLaneContinuationContext(t.Context(), primaryConversation, PromptLaneRespond); err != nil {
		t.Fatalf("clear primary response continuation: %v", err)
	}
	if err := store.ClearLaneContinuationContext(t.Context(), primaryConversation, PromptLaneRespond); err != nil {
		t.Fatalf("idempotent clear primary response continuation: %v", err)
	}
	if record, found, err := store.LoadLaneContinuationContext(t.Context(), primaryConversation, PromptLaneRespond); err != nil || found || record != (LaneContinuationRecord{}) {
		t.Fatalf("cleared primary response continuation = (%#v, %v, %v)", record, found, err)
	}
	assertRuntimeContinuation(t, store, primaryCompact)
	assertRuntimeContinuation(t, store, isolatedRespond)
	if record, found, err := store.LoadLaneContinuationContext(t.Context(), "conversation-runtime-missing", PromptLaneRespond); err != nil || found || record != (LaneContinuationRecord{}) {
		t.Fatalf("missing continuation = (%#v, %v, %v)", record, found, err)
	}
	if _, err := store.SaveLaneContinuationContext(t.Context(), LaneContinuationRecord{
		ConversationID: "conversation-runtime-missing", Lane: PromptLaneRespond,
		PreviousResponseID: "missing", RequestShapeHash: hashA,
		InputPrefixHash: hashB, ResponseItemHash: hashC, WindowRevision: 1,
	}); err == nil {
		t.Fatal("save continuation for missing conversation error = nil")
	}
	if err := store.ClearLaneContinuationContext(t.Context(), "conversation-runtime-missing", PromptLaneRespond); err == nil {
		t.Fatal("clear continuation for missing conversation error = nil")
	}

	previousWindowID := "window-previous"
	observedTokens := uint64(8_192)
	estimatedTokens := uint64(9_216)
	primaryWindow := ContextWindowRecord{
		ConversationID:         primaryConversation,
		Lane:                   PromptLaneRespond,
		WindowNumber:           1,
		FirstWindowID:          "window-first",
		PreviousWindowID:       &previousWindowID,
		WindowID:               "window-primary-1",
		ObservedPrefillTokens:  &observedTokens,
		EstimatedPrefillTokens: &estimatedTokens,
		LastTrigger:            "created",
		FailureCount:           1,
		PromptWindowRevision:   1,
	}
	primaryWindow = saveAndLoadRuntimeContextWindow(t, store, primaryWindow)
	replacedWindow := primaryWindow
	replacedWindow.WindowNumber = 2
	replacedWindow.PreviousWindowID = nil
	replacedWindow.WindowID = "window-primary-2"
	replacedWindow.ObservedPrefillTokens = nil
	replacedWindow.EstimatedPrefillTokens = nil
	replacedWindow.LastTrigger = "compacted"
	replacedWindow.FailureCount = 2
	replacedWindow.PromptWindowRevision = 2
	replacedWindow.UpdatedAtUnixMS = 0
	replacedWindow = saveAndLoadRuntimeContextWindow(t, store, replacedWindow)
	if replacedWindow.PreviousWindowID != nil || replacedWindow.ObservedPrefillTokens != nil || replacedWindow.EstimatedPrefillTokens != nil {
		t.Fatalf("replaced context window retained stale nullable state: %#v", replacedWindow)
	}
	isolatedWindow := ContextWindowRecord{
		ConversationID:       isolatedConversation,
		Lane:                 PromptLaneRespond,
		WindowNumber:         7,
		FirstWindowID:        "window-isolated-first",
		WindowID:             "window-isolated-current",
		LastTrigger:          "observed",
		PromptWindowRevision: 7,
	}
	isolatedWindow = saveAndLoadRuntimeContextWindow(t, store, isolatedWindow)
	compactWindow := ContextWindowRecord{
		ConversationID:       primaryConversation,
		Lane:                 PromptLaneCompact,
		WindowNumber:         4,
		FirstWindowID:        "window-compact-first",
		WindowID:             "window-compact-current",
		LastTrigger:          "compacted",
		PromptWindowRevision: 4,
	}
	compactWindow = saveAndLoadRuntimeContextWindow(t, store, compactWindow)
	assertRuntimeContextWindow(t, store, replacedWindow)
	assertRuntimeContextWindow(t, store, isolatedWindow)
	assertRuntimeContextWindow(t, store, compactWindow)
	assertRuntimeStateClockRollbackOverwritesTimestamps(t, store, emptyConversation)
	if record, found, err := store.LoadContextWindowContext(t.Context(), "conversation-runtime-missing", PromptLaneRespond); err != nil || found || !reflect.DeepEqual(record, ContextWindowRecord{}) {
		t.Fatalf("missing context window = (%#v, %v, %v)", record, found, err)
	}
	if _, err := store.SaveContextWindowContext(t.Context(), ContextWindowRecord{
		ConversationID: "conversation-runtime-missing", Lane: PromptLaneRespond,
		WindowNumber: 1, FirstWindowID: "missing-first", WindowID: "missing-current",
		LastTrigger: "created", PromptWindowRevision: 1,
	}); err == nil {
		t.Fatal("save context window for missing conversation error = nil")
	}

	assertRuntimeStateCancellation(
		t, store, database,
		primaryConversation, primaryTurn,
		primaryCompact, compactWindow,
	)
	primaryEventCount := assertRuntimeStateInjectedRollbacks(
		t, store, database,
		primaryConversation, primaryTurn,
		primaryCompact, compactWindow,
	)

	closeRuntimeStateSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB runtime state: %v", err)
	}
	instance, database, closed = restarted, restarted.SQL(), false
	restartedStore, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeStateEventSequence(
		t, restartedStore, primaryConversation, primaryTurn, primaryEventCount,
	)
	assertRuntimeStateEventSequence(t, restartedStore, isolatedConversation, isolatedTurn, 1)
	if record, found, err := restartedStore.LoadLaneContinuationContext(t.Context(), primaryConversation, PromptLaneRespond); err != nil || found || record != (LaneContinuationRecord{}) {
		t.Fatalf("cleared continuation after restart = (%#v, %v, %v)", record, found, err)
	}
	assertRuntimeContinuation(t, restartedStore, primaryCompact)
	assertRuntimeContinuation(t, restartedStore, isolatedRespond)
	assertRuntimeContextWindow(t, restartedStore, replacedWindow)
	assertRuntimeContextWindow(t, restartedStore, isolatedWindow)
	assertRuntimeContextWindow(t, restartedStore, compactWindow)
}

func assertRuntimeStateInjectedRollbacks(
	t *testing.T,
	store *Store,
	database *sql.DB,
	conversationID, turnID string,
	continuation LaneContinuationRecord,
	window ContextWindowRecord,
) int {
	t.Helper()
	var errInjected = errors.New("injected runtime state failure")
	store.seekDBWriteHook = nil
	t.Cleanup(func() { store.seekDBWriteHook = nil })

	for _, stage := range []seekDBWriteStage{
		seekDBStageEventAfterSequence,
		seekDBStageEventAfterInsert,
		seekDBStageEventBeforeCommit,
	} {
		before := runtimeStateEventRowCount(t, database, conversationID, turnID)
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return errInjected
			}
			return nil
		}
		_, err := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
			ConversationID: conversationID,
			TurnID:         turnID,
			EventType:      "model",
			MetadataJSON:   fmt.Sprintf(`{"injectedStage":%q}`, stage),
		})
		if !errors.Is(err, errInjected) {
			t.Fatalf("runtime event failure at %s = %v, want injected error", stage, err)
		}
		if after := runtimeStateEventRowCount(t, database, conversationID, turnID); after != before {
			t.Fatalf("runtime event failure at %s changed row count from %d to %d", stage, before, after)
		}
	}
	store.seekDBWriteHook = nil
	beforeSuccessfulAppend := runtimeStateEventRowCount(t, database, conversationID, turnID)
	probe, err := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
		ConversationID: conversationID,
		TurnID:         turnID,
		EventType:      "model",
		MetadataJSON:   `{"rollbackProbe":true}`,
	})
	if err != nil {
		t.Fatalf("append after injected runtime event failures: %v", err)
	}
	if probe.Sequence != uint64(beforeSuccessfulAppend+1) {
		t.Fatalf("sequence after injected rollbacks = %d, want %d", probe.Sequence, beforeSuccessfulAppend+1)
	}

	for _, stage := range []seekDBWriteStage{
		seekDBStageContinuationAfterUpsert,
		seekDBStageContinuationBeforeCommit,
	} {
		replacement := continuation
		replacement.PreviousResponseID = "injected-upsert-" + string(stage)
		replacement.UpdatedAtUnixMS = 0
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return errInjected
			}
			return nil
		}
		if _, err := store.SaveLaneContinuationContext(t.Context(), replacement); !errors.Is(err, errInjected) {
			t.Fatalf("continuation save failure at %s = %v, want injected error", stage, err)
		}
		store.seekDBWriteHook = nil
		assertRuntimeContinuation(t, store, continuation)
	}
	for _, stage := range []seekDBWriteStage{
		seekDBStageContinuationAfterDelete,
		seekDBStageContinuationBeforeCommit,
	} {
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return errInjected
			}
			return nil
		}
		if err := store.ClearLaneContinuationContext(
			t.Context(), continuation.ConversationID, continuation.Lane,
		); !errors.Is(err, errInjected) {
			t.Fatalf("continuation clear failure at %s = %v, want injected error", stage, err)
		}
		store.seekDBWriteHook = nil
		assertRuntimeContinuation(t, store, continuation)
	}

	for _, stage := range []seekDBWriteStage{
		seekDBStageContextAfterUpsert,
		seekDBStageContextBeforeCommit,
	} {
		replacement := window
		replacement.WindowID = "injected-window-" + string(stage)
		replacement.UpdatedAtUnixMS = 0
		store.seekDBWriteHook = func(got seekDBWriteStage) error {
			if got == stage {
				return errInjected
			}
			return nil
		}
		if _, err := store.SaveContextWindowContext(t.Context(), replacement); !errors.Is(err, errInjected) {
			t.Fatalf("context window failure at %s = %v, want injected error", stage, err)
		}
		store.seekDBWriteHook = nil
		assertRuntimeContextWindow(t, store, window)
	}
	store.seekDBWriteHook = nil
	return beforeSuccessfulAppend + 1
}

func assertRuntimeStateCancellation(
	t *testing.T,
	store *Store,
	database *sql.DB,
	conversationID, turnID string,
	continuation LaneContinuationRecord,
	window ContextWindowRecord,
) {
	t.Helper()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	beforeEvents := runtimeStateEventRowCount(t, database, conversationID, turnID)
	if _, err := store.AppendTurnRuntimeEventContext(canceled, TurnRuntimeEventInput{
		ConversationID: conversationID, TurnID: turnID,
		EventType: "model", MetadataJSON: `{"canceled":true}`,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled runtime event append error = %v, want context.Canceled", err)
	}
	if got := runtimeStateEventRowCount(t, database, conversationID, turnID); got != beforeEvents {
		t.Fatalf("canceled runtime event append changed row count from %d to %d", beforeEvents, got)
	}
	if _, err := store.ListTurnRuntimeEventsContext(canceled, conversationID, turnID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled runtime event list error = %v, want context.Canceled", err)
	}

	replacementContinuation := continuation
	replacementContinuation.PreviousResponseID = "canceled-response"
	replacementContinuation.UpdatedAtUnixMS = 0
	if _, err := store.SaveLaneContinuationContext(canceled, replacementContinuation); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled continuation save error = %v, want context.Canceled", err)
	}
	assertRuntimeContinuation(t, store, continuation)
	if err := store.ClearLaneContinuationContext(canceled, continuation.ConversationID, continuation.Lane); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled continuation clear error = %v, want context.Canceled", err)
	}
	assertRuntimeContinuation(t, store, continuation)
	if _, _, err := store.LoadLaneContinuationContext(canceled, continuation.ConversationID, continuation.Lane); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled continuation load error = %v, want context.Canceled", err)
	}

	replacementWindow := window
	replacementWindow.WindowID = "canceled-window"
	replacementWindow.UpdatedAtUnixMS = 0
	if _, err := store.SaveContextWindowContext(canceled, replacementWindow); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context window save error = %v, want context.Canceled", err)
	}
	assertRuntimeContextWindow(t, store, window)
	if _, _, err := store.LoadContextWindowContext(canceled, window.ConversationID, window.Lane); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context window load error = %v, want context.Canceled", err)
	}
}

func assertRuntimeStateEventSequence(t *testing.T, store *Store, conversationID, turnID string, count int) []TurnRuntimeEventRecord {
	t.Helper()
	records, err := store.ListTurnRuntimeEventsContext(t.Context(), conversationID, turnID)
	if err != nil {
		t.Fatalf("list runtime events for %s/%s: %v", conversationID, turnID, err)
	}
	if len(records) != count {
		t.Fatalf("runtime event count for %s/%s = %d, want %d", conversationID, turnID, len(records), count)
	}
	for index, record := range records {
		if record.ConversationID != conversationID || record.TurnID != turnID || record.Sequence != uint64(index+1) {
			t.Fatalf("runtime event %d = %#v", index, record)
		}
	}
	return records
}

func assertRuntimeStateClockRollbackOverwritesTimestamps(t *testing.T, store *Store, conversationID string) {
	t.Helper()
	originalNow := store.now
	t.Cleanup(func() { store.now = originalNow })

	currentTime := time.UnixMilli(2_000)
	store.now = func() time.Time { return currentTime }
	continuation := saveAndLoadRuntimeContinuation(t, store, LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               PromptLaneExtract,
		PreviousResponseID: "response-before-clock-rollback",
		RequestShapeHash:   runtimeStateHash("clock-shape-before"),
		InputPrefixHash:    runtimeStateHash("clock-input-before"),
		ResponseItemHash:   runtimeStateHash("clock-response-before"),
		WindowRevision:     1,
	})
	window := saveAndLoadRuntimeContextWindow(t, store, ContextWindowRecord{
		ConversationID:       conversationID,
		Lane:                 PromptLaneExtract,
		WindowNumber:         1,
		FirstWindowID:        "clock-window-first",
		WindowID:             "clock-window-before-rollback",
		LastTrigger:          "created",
		PromptWindowRevision: 1,
	})
	if continuation.UpdatedAtUnixMS != 2_000 || window.UpdatedAtUnixMS != 2_000 {
		t.Fatalf("initial timestamps = (%d, %d), want (2000, 2000)", continuation.UpdatedAtUnixMS, window.UpdatedAtUnixMS)
	}

	currentTime = time.UnixMilli(1_000)
	continuation.PreviousResponseID = "response-after-clock-rollback"
	continuation.WindowRevision = 2
	continuation.UpdatedAtUnixMS = 0
	continuation = saveAndLoadRuntimeContinuation(t, store, continuation)
	window.WindowNumber = 2
	window.WindowID = "clock-window-after-rollback"
	window.LastTrigger = "clock_rollback"
	window.PromptWindowRevision = 2
	window.UpdatedAtUnixMS = 0
	window = saveAndLoadRuntimeContextWindow(t, store, window)
	if continuation.UpdatedAtUnixMS != 1_000 || window.UpdatedAtUnixMS != 1_000 {
		t.Fatalf("rollback timestamps = (%d, %d), want (1000, 1000)", continuation.UpdatedAtUnixMS, window.UpdatedAtUnixMS)
	}

	store.now = originalNow
}

func saveAndLoadRuntimeContinuation(t *testing.T, store *Store, record LaneContinuationRecord) LaneContinuationRecord {
	t.Helper()
	saved, err := store.SaveLaneContinuationContext(t.Context(), record)
	if err != nil {
		t.Fatalf("save continuation %#v: %v", record, err)
	}
	if saved.UpdatedAtUnixMS <= 0 {
		t.Fatalf("saved continuation timestamp = %d", saved.UpdatedAtUnixMS)
	}
	assertRuntimeContinuation(t, store, saved)
	return saved
}

func assertRuntimeContinuation(t *testing.T, store *Store, want LaneContinuationRecord) {
	t.Helper()
	got, found, err := store.LoadLaneContinuationContext(t.Context(), want.ConversationID, want.Lane)
	if err != nil || !found || got != want {
		t.Fatalf("load continuation = (%#v, %v, %v), want %#v", got, found, err, want)
	}
}

func saveAndLoadRuntimeContextWindow(t *testing.T, store *Store, record ContextWindowRecord) ContextWindowRecord {
	t.Helper()
	saved, err := store.SaveContextWindowContext(t.Context(), record)
	if err != nil {
		t.Fatalf("save context window %#v: %v", record, err)
	}
	if saved.UpdatedAtUnixMS <= 0 {
		t.Fatalf("saved context window timestamp = %d", saved.UpdatedAtUnixMS)
	}
	assertRuntimeContextWindow(t, store, saved)
	return saved
}

func assertRuntimeContextWindow(t *testing.T, store *Store, want ContextWindowRecord) {
	t.Helper()
	got, found, err := store.LoadContextWindowContext(t.Context(), want.ConversationID, want.Lane)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("load context window = (%#v, %v, %v), want %#v", got, found, err, want)
	}
}

func seedRuntimeStateConversation(t *testing.T, database *sql.DB, conversationID, characterID string, now int64) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`, conversationID, characterID, now, now); err != nil {
		t.Fatalf("seed runtime conversation: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, 'character')`, characterID, conversationID); err != nil {
		t.Fatalf("seed runtime character binding: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO prompt_windows(
  conversation_id, revision, summary, cutoff_message_sequence,
  projection_revision, projection_state, updated_at_ms
) VALUES (?, 1, NULL, 0, 1, ?, ?)`, conversationID, `{"version":1,"omissions":[]}`, now); err != nil {
		t.Fatalf("seed runtime prompt window: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit runtime conversation seed: %v", err)
	}
}

func seedRuntimeStateTurn(t *testing.T, database *sql.DB, conversationID, turnID string, sequence uint64, now int64) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, sequence, status, origin, extraction_state,
  extraction_attempt_count, extraction_next_attempt_at_ms, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, 'completed', 'user', 'ineligible', 0, 0, ?, ?)`,
		turnID, conversationID, sequence, now, now,
	); err != nil {
		t.Fatalf("seed runtime turn: %v", err)
	}
}

func runtimeStateEventRowCount(t *testing.T, database *sql.DB, conversationID, turnID string) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM turn_runtime_events WHERE conversation_id = ? AND turn_id = ?`,
		conversationID, turnID,
	).Scan(&count); err != nil {
		t.Fatalf("count runtime events: %v", err)
	}
	return count
}

func runtimeStateHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func openRuntimeStateSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	config := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-runtime-state"),
		Address:       reserveRuntimeStateLoopbackAddress(t),
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
		t.Fatalf("open real SeekDB runtime state: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveRuntimeStateLoopbackAddress(t *testing.T) string {
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

func closeRuntimeStateSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB runtime state: %v", err)
	}
}
