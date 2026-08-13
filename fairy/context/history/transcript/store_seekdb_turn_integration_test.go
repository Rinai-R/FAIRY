//go:build integration

package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	historyexpr "fairy/context/history/expression"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBTurnLifecycleIsAtomicIsolatedAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openTranscriptSeekDBRuntime(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeTranscriptSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB turn schema: %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	// Keep every mutation in the same millisecond. SeekDB/MySQL reports changed
	// rows (not matched rows), so conversation touches must accept a no-op
	// GREATEST update without rolling back an otherwise valid Turn transaction.
	store.now = func() time.Time { return time.UnixMilli(1_786_400_000_000) }

	bootstrap, err := store.OpenOrCreateCharacterConversationContext(t.Context(), "character-turn-lifecycle")
	if err != nil {
		t.Fatalf("open primary conversation: %v", err)
	}
	conversationID := bootstrap.Conversation.ID
	otherBootstrap, err := store.OpenOrCreateCharacterConversationContext(t.Context(), "character-turn-lifecycle-other")
	if err != nil {
		t.Fatalf("open isolated conversation: %v", err)
	}

	plainTurn, err := store.BeginTurnContext(t.Context(), conversationID, "hello")
	if err != nil {
		t.Fatalf("begin user turn: %v", err)
	}
	assertPersistedSeekDBUserTurn(t, plainTurn, conversationID, "hello", "", 1)
	assertSeekDBTurnState(t, database, plainTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 1, status: "interpreting", origin: "user",
		extractionState: "ineligible",
	})

	const externalMessageID = "onebot-message-3.2"
	correlatedTurn, err := store.BeginCorrelatedTurnContext(t.Context(), conversationID, "correlated", externalMessageID)
	if err != nil {
		t.Fatalf("begin correlated turn: %v", err)
	}
	assertPersistedSeekDBUserTurn(t, correlatedTurn, conversationID, "correlated", externalMessageID, 2)

	initiationTurn, err := store.BeginInitiationTurnContext(
		t.Context(), conversationID, []string{"observation-2", "observation-1"},
	)
	if err != nil {
		t.Fatalf("begin initiation turn: %v", err)
	}
	if initiationTurn.ID == "" || initiationTurn.ConversationID != conversationID ||
		!reflect.DeepEqual(initiationTurn.UserMessage, MessageRecord{}) {
		t.Fatalf("initiation turn fabricated a user message: %#v", initiationTurn)
	}
	assertSeekDBTurnState(t, database, initiationTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 3, status: "interpreting", origin: "desktop_initiation",
		extractionState: "ineligible",
	})
	assertSeekDBInitiationEvidence(t, database, initiationTurn.ID, []string{"observation-1", "observation-2"})

	completed, err := store.CompleteTurnContext(t.Context(), conversationID, plainTurn.ID, "plain reply")
	if err != nil {
		t.Fatalf("complete user turn: %v", err)
	}
	assertSeekDBAssistantMessage(t, completed, conversationID, plainTurn.ID, "plain reply", 3, nil)
	assertSeekDBTurnState(t, database, plainTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 1, status: "completed", origin: "user",
		extractionState: "pending",
	})

	expressionParts := seekDBTurnExpressionParts()
	expressionContent := historyexpr.TextProjection(expressionParts)
	expressionMessage, err := store.CompleteExpressionTurnContext(
		t.Context(), conversationID, correlatedTurn.ID, expressionContent, expressionParts,
	)
	if err != nil {
		t.Fatalf("complete expression turn: %v", err)
	}
	assertSeekDBAssistantMessage(t, expressionMessage, conversationID, correlatedTurn.ID, expressionContent, 4, expressionParts)
	assertSeekDBTurnState(t, database, correlatedTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, messageID: externalMessageID, sequence: 2,
		status: "completed", origin: "user", extractionState: "pending",
	})

	initiationReply, err := store.CompleteTurnContext(t.Context(), conversationID, initiationTurn.ID, "welcome back")
	if err != nil {
		t.Fatalf("complete initiation turn: %v", err)
	}
	assertSeekDBAssistantMessage(t, initiationReply, conversationID, initiationTurn.ID, "welcome back", 5, nil)
	assertSeekDBTurnState(t, database, initiationTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 3, status: "completed", origin: "desktop_initiation",
		extractionState: "ineligible",
	})

	policyTurn, err := store.BeginTurnContext(t.Context(), conversationID, "do not extract")
	if err != nil {
		t.Fatal(err)
	}
	policyReply, err := store.CompleteExpressionTurnForPolicy(
		conversationID, policyTurn.ID, expressionContent, expressionParts, false,
	)
	if err != nil {
		t.Fatalf("complete expression turn for ineligible policy: %v", err)
	}
	assertSeekDBAssistantMessage(t, policyReply, conversationID, policyTurn.ID, expressionContent, 7, expressionParts)
	assertSeekDBTurnState(t, database, policyTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 4, status: "completed", origin: "user",
		extractionState: "ineligible",
	})

	prefixTurn, err := store.BeginTurnContext(t.Context(), conversationID, "interrupt text")
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := store.InterruptTurnContext(t.Context(), conversationID, prefixTurn.ID, "published prefix")
	if err != nil {
		t.Fatalf("interrupt text turn: %v", err)
	}
	if prefix == nil {
		t.Fatal("interrupted text turn did not persist its published prefix")
	}
	assertSeekDBAssistantMessage(t, *prefix, conversationID, prefixTurn.ID, "published prefix", 9, nil)
	assertSeekDBTurnState(t, database, prefixTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 5, status: "interrupted", origin: "user",
		extractionState: "ineligible",
	})
	assertSeekDBTerminalIsExclusive(t, "interrupted", store, database, conversationID, prefixTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, status: "interrupted", origin: "user", extractionState: "ineligible",
		ignoreSequence: true,
	}, 1)

	interruptedExpressionParts := []historyexpr.Part{
		{Kind: historyexpr.Utterance, Text: "published expression", VisualState: "idle"},
		{Kind: historyexpr.Sticker, VisualState: "happy", Sticker: &historyexpr.StickerSnapshot{
			ID: "sticker-interrupted", Description: "interrupted snapshot", MIMEType: "image/png",
		}},
	}
	interruptedExpressionTurn, err := store.BeginTurnContext(t.Context(), conversationID, "interrupt expression")
	if err != nil {
		t.Fatal(err)
	}
	interruptedExpression, err := store.InterruptExpressionTurnContext(
		t.Context(), conversationID, interruptedExpressionTurn.ID,
		historyexpr.TextProjection(interruptedExpressionParts), interruptedExpressionParts,
	)
	if err != nil {
		t.Fatalf("interrupt expression turn: %v", err)
	}
	if interruptedExpression == nil {
		t.Fatal("interrupted expression turn did not persist its published expression")
	}
	assertSeekDBAssistantMessage(
		t, *interruptedExpression, conversationID, interruptedExpressionTurn.ID,
		historyexpr.TextProjection(interruptedExpressionParts), 11, interruptedExpressionParts,
	)
	assertSeekDBTurnState(t, database, interruptedExpressionTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 6, status: "interrupted", origin: "user",
		extractionState: "ineligible",
	})

	failedTurn, err := store.BeginTurnContext(t.Context(), conversationID, "fail this turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailTurnContext(
		t.Context(), conversationID, failedTurn.ID, "PROVIDER_FAILED", "provider unavailable", true,
	); err != nil {
		t.Fatalf("fail turn: %v", err)
	}
	assertSeekDBTurnState(t, database, failedTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 7, status: "failed", origin: "user",
		extractionState: "ineligible", errorCode: "PROVIDER_FAILED",
		errorMessage: "provider unavailable", retryable: seekDBBool(true),
	})
	assertSeekDBTurnAssistantCount(t, database, failedTurn.ID, 0)
	assertSeekDBTerminalIsExclusive(t, "failed", store, database, conversationID, failedTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, status: "failed", origin: "user", extractionState: "ineligible",
		errorCode: "PROVIDER_FAILED", errorMessage: "provider unavailable", retryable: seekDBBool(true),
		ignoreSequence: true,
	}, 0)

	assertSeekDBTerminalIsExclusive(t, "completed", store, database, conversationID, plainTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, status: "completed", origin: "user", extractionState: "pending",
		ignoreSequence: true,
	}, 1)
	assertSeekDBCrossConversationTerminalIsolation(
		t, store, database, conversationID, otherBootstrap.Conversation.ID,
	)
	pureStickerTurn := assertSeekDBPureStickerCompletion(t, store, database, conversationID)
	assertSeekDBEmptyInterrupt(t, store, database, conversationID)
	assertSeekDBConcurrentTerminalWinner(t, store, database, conversationID)
	assertSeekDBCanceledTurnMutationsDoNotWrite(t, store, database, conversationID)
	assertSeekDBInjectedTurnRollbacks(t, store, database, conversationID)
	assertSeekDBConcurrentTurnSequences(t, store, database, conversationID, 12)

	loaded, err := store.LoadConversationContext(t.Context(), conversationID)
	if err != nil {
		t.Fatalf("load completed turn history: %v", err)
	}
	assertSeekDBCorrelatedTurnProjection(t, loaded.Messages, correlatedTurn.ID, externalMessageID)
	assertSeekDBExpressionSnapshot(t, loaded.Messages, correlatedTurn.ID, expressionParts)
	messageCountBeforeRestart := len(loaded.Messages)
	turnCountBeforeRestart := seekDBConversationRowCount(t, database, "conversation_turns", conversationID)

	closeTranscriptSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB turn runtime: %v", err)
	}
	instance, database, closed = restarted, restarted.SQL(), false
	restartedStore, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restartedStore.LoadConversationContext(t.Context(), conversationID)
	if err != nil {
		t.Fatalf("load turn history after restart: %v", err)
	}
	if len(restored.Messages) != messageCountBeforeRestart {
		t.Fatalf("message count after restart = %d, want %d", len(restored.Messages), messageCountBeforeRestart)
	}
	if got := seekDBConversationRowCount(t, database, "conversation_turns", conversationID); got != turnCountBeforeRestart {
		t.Fatalf("turn count after restart = %d, want %d", got, turnCountBeforeRestart)
	}
	assertSeekDBCorrelatedTurnProjection(t, restored.Messages, correlatedTurn.ID, externalMessageID)
	assertSeekDBExpressionSnapshot(t, restored.Messages, correlatedTurn.ID, expressionParts)
	assertSeekDBExpressionSnapshot(t, restored.Messages, pureStickerTurn.ID, seekDBPureStickerParts())
	assertSeekDBTurnState(t, database, failedTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 7, status: "failed", origin: "user",
		extractionState: "ineligible", errorCode: "PROVIDER_FAILED",
		errorMessage: "provider unavailable", retryable: seekDBBool(true),
	})
	assertSeekDBTurnState(t, database, prefixTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 5, status: "interrupted", origin: "user",
		extractionState: "ineligible",
	})
	assertSeekDBTurnState(t, database, initiationTurn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, sequence: 3, status: "completed", origin: "desktop_initiation",
		extractionState: "ineligible",
	})
	assertSeekDBInitiationEvidence(t, database, initiationTurn.ID, []string{"observation-1", "observation-2"})
}

func assertSeekDBPureStickerCompletion(t *testing.T, store *Store, database *sql.DB, conversationID string) PersistedTurn {
	t.Helper()
	turn, err := store.BeginTurnContext(t.Context(), conversationID, "pure sticker")
	if err != nil {
		t.Fatalf("begin pure sticker turn: %v", err)
	}
	parts := seekDBPureStickerParts()
	wantSequence := seekDBConversationMaxSequence(t, database, "conversation_messages", conversationID) + 1
	message, err := store.CompleteExpressionTurnContext(t.Context(), conversationID, turn.ID, "", parts)
	if err != nil {
		t.Fatalf("complete pure sticker turn: %v", err)
	}
	assertSeekDBAssistantMessage(t, message, conversationID, turn.ID, "", wantSequence, parts)
	assertSeekDBTurnState(t, database, turn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, status: "completed", origin: "user",
		extractionState: "pending", ignoreSequence: true,
	})
	return turn
}

func assertSeekDBEmptyInterrupt(t *testing.T, store *Store, database *sql.DB, conversationID string) {
	t.Helper()
	turn, err := store.BeginTurnContext(t.Context(), conversationID, "empty interrupt")
	if err != nil {
		t.Fatalf("begin empty interrupt turn: %v", err)
	}
	assistant, err := store.InterruptTurnContext(t.Context(), conversationID, turn.ID, "")
	if err != nil {
		t.Fatalf("interrupt without published output: %v", err)
	}
	if assistant != nil {
		t.Fatalf("empty interrupt persisted assistant message: %#v", assistant)
	}
	assertSeekDBTurnState(t, database, turn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, status: "interrupted", origin: "user",
		extractionState: "ineligible", ignoreSequence: true,
	})
	assertSeekDBTurnAssistantCount(t, database, turn.ID, 0)
}

func assertSeekDBConcurrentTerminalWinner(t *testing.T, store *Store, database *sql.DB, conversationID string) {
	t.Helper()
	turn, err := store.BeginTurnContext(t.Context(), conversationID, "terminal race")
	if err != nil {
		t.Fatalf("begin terminal race turn: %v", err)
	}
	type result struct {
		terminal string
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 3)
	var wait sync.WaitGroup
	calls := []struct {
		terminal string
		call     func() error
	}{
		{terminal: "completed", call: func() error {
			_, err := store.CompleteTurnContext(t.Context(), conversationID, turn.ID, "terminal race completion")
			return err
		}},
		{terminal: "interrupted", call: func() error {
			_, err := store.InterruptTurnContext(t.Context(), conversationID, turn.ID, "terminal race prefix")
			return err
		}},
		{terminal: "failed", call: func() error {
			return store.FailTurnContext(t.Context(), conversationID, turn.ID, "TERMINAL_RACE", "terminal race failure", false)
		}},
	}
	for _, candidate := range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- result{terminal: candidate.terminal, err: candidate.call()}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" {
				t.Fatalf("multiple terminal writers succeeded: %s and %s", winner, result.terminal)
			}
			winner = result.terminal
			continue
		}
		if !strings.Contains(result.err.Error(), terminalTurnConflictMessage) {
			t.Fatalf("terminal race %s error = %v", result.terminal, result.err)
		}
	}
	if winner == "" {
		t.Fatal("no terminal writer won the race")
	}
	var status string
	if err := database.QueryRowContext(t.Context(), `
SELECT status FROM conversation_turns WHERE id = ?`, turn.ID).Scan(&status); err != nil {
		t.Fatalf("load terminal race winner: %v", err)
	}
	if status != winner {
		t.Fatalf("terminal race status = %q, want winner %q", status, winner)
	}
	wantAssistantCount := 0
	if winner == "completed" || winner == "interrupted" {
		wantAssistantCount = 1
	}
	assertSeekDBTurnAssistantCount(t, database, turn.ID, wantAssistantCount)
}

func assertSeekDBInjectedTurnRollbacks(t *testing.T, store *Store, database *sql.DB, conversationID string) {
	t.Helper()
	sentinel := errors.New("injected SeekDB turn write failure")

	tests := []struct {
		name  string
		stage seekDBTurnWriteStage
		call  func() (string, error)
	}{
		{
			name: "begin after turn", stage: seekDBTurnStageBeginAfterTurn,
			call: func() (string, error) {
				turn, err := store.BeginTurnContext(t.Context(), conversationID, "rollback begin turn")
				return turn.ID, err
			},
		},
		{
			name: "begin after message", stage: seekDBTurnStageBeginAfterMessage,
			call: func() (string, error) {
				turn, err := store.BeginCorrelatedTurnContext(t.Context(), conversationID, "rollback begin message", "rollback-external")
				return turn.ID, err
			},
		},
		{
			name: "initiation after turn", stage: seekDBTurnStageInitiationAfterTurn,
			call: func() (string, error) {
				turn, err := store.BeginInitiationTurnContext(t.Context(), conversationID, []string{"rollback-initiation-turn"})
				return turn.ID, err
			},
		},
		{
			name: "initiation after evidence", stage: seekDBTurnStageInitiationAfterEvidence,
			call: func() (string, error) {
				turn, err := store.BeginInitiationTurnContext(t.Context(), conversationID, []string{"rollback-initiation-evidence"})
				return turn.ID, err
			},
		},
	}
	for _, test := range tests {
		t.Run("atomic "+test.name, func(t *testing.T) {
			turnsBefore := seekDBConversationRowCount(t, database, "conversation_turns", conversationID)
			messagesBefore := seekDBConversationRowCount(t, database, "conversation_messages", conversationID)
			evidenceBefore := seekDBEvidenceCountForConversation(t, database, conversationID)
			store.seekDBTurnHook = func(stage seekDBTurnWriteStage) error {
				if stage == test.stage {
					return sentinel
				}
				return nil
			}
			turnID, err := test.call()
			store.seekDBTurnHook = nil
			if !errors.Is(err, sentinel) {
				t.Fatalf("injected mutation error = %v, want %v", err, sentinel)
			}
			if turnID != "" {
				t.Fatalf("failed mutation exposed persisted turn ID %q", turnID)
			}
			if got := seekDBConversationRowCount(t, database, "conversation_turns", conversationID); got != turnsBefore {
				t.Fatalf("turn count after rollback = %d, want %d", got, turnsBefore)
			}
			if got := seekDBConversationRowCount(t, database, "conversation_messages", conversationID); got != messagesBefore {
				t.Fatalf("message count after rollback = %d, want %d", got, messagesBefore)
			}
			if got := seekDBEvidenceCountForConversation(t, database, conversationID); got != evidenceBefore {
				t.Fatalf("evidence count after rollback = %d, want %d", got, evidenceBefore)
			}
		})
	}

	terminalTests := []struct {
		name  string
		stage seekDBTurnWriteStage
		call  func(turnID string) error
	}{
		{
			name: "complete after update", stage: seekDBTurnStageCompleteAfterUpdate,
			call: func(turnID string) error {
				_, err := store.CompleteTurnContext(t.Context(), conversationID, turnID, "rollback completion update")
				return err
			},
		},
		{
			name: "complete after message", stage: seekDBTurnStageCompleteAfterMessage,
			call: func(turnID string) error {
				_, err := store.CompleteExpressionTurnContext(
					t.Context(), conversationID, turnID,
					historyexpr.TextProjection(seekDBTurnExpressionParts()), seekDBTurnExpressionParts(),
				)
				return err
			},
		},
		{
			name: "interrupt after update", stage: seekDBTurnStageInterruptAfterUpdate,
			call: func(turnID string) error {
				_, err := store.InterruptTurnContext(t.Context(), conversationID, turnID, "rollback interrupt update")
				return err
			},
		},
		{
			name: "interrupt after message", stage: seekDBTurnStageInterruptAfterMessage,
			call: func(turnID string) error {
				_, err := store.InterruptExpressionTurnContext(
					t.Context(), conversationID, turnID,
					historyexpr.TextProjection(seekDBTurnExpressionParts()), seekDBTurnExpressionParts(),
				)
				return err
			},
		},
	}
	for _, test := range terminalTests {
		t.Run("atomic "+test.name, func(t *testing.T) {
			turn, err := store.BeginTurnContext(t.Context(), conversationID, "terminal rollback "+test.name)
			if err != nil {
				t.Fatalf("begin rollback fixture: %v", err)
			}
			messagesBefore := seekDBConversationRowCount(t, database, "conversation_messages", conversationID)
			store.seekDBTurnHook = func(stage seekDBTurnWriteStage) error {
				if stage == test.stage {
					return sentinel
				}
				return nil
			}
			err = test.call(turn.ID)
			store.seekDBTurnHook = nil
			if !errors.Is(err, sentinel) {
				t.Fatalf("injected terminal error = %v, want %v", err, sentinel)
			}
			assertSeekDBTurnState(t, database, turn.ID, seekDBExpectedTurnState{
				conversationID: conversationID, status: "interpreting", origin: "user",
				extractionState: "ineligible", ignoreSequence: true,
			})
			assertSeekDBTurnAssistantCount(t, database, turn.ID, 0)
			if got := seekDBConversationRowCount(t, database, "conversation_messages", conversationID); got != messagesBefore {
				t.Fatalf("message count after terminal rollback = %d, want %d", got, messagesBefore)
			}
			if err := store.FailTurnContext(
				t.Context(), conversationID, turn.ID, "ROLLBACK_VERIFIED", "cleanup", false,
			); err != nil {
				t.Fatalf("terminal retry after rollback: %v", err)
			}
		})
	}
}

func assertSeekDBConcurrentTurnSequences(t *testing.T, store *Store, database *sql.DB, conversationID string, count int) {
	t.Helper()
	turnSequencesBefore := seekDBConversationMaxSequence(t, database, "conversation_turns", conversationID)
	messageSequencesBefore := seekDBConversationMaxSequence(t, database, "conversation_messages", conversationID)
	type beginResult struct {
		turn PersistedTurn
		err  error
	}
	start := make(chan struct{})
	results := make(chan beginResult, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			turn, err := store.BeginCorrelatedTurnContext(
				t.Context(), conversationID, fmt.Sprintf("concurrent-%d", index), fmt.Sprintf("external-%d", index),
			)
			results <- beginResult{turn: turn, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	turns := make([]PersistedTurn, 0, count)
	turnIDs := make(map[string]struct{}, count)
	messageSequences := make(map[uint64]struct{}, count)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent BeginCorrelatedTurnContext: %v", result.err)
		}
		if _, exists := turnIDs[result.turn.ID]; exists {
			t.Fatalf("duplicate concurrent turn ID %q", result.turn.ID)
		}
		turnIDs[result.turn.ID] = struct{}{}
		if _, exists := messageSequences[result.turn.UserMessage.Sequence]; exists {
			t.Fatalf("duplicate concurrent user message sequence %d", result.turn.UserMessage.Sequence)
		}
		messageSequences[result.turn.UserMessage.Sequence] = struct{}{}
		turns = append(turns, result.turn)
	}
	if len(turns) != count {
		t.Fatalf("concurrent turns = %d, want %d", len(turns), count)
	}
	assertSeekDBContiguousSequences(
		t, "concurrent turn", turnSequencesBefore+1,
		seekDBConversationSequencesAfter(t, database, "conversation_turns", conversationID, turnSequencesBefore), count,
	)
	assertSeekDBContiguousSequences(
		t, "concurrent user message", messageSequencesBefore+1,
		seekDBConversationSequencesAfter(t, database, "conversation_messages", conversationID, messageSequencesBefore), count,
	)

	type completeResult struct {
		message MessageRecord
		err     error
	}
	completeStart := make(chan struct{})
	completions := make(chan completeResult, count)
	for _, turn := range turns {
		turn := turn
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-completeStart
			message, err := store.CompleteTurnContext(t.Context(), conversationID, turn.ID, "reply "+turn.ID)
			completions <- completeResult{message: message, err: err}
		}()
	}
	close(completeStart)
	wait.Wait()
	close(completions)
	assistantSequences := make(map[uint64]struct{}, count)
	for result := range completions {
		if result.err != nil {
			t.Fatalf("concurrent CompleteTurnContext: %v", result.err)
		}
		if _, exists := assistantSequences[result.message.Sequence]; exists {
			t.Fatalf("duplicate concurrent assistant sequence %d", result.message.Sequence)
		}
		assistantSequences[result.message.Sequence] = struct{}{}
	}
	assertSeekDBContiguousSequences(
		t, "concurrent assistant message", messageSequencesBefore+uint64(count)+1,
		seekDBConversationSequencesAfter(
			t, database, "conversation_messages", conversationID, messageSequencesBefore+uint64(count),
		), count,
	)
}

func assertSeekDBTerminalIsExclusive(
	t *testing.T,
	label string,
	store *Store,
	database *sql.DB,
	conversationID string,
	turnID string,
	want seekDBExpectedTurnState,
	wantAssistantCount int,
) {
	t.Helper()
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "complete twice",
			call: func() error {
				_, err := store.CompleteTurnContext(t.Context(), conversationID, turnID, "second completion")
				return err
			},
		},
		{
			name: "interrupt completed",
			call: func() error {
				_, err := store.InterruptTurnContext(t.Context(), conversationID, turnID, "late prefix")
				return err
			},
		},
		{
			name: "fail completed",
			call: func() error {
				return store.FailTurnContext(t.Context(), conversationID, turnID, "LATE_FAILURE", "too late", false)
			},
		},
	}
	for _, test := range tests {
		t.Run(label+" "+test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("terminal state was rewritten")
			}
		})
	}
	assertSeekDBTurnAssistantCount(t, database, turnID, wantAssistantCount)
	assertSeekDBTurnState(t, database, turnID, want)
}

func assertSeekDBCrossConversationTerminalIsolation(
	t *testing.T,
	store *Store,
	database *sql.DB,
	ownerConversationID string,
	otherConversationID string,
) {
	t.Helper()
	turn, err := store.BeginTurnContext(t.Context(), ownerConversationID, "cross conversation")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "complete",
			call: func() error {
				_, err := store.CompleteTurnContext(t.Context(), otherConversationID, turn.ID, "wrong conversation")
				return err
			},
		},
		{
			name: "interrupt",
			call: func() error {
				_, err := store.InterruptTurnContext(t.Context(), otherConversationID, turn.ID, "wrong conversation")
				return err
			},
		},
		{
			name: "fail",
			call: func() error {
				return store.FailTurnContext(t.Context(), otherConversationID, turn.ID, "WRONG_OWNER", "wrong conversation", false)
			},
		},
	}
	for _, test := range tests {
		t.Run("cross conversation "+test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("cross-conversation terminal mutation was accepted")
			}
		})
	}
	assertSeekDBTurnState(t, database, turn.ID, seekDBExpectedTurnState{
		conversationID: ownerConversationID, status: "interpreting", origin: "user",
		extractionState: "ineligible", ignoreSequence: true,
	})
	assertSeekDBTurnAssistantCount(t, database, turn.ID, 0)
	if err := store.FailTurnContext(t.Context(), ownerConversationID, turn.ID, "CLEANUP", "cleanup", false); err != nil {
		t.Fatalf("finish cross-conversation fixture: %v", err)
	}
}

func assertSeekDBCanceledTurnMutationsDoNotWrite(t *testing.T, store *Store, database *sql.DB, conversationID string) {
	t.Helper()
	turnsBefore := seekDBConversationRowCount(t, database, "conversation_turns", conversationID)
	messagesBefore := seekDBConversationRowCount(t, database, "conversation_messages", conversationID)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.BeginTurnContext(canceled, conversationID, "canceled begin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled BeginTurnContext error = %v, want context.Canceled", err)
	}
	if got := seekDBConversationRowCount(t, database, "conversation_turns", conversationID); got != turnsBefore {
		t.Fatalf("canceled begin wrote %d turns, want %d", got, turnsBefore)
	}
	if got := seekDBConversationRowCount(t, database, "conversation_messages", conversationID); got != messagesBefore {
		t.Fatalf("canceled begin wrote %d messages, want %d", got, messagesBefore)
	}

	turn, err := store.BeginTurnContext(t.Context(), conversationID, "canceled terminal")
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel = context.WithCancel(t.Context())
	cancel()
	if _, err := store.CompleteTurnContext(canceled, conversationID, turn.ID, "must not commit"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CompleteTurnContext error = %v, want context.Canceled", err)
	}
	assertSeekDBTurnState(t, database, turn.ID, seekDBExpectedTurnState{
		conversationID: conversationID, status: "interpreting", origin: "user",
		extractionState: "ineligible", ignoreSequence: true,
	})
	assertSeekDBTurnAssistantCount(t, database, turn.ID, 0)
	if err := store.FailTurnContext(t.Context(), conversationID, turn.ID, "CLEANUP", "cleanup", false); err != nil {
		t.Fatalf("finish canceled terminal fixture: %v", err)
	}
}

type seekDBExpectedTurnState struct {
	conversationID  string
	messageID       string
	sequence        uint64
	status          string
	origin          string
	extractionState string
	errorCode       string
	errorMessage    string
	retryable       *bool
	ignoreSequence  bool
}

func assertSeekDBTurnState(t *testing.T, database *sql.DB, turnID string, want seekDBExpectedTurnState) {
	t.Helper()
	var (
		conversationID, status, origin, extractionState string
		messageID, errorCode, errorMessage              sql.NullString
		errorRetryable                                  sql.NullBool
		sequence                                        int64
	)
	if err := database.QueryRowContext(t.Context(), `
SELECT conversation_id, message_id, sequence, status, origin, extraction_state,
       error_code, error_message, error_retryable
FROM conversation_turns WHERE id = ?`, turnID).Scan(
		&conversationID, &messageID, &sequence, &status, &origin, &extractionState,
		&errorCode, &errorMessage, &errorRetryable,
	); err != nil {
		t.Fatalf("load SeekDB turn %q: %v", turnID, err)
	}
	if sequence <= 0 {
		t.Fatalf("turn %q sequence = %d", turnID, sequence)
	}
	if conversationID != want.conversationID || messageID.String != want.messageID ||
		(!want.ignoreSequence && uint64(sequence) != want.sequence) || status != want.status || origin != want.origin ||
		extractionState != want.extractionState || errorCode.String != want.errorCode || errorMessage.String != want.errorMessage {
		t.Fatalf("turn %q = conversation=%q message=%q sequence=%d status=%q origin=%q extraction=%q code=%q error=%q; want %#v",
			turnID, conversationID, messageID.String, sequence, status, origin, extractionState,
			errorCode.String, errorMessage.String, want,
		)
	}
	if want.retryable == nil {
		if errorRetryable.Valid {
			t.Fatalf("turn %q retryable = %v, want NULL", turnID, errorRetryable.Bool)
		}
	} else if !errorRetryable.Valid || errorRetryable.Bool != *want.retryable {
		t.Fatalf("turn %q retryable = (%v, %v), want %v", turnID, errorRetryable.Bool, errorRetryable.Valid, *want.retryable)
	}
}

func assertPersistedSeekDBUserTurn(
	t *testing.T,
	turn PersistedTurn,
	conversationID string,
	content string,
	externalMessageID string,
	wantSequence uint64,
) {
	t.Helper()
	if turn.ID == "" || turn.ConversationID != conversationID || turn.UserMessage.ID == "" ||
		turn.UserMessage.ConversationID != conversationID || turn.UserMessage.TurnID != turn.ID ||
		turn.UserMessage.Role != "user" || turn.UserMessage.Content != content ||
		turn.UserMessage.MessageID != externalMessageID || turn.UserMessage.Sequence != wantSequence ||
		len(turn.UserMessage.Parts) != 0 || turn.UserMessage.CreatedAtUnixMS <= 0 {
		t.Fatalf("persisted user turn = %#v", turn)
	}
}

func assertSeekDBAssistantMessage(
	t *testing.T,
	message MessageRecord,
	conversationID string,
	turnID string,
	content string,
	wantSequence uint64,
	wantParts []historyexpr.Part,
) {
	t.Helper()
	partsMatch := len(message.Parts) == 0 && len(wantParts) == 0
	if len(wantParts) > 0 {
		partsMatch = reflect.DeepEqual(message.Parts, wantParts)
	}
	if message.ID == "" || message.ConversationID != conversationID || message.TurnID != turnID ||
		message.MessageID != "" || message.Sequence != wantSequence || message.Role != "assistant" ||
		message.Content != content || message.CreatedAtUnixMS <= 0 || !partsMatch {
		t.Fatalf("assistant message = %#v, want content %q sequence %d parts %#v", message, content, wantSequence, wantParts)
	}
}

func assertSeekDBInitiationEvidence(t *testing.T, database *sql.DB, turnID string, want []string) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `
SELECT evidence_id FROM conversation_turn_evidence WHERE turn_id = ? ORDER BY evidence_id`, turnID)
	if err != nil {
		t.Fatalf("load initiation evidence: %v", err)
	}
	defer rows.Close()
	got := make([]string, 0, len(want))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan initiation evidence: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate initiation evidence: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("initiation evidence = %#v, want %#v", got, want)
	}
}

func assertSeekDBTurnAssistantCount(t *testing.T, database *sql.DB, turnID string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_messages WHERE turn_id = ? AND role = 'assistant'`, turnID).Scan(&got); err != nil {
		t.Fatalf("count assistant messages for turn %q: %v", turnID, err)
	}
	if got != want {
		t.Fatalf("assistant messages for turn %q = %d, want %d", turnID, got, want)
	}
}

func assertSeekDBCorrelatedTurnProjection(t *testing.T, messages []MessageRecord, turnID, externalMessageID string) {
	t.Helper()
	roles := make(map[string]bool, 2)
	for _, message := range messages {
		if message.TurnID != turnID {
			continue
		}
		if message.MessageID != externalMessageID {
			t.Fatalf("correlated %s message ID = %q, want %q", message.Role, message.MessageID, externalMessageID)
		}
		roles[message.Role] = true
	}
	if !roles["user"] || !roles["assistant"] || len(roles) != 2 {
		t.Fatalf("correlated turn roles = %#v, want user and assistant", roles)
	}
}

func assertSeekDBExpressionSnapshot(t *testing.T, messages []MessageRecord, turnID string, want []historyexpr.Part) {
	t.Helper()
	for _, message := range messages {
		if message.TurnID == turnID && message.Role == "assistant" {
			if !reflect.DeepEqual(message.Parts, want) || message.Content != historyexpr.TextProjection(want) {
				t.Fatalf("expression snapshot = %#v, want %#v", message, want)
			}
			return
		}
	}
	t.Fatalf("assistant expression for turn %q was not loaded", turnID)
}

func seekDBConversationRowCount(t *testing.T, database *sql.DB, table, conversationID string) int {
	t.Helper()
	if table != "conversation_turns" && table != "conversation_messages" {
		t.Fatalf("unsupported count table %q", table)
	}
	var count int
	query := "SELECT COUNT(*) FROM " + table + " WHERE conversation_id = ?"
	if err := database.QueryRowContext(t.Context(), query, conversationID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func seekDBEvidenceCountForConversation(t *testing.T, database *sql.DB, conversationID string) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM conversation_turn_evidence e
JOIN conversation_turns t ON t.id = e.turn_id
WHERE t.conversation_id = ?`, conversationID).Scan(&count); err != nil {
		t.Fatalf("count conversation evidence: %v", err)
	}
	return count
}

func seekDBConversationMaxSequence(t *testing.T, database *sql.DB, table, conversationID string) uint64 {
	t.Helper()
	if table != "conversation_turns" && table != "conversation_messages" {
		t.Fatalf("unsupported sequence table %q", table)
	}
	var sequence int64
	query := "SELECT COALESCE(MAX(sequence), 0) FROM " + table + " WHERE conversation_id = ?"
	if err := database.QueryRowContext(t.Context(), query, conversationID).Scan(&sequence); err != nil {
		t.Fatalf("max %s sequence: %v", table, err)
	}
	if sequence < 0 {
		t.Fatalf("max %s sequence = %d", table, sequence)
	}
	return uint64(sequence)
}

func seekDBConversationSequencesAfter(
	t *testing.T,
	database *sql.DB,
	table string,
	conversationID string,
	after uint64,
) []uint64 {
	t.Helper()
	if table != "conversation_turns" && table != "conversation_messages" {
		t.Fatalf("unsupported sequence table %q", table)
	}
	query := "SELECT sequence FROM " + table + " WHERE conversation_id = ? AND sequence > ? ORDER BY sequence"
	rows, err := database.QueryContext(t.Context(), query, conversationID, int64(after))
	if err != nil {
		t.Fatalf("list %s sequences: %v", table, err)
	}
	defer rows.Close()
	sequences := make([]uint64, 0)
	for rows.Next() {
		var sequence int64
		if err := rows.Scan(&sequence); err != nil {
			t.Fatalf("scan %s sequence: %v", table, err)
		}
		if sequence <= 0 {
			t.Fatalf("invalid %s sequence %d", table, sequence)
		}
		sequences = append(sequences, uint64(sequence))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s sequences: %v", table, err)
	}
	return sequences
}

func assertSeekDBContiguousSequences(t *testing.T, label string, first uint64, sequences []uint64, count int) {
	t.Helper()
	if len(sequences) != count {
		t.Fatalf("%s sequences = %#v, want %d values", label, sequences, count)
	}
	for index, sequence := range sequences {
		want := first + uint64(index)
		if sequence != want {
			t.Fatalf("%s sequence[%d] = %d, want %d", label, index, sequence, want)
		}
	}
}

func seekDBBool(value bool) *bool { return &value }
