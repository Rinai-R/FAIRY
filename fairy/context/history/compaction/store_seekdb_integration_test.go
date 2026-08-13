//go:build integration

package compaction

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

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	historytranscript "fairy/context/history/transcript"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBCompactionCommitsAreAtomicConcurrentAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openCompactionSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeCompactionSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB compaction schema: %v", err)
	}
	store, runtimeStore, transcriptStore := newSeekDBCompactionTestStores(
		t, database, runtimeConfig.QueryLimit,
	)

	persisted := make(map[string]seekDBCompactionDomainSnapshot)
	persisted["conversation-commit-prompt"] = assertSeekDBPromptWindowCommit(
		t, database, store, runtimeStore, "conversation-commit-prompt",
	)
	persisted["conversation-commit-compaction"] = assertSeekDBAtomicCompactionCommit(
		t, database, store, runtimeStore, "conversation-commit-compaction",
	)
	persisted["conversation-commit-projection"] = assertSeekDBProjectionCommit(
		t, database, store, runtimeStore, transcriptStore, "conversation-commit-projection",
	)
	persisted["conversation-commit-tiered"] = assertSeekDBTieredCompactionCommit(
		t, database, store, runtimeStore, transcriptStore, "conversation-commit-tiered",
	)
	persisted["conversation-commit-race"] = assertSeekDBL2L3Race(
		t, database, store, runtimeStore, "conversation-commit-race",
	)

	assertSeekDBCompactionBoundariesAndCancellation(
		t, database, store, runtimeStore,
	)
	assertSeekDBOldPlansRejectTranscriptChanges(
		t, database, store, runtimeStore, transcriptStore,
	)
	assertSeekDBCompactionInjectedRollbacks(
		t, database, store, runtimeStore,
	)

	closeCompactionSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB compaction runtime: %v", err)
	}
	instance, database, closed = restarted, restarted.SQL(), false
	_, restartedRuntimeStore, _ := newSeekDBCompactionTestStores(
		t, database, runtimeConfig.QueryLimit,
	)
	for conversationID, want := range persisted {
		got := loadSeekDBCompactionDomainSnapshot(
			t, database, restartedRuntimeStore, conversationID, historyruntime.PromptLaneRespond,
		)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("state for %s after restart = %#v, want %#v", conversationID, got, want)
		}
	}
}

func assertSeekDBOldPlansRejectTranscriptChanges(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	transcriptStore *historytranscript.Store,
) {
	t.Helper()
	type mutationCase struct {
		name           string
		conversationID string
		seedActive     bool
		mutate         func(string) error
		commit         func(string, historytranscript.TranscriptBoundary) error
	}
	cases := []mutationCase{
		{
			name:           "begin user turn invalidates legacy prompt commit",
			conversationID: "conversation-boundary-begin",
			mutate: func(conversationID string) error {
				_, err := transcriptStore.BeginTurnContext(t.Context(), conversationID, "late user turn")
				return err
			},
			commit: func(conversationID string, boundary historytranscript.TranscriptBoundary) error {
				_, err := store.CommitPromptWindowContext(
					t.Context(), conversationID, 1, boundary, "old prompt summary",
				)
				return err
			},
		},
		{
			name:           "begin initiation invalidates legacy compaction",
			conversationID: "conversation-boundary-initiation",
			mutate: func(conversationID string) error {
				_, err := transcriptStore.BeginInitiationTurnContext(
					t.Context(), conversationID, []string{"observation-late"},
				)
				return err
			},
			commit: func(conversationID string, boundary historytranscript.TranscriptBoundary) error {
				_, err := store.CommitCompactionContext(
					t.Context(), conversationID, 1, boundary, "old compaction summary",
					seekDBCompactionWindow(conversationID, 2, "context-old-initiation-plan"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name:           "complete turn invalidates projection",
			conversationID: "conversation-boundary-complete",
			seedActive:     true,
			mutate: func(conversationID string) error {
				_, err := transcriptStore.CompleteTurnContext(
					t.Context(), conversationID, seekDBBoundaryActiveTurnID(conversationID), "late assistant reply",
				)
				return err
			},
			commit: func(conversationID string, boundary historytranscript.TranscriptBoundary) error {
				_, err := store.CommitPromptProjectionContext(
					t.Context(), conversationID, 1, 1, boundary,
					seekDBMemoryProjection(1, 2, boundary.MessageSequence+1, "old-complete-plan"),
					seekDBCompactionWindow(conversationID, 2, "context-old-complete-plan"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name:           "published interrupt invalidates tiered compaction",
			conversationID: "conversation-boundary-interrupt",
			seedActive:     true,
			mutate: func(conversationID string) error {
				_, err := transcriptStore.InterruptTurnContext(
					t.Context(), conversationID, seekDBBoundaryActiveTurnID(conversationID), "late published prefix",
				)
				return err
			},
			commit: func(conversationID string, boundary historytranscript.TranscriptBoundary) error {
				_, err := store.CommitTieredCompactionContext(
					t.Context(), conversationID, 1, 1, boundary,
					"old tiered summary", 2,
					seekDBFullProjection(1, 2, boundary.MessageSequence+1, 2),
					seekDBCompactionWindow(conversationID, 2, "context-old-interrupt-plan"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
	}
	for _, test := range cases {
		seedSeekDBCompactionTranscript(t, database, test.conversationID)
		if test.seedActive {
			seedSeekDBBoundaryActiveTurn(t, database, test.conversationID)
		}
		seedSeekDBCompactionRuntimeState(
			t, runtimeStore, test.conversationID, 1, "old-plan-"+test.conversationID,
		)
		planned, err := transcriptStore.LoadConversationPromptContext(t.Context(), test.conversationID)
		if err != nil {
			t.Fatalf("load %s old plan: %v", test.name, err)
		}
		if err := test.mutate(test.conversationID); err != nil {
			t.Fatalf("%s mutation: %v", test.name, err)
		}
		current, err := transcriptStore.LoadConversationPromptContext(t.Context(), test.conversationID)
		if err != nil {
			t.Fatalf("load %s current boundary: %v", test.name, err)
		}
		if current.TranscriptBoundary == planned.TranscriptBoundary {
			t.Fatalf("%s did not advance transcript boundary %#v", test.name, planned.TranscriptBoundary)
		}
		beforeRejectedCommit := loadSeekDBCompactionDomainSnapshot(
			t, database, runtimeStore, test.conversationID, historyruntime.PromptLaneRespond,
		)
		if err := test.commit(test.conversationID, planned.TranscriptBoundary); !errors.Is(err, ErrPromptWindowRevisionChanged) {
			t.Fatalf("%s commit error = %v, want %v", test.name, err, ErrPromptWindowRevisionChanged)
		}
		assertSeekDBCompactionDomainUnchanged(
			t, database, runtimeStore, test.conversationID, beforeRejectedCommit,
		)
	}

	assertSeekDBTerminalWithoutMessageKeepsBoundary(
		t, database, store, runtimeStore, transcriptStore,
		"conversation-boundary-fail", false,
	)
	assertSeekDBTerminalWithoutMessageKeepsBoundary(
		t, database, store, runtimeStore, transcriptStore,
		"conversation-boundary-empty-interrupt", true,
	)
}

func assertSeekDBTerminalWithoutMessageKeepsBoundary(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	transcriptStore *historytranscript.Store,
	conversationID string,
	emptyInterrupt bool,
) {
	t.Helper()
	seedSeekDBCompactionTranscript(t, database, conversationID)
	seedSeekDBBoundaryActiveTurn(t, database, conversationID)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, conversationID, 1, "terminal-no-message")
	planned, err := transcriptStore.LoadConversationPromptContext(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if emptyInterrupt {
		message, err := transcriptStore.InterruptTurnContext(
			t.Context(), conversationID, seekDBBoundaryActiveTurnID(conversationID), "",
		)
		if err != nil || message != nil {
			t.Fatalf("empty interrupt = (%#v, %v)", message, err)
		}
	} else if err := transcriptStore.FailTurnContext(
		t.Context(), conversationID, seekDBBoundaryActiveTurnID(conversationID),
		"MODEL_FAILED", "provider unavailable", true,
	); err != nil {
		t.Fatalf("fail turn without message: %v", err)
	}
	current, err := transcriptStore.LoadConversationPromptContext(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if current.TranscriptBoundary != planned.TranscriptBoundary {
		t.Fatalf("terminal without message boundary = %#v, want %#v", current.TranscriptBoundary, planned.TranscriptBoundary)
	}
	if emptyInterrupt {
		_, err = store.CommitCompactionContext(
			t.Context(), conversationID, 1, planned.TranscriptBoundary, "empty interrupt summary",
			seekDBCompactionWindow(conversationID, 2, "context-empty-interrupt"),
			historyruntime.PromptLaneRespond,
		)
	} else {
		_, err = store.CommitPromptWindowContext(
			t.Context(), conversationID, 1, planned.TranscriptBoundary, "failed turn summary",
		)
	}
	if err != nil {
		t.Fatalf("terminal without message compaction: %v", err)
	}
	prompt := loadSeekDBPromptSnapshot(t, database, conversationID)
	if prompt.revision != 2 || prompt.cutoff != planned.TranscriptBoundary.MessageSequence {
		t.Fatalf("terminal without message prompt = %#v", prompt)
	}
}

func assertSeekDBPromptWindowCommit(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	conversationID string,
) seekDBCompactionDomainSnapshot {
	t.Helper()
	seedSeekDBCompactionTranscript(t, database, conversationID)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, conversationID, 1, "prompt-sentinel")

	result, err := store.CommitPromptWindowContext(
		t.Context(), conversationID, 1, seekDBCompactionBoundary(), "  已压缩摘要  ",
	)
	if err != nil || result.WindowRevision != 2 || result.RetainedDialogueItems != 0 {
		t.Fatalf("CommitPromptWindowContext() = (%#v, %v)", result, err)
	}
	after := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	assertSeekDBPromptState(t, after.prompt, seekDBPromptStateExpectation{
		revision: 2, projectionRevision: 1, summary: seekDBString("已压缩摘要"),
		cutoff: 4, projection: historyprojection.Empty(),
	})
	if !after.contextFound || after.context.WindowID != "context-prompt-sentinel" ||
		!after.continuationFound || after.continuation.PreviousResponseID != "response-prompt-sentinel" {
		t.Fatalf("prompt-only commit changed runtime state: %#v", after)
	}
	if _, err := store.CommitPromptWindowContext(
		t.Context(), conversationID, 1, seekDBCompactionBoundary(), "stale summary",
	); !errors.Is(err, ErrPromptWindowRevisionChanged) {
		t.Fatalf("stale prompt commit error = %v, want %v", err, ErrPromptWindowRevisionChanged)
	}
	assertSeekDBCompactionDomainUnchanged(t, database, runtimeStore, conversationID, after)
	return after
}

func assertSeekDBAtomicCompactionCommit(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	conversationID string,
) seekDBCompactionDomainSnapshot {
	t.Helper()
	seedSeekDBCompactionTranscript(t, database, conversationID)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, conversationID, 1, "compaction-sentinel")
	compactContinuation := seekDBCompactionContinuation(
		conversationID, historyruntime.PromptLaneCompact, 1, "compact-preserved",
	)
	if _, err := runtimeStore.SaveLaneContinuationContext(t.Context(), compactContinuation); err != nil {
		t.Fatalf("seed compact lane: %v", err)
	}

	window := seekDBCompactionWindow(conversationID, 2, "context-compaction-success")
	result, err := store.CommitCompactionContext(
		t.Context(), conversationID, 1, seekDBCompactionBoundary(), "atomic summary", window, historyruntime.PromptLaneRespond,
	)
	if err != nil || result.WindowRevision != 2 {
		t.Fatalf("CommitCompactionContext() = (%#v, %v)", result, err)
	}
	after := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	assertSeekDBPromptState(t, after.prompt, seekDBPromptStateExpectation{
		revision: 2, projectionRevision: 1, summary: seekDBString("atomic summary"),
		cutoff: 4, projection: historyprojection.Empty(),
	})
	if !after.contextFound || !equalSeekDBContextWindowIgnoringTimestamp(after.context, window) || after.continuationFound {
		t.Fatalf("atomic compaction runtime state = %#v", after)
	}
	assertSeekDBContinuation(t, runtimeStore, compactContinuation)

	staleContinuation := seekDBCompactionContinuation(
		conversationID, historyruntime.PromptLaneRespond, 2, "stale-preserved",
	)
	staleContinuation, err = runtimeStore.SaveLaneContinuationContext(t.Context(), staleContinuation)
	if err != nil {
		t.Fatalf("save stale sentinel continuation: %v", err)
	}
	beforeStale := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	if _, err := store.CommitCompactionContext(
		t.Context(), conversationID, 1, seekDBCompactionBoundary(), "stale summary",
		seekDBCompactionWindow(conversationID, 2, "context-stale"), historyruntime.PromptLaneRespond,
	); !errors.Is(err, ErrPromptWindowRevisionChanged) {
		t.Fatalf("stale compaction error = %v, want %v", err, ErrPromptWindowRevisionChanged)
	}
	assertSeekDBCompactionDomainUnchanged(t, database, runtimeStore, conversationID, beforeStale)
	return beforeStale
}

func assertSeekDBProjectionCommit(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	transcriptStore *historytranscript.Store,
	conversationID string,
) seekDBCompactionDomainSnapshot {
	t.Helper()
	seedSeekDBCompactionTranscript(t, database, conversationID)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, conversationID, 1, "projection-sentinel")
	projection := seekDBMemoryProjection(1, 2, 3, "memory-projection")
	window := seekDBCompactionWindow(conversationID, 2, "context-projection-success")
	result, err := store.CommitPromptProjectionContext(
		t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(),
		projection, window, historyruntime.PromptLaneRespond,
	)
	if err != nil || result.WindowRevision != 2 {
		t.Fatalf("CommitPromptProjectionContext() = (%#v, %v)", result, err)
	}
	after := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	assertSeekDBPromptState(t, after.prompt, seekDBPromptStateExpectation{
		revision: 2, projectionRevision: 2, cutoff: 0, projection: projection,
	})
	if !after.contextFound || !equalSeekDBContextWindowIgnoringTimestamp(after.context, window) || after.continuationFound {
		t.Fatalf("projection runtime state = %#v", after)
	}
	assertSeekDBActiveAndCompleteTranscript(t, transcriptStore, conversationID, []uint64{3, 4}, 4)

	staleContinuation := seekDBCompactionContinuation(
		conversationID, historyruntime.PromptLaneRespond, 2, "projection-stale-preserved",
	)
	if _, err := runtimeStore.SaveLaneContinuationContext(t.Context(), staleContinuation); err != nil {
		t.Fatalf("save projection stale continuation: %v", err)
	}
	beforeStale := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	if _, err := store.CommitPromptProjectionContext(
		t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(), projection,
		seekDBCompactionWindow(conversationID, 2, "context-projection-stale"),
		historyruntime.PromptLaneRespond,
	); !errors.Is(err, ErrPromptWindowRevisionChanged) {
		t.Fatalf("stale projection error = %v, want %v", err, ErrPromptWindowRevisionChanged)
	}
	assertSeekDBCompactionDomainUnchanged(t, database, runtimeStore, conversationID, beforeStale)
	return beforeStale
}

func assertSeekDBTieredCompactionCommit(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	transcriptStore *historytranscript.Store,
	conversationID string,
) seekDBCompactionDomainSnapshot {
	t.Helper()
	seedSeekDBCompactionTranscript(t, database, conversationID)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, conversationID, 1, "tiered-sentinel")
	projection := seekDBFullProjection(1, 2, 3, 2)
	window := seekDBCompactionWindow(conversationID, 2, "context-tiered-success")
	result, err := store.CommitTieredCompactionContext(
		t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(), "tiered summary", 2,
		projection, window, historyruntime.PromptLaneRespond,
	)
	if err != nil || result.WindowRevision != 2 {
		t.Fatalf("CommitTieredCompactionContext() = (%#v, %v)", result, err)
	}
	after := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	assertSeekDBPromptState(t, after.prompt, seekDBPromptStateExpectation{
		revision: 2, projectionRevision: 2, summary: seekDBString("tiered summary"),
		cutoff: 2, projection: projection,
	})
	if !after.contextFound || !equalSeekDBContextWindowIgnoringTimestamp(after.context, window) || after.continuationFound {
		t.Fatalf("tiered compaction runtime state = %#v", after)
	}
	assertSeekDBActiveAndCompleteTranscript(t, transcriptStore, conversationID, []uint64{3, 4}, 4)

	staleContinuation := seekDBCompactionContinuation(
		conversationID, historyruntime.PromptLaneRespond, 2, "tiered-stale-preserved",
	)
	if _, err := runtimeStore.SaveLaneContinuationContext(t.Context(), staleContinuation); err != nil {
		t.Fatalf("save tiered stale continuation: %v", err)
	}
	beforeStale := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	if _, err := store.CommitTieredCompactionContext(
		t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(), "stale tiered", 4,
		seekDBFullProjection(1, 4, 5, 3),
		seekDBCompactionWindow(conversationID, 2, "context-tiered-stale"),
		historyruntime.PromptLaneRespond,
	); !errors.Is(err, ErrPromptWindowRevisionChanged) {
		t.Fatalf("stale tiered compaction error = %v, want %v", err, ErrPromptWindowRevisionChanged)
	}
	assertSeekDBCompactionDomainUnchanged(t, database, runtimeStore, conversationID, beforeStale)
	return beforeStale
}

func assertSeekDBL2L3Race(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
	conversationID string,
) seekDBCompactionDomainSnapshot {
	t.Helper()
	seedSeekDBCompactionTranscript(t, database, conversationID)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, conversationID, 1, "race-sentinel")

	type outcome struct {
		kind string
		err  error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, err := store.CommitPromptProjectionContext(
			t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(),
			seekDBMemoryProjection(1, 2, 3, "race-memory"),
			seekDBCompactionWindow(conversationID, 2, "context-race-l2"),
			historyruntime.PromptLaneRespond,
		)
		results <- outcome{kind: "projection", err: err}
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := store.CommitTieredCompactionContext(
			t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(), "race tiered", 2,
			seekDBFullProjection(1, 2, 3, 2),
			seekDBCompactionWindow(conversationID, 2, "context-race-l3"),
			historyruntime.PromptLaneRespond,
		)
		results <- outcome{kind: "tiered", err: err}
	}()
	close(start)
	wait.Wait()
	close(results)
	var winner string
	var successCount, conflictCount int
	for result := range results {
		switch {
		case result.err == nil:
			successCount++
			winner = result.kind
		case errors.Is(result.err, ErrPromptWindowRevisionChanged):
			conflictCount++
		default:
			t.Fatalf("%s race commit error = %v", result.kind, result.err)
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("race success/conflict = %d/%d, want 1/1", successCount, conflictCount)
	}

	after := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	if after.prompt.revision != 2 || after.prompt.projectionRevision != 2 ||
		!after.contextFound || after.continuationFound {
		t.Fatalf("race domain state = %#v", after)
	}
	switch winner {
	case "projection":
		assertSeekDBPromptState(t, after.prompt, seekDBPromptStateExpectation{
			revision: 2, projectionRevision: 2, cutoff: 0,
			projection: seekDBMemoryProjection(1, 2, 3, "race-memory"),
		})
		if after.context.WindowID != "context-race-l2" {
			t.Fatalf("projection winner context = %#v", after.context)
		}
	case "tiered":
		assertSeekDBPromptState(t, after.prompt, seekDBPromptStateExpectation{
			revision: 2, projectionRevision: 2, summary: seekDBString("race tiered"),
			cutoff: 2, projection: seekDBFullProjection(1, 2, 3, 2),
		})
		if after.context.WindowID != "context-race-l3" {
			t.Fatalf("tiered winner context = %#v", after.context)
		}
	default:
		t.Fatalf("unknown race winner %q", winner)
	}
	return after
}

func assertSeekDBCompactionBoundariesAndCancellation(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
) {
	t.Helper()

	const boundaryConversation = "conversation-compaction-boundaries"
	seedSeekDBCompactionTranscript(t, database, boundaryConversation)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, boundaryConversation, 1, "boundary-sentinel")
	baseline := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, boundaryConversation, historyruntime.PromptLaneRespond,
	)
	boundaryCases := []struct {
		name string
		call func() error
	}{
		{
			name: "tiered cutoff exceeds transcript",
			call: func() error {
				_, err := store.CommitTieredCompactionContext(
					t.Context(), boundaryConversation, 1, 1, seekDBCompactionBoundary(), "invalid cutoff", 5,
					historyprojection.Empty(),
					seekDBCompactionWindow(boundaryConversation, 2, "context-invalid-cutoff"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name: "projection omission exceeds transcript",
			call: func() error {
				_, err := store.CommitPromptProjectionContext(
					t.Context(), boundaryConversation, 1, 1, seekDBCompactionBoundary(),
					seekDBMemoryProjection(1, 5, 0, "invalid-range"),
					seekDBCompactionWindow(boundaryConversation, 2, "context-invalid-range"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name: "projection recent tail exceeds transcript",
			call: func() error {
				state := historyprojection.Empty()
				state.RecentTailStartSequence = 6
				_, err := store.CommitPromptProjectionContext(
					t.Context(), boundaryConversation, 1, 1, seekDBCompactionBoundary(), state,
					seekDBCompactionWindow(boundaryConversation, 2, "context-invalid-tail"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
	}
	for _, test := range boundaryCases {
		if err := test.call(); err == nil {
			t.Fatalf("%s error = nil", test.name)
		}
		assertSeekDBCompactionDomainUnchanged(
			t, database, runtimeStore, boundaryConversation, baseline,
		)
	}

	const canceledConversation = "conversation-compaction-canceled"
	seedSeekDBCompactionTranscript(t, database, canceledConversation)
	seedSeekDBCompactionRuntimeState(t, runtimeStore, canceledConversation, 1, "cancel-sentinel")
	canceledBaseline := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, canceledConversation, historyruntime.PromptLaneRespond,
	)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	canceledWindow := seekDBCompactionWindow(canceledConversation, 2, "context-canceled")
	canceledCalls := []struct {
		name string
		call func() error
	}{
		{
			name: "prompt window",
			call: func() error {
				_, err := store.CommitPromptWindowContext(canceled, canceledConversation, 1, seekDBCompactionBoundary(), "canceled")
				return err
			},
		},
		{
			name: "compaction",
			call: func() error {
				_, err := store.CommitCompactionContext(
					canceled, canceledConversation, 1, seekDBCompactionBoundary(), "canceled", canceledWindow,
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name: "projection",
			call: func() error {
				_, err := store.CommitPromptProjectionContext(
					canceled, canceledConversation, 1, 1, seekDBCompactionBoundary(),
					historyprojection.Empty(), canceledWindow, historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name: "tiered",
			call: func() error {
				_, err := store.CommitTieredCompactionContext(
					canceled, canceledConversation, 1, 1, seekDBCompactionBoundary(), "canceled", 0,
					historyprojection.Empty(), canceledWindow, historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
	}
	for _, test := range canceledCalls {
		if err := test.call(); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled %s error = %v, want context.Canceled", test.name, err)
		}
		assertSeekDBCompactionDomainUnchanged(
			t, database, runtimeStore, canceledConversation, canceledBaseline,
		)
	}
}

func assertSeekDBCompactionInjectedRollbacks(
	t *testing.T,
	database *sql.DB,
	store *Store,
	runtimeStore *historyruntime.Store,
) {
	t.Helper()
	var errInjected = errors.New("injected compaction failure")
	store.seekDBWriteHook = nil
	t.Cleanup(func() { store.seekDBWriteHook = nil })

	type hookCase struct {
		name           string
		conversationID string
		stages         []seekDBWriteStage
		call           func(string) error
	}
	cases := []hookCase{
		{
			name:           "prompt",
			conversationID: "conversation-rollback-prompt",
			stages: []seekDBWriteStage{
				seekDBStagePromptAfterCAS,
				seekDBStagePromptBeforeCommit,
			},
			call: func(conversationID string) error {
				_, err := store.CommitPromptWindowContext(
					t.Context(), conversationID, 1, seekDBCompactionBoundary(), "injected prompt",
				)
				return err
			},
		},
		{
			name:           "compaction",
			conversationID: "conversation-rollback-compaction",
			stages: []seekDBWriteStage{
				seekDBStageCompactionAfterPrompt,
				seekDBStageCompactionAfterContext,
				seekDBStageCompactionAfterClear,
				seekDBStageCompactionBeforeCommit,
			},
			call: func(conversationID string) error {
				_, err := store.CommitCompactionContext(
					t.Context(), conversationID, 1, seekDBCompactionBoundary(), "injected compaction",
					seekDBCompactionWindow(conversationID, 2, "context-injected-compaction"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name:           "projection",
			conversationID: "conversation-rollback-projection",
			stages: []seekDBWriteStage{
				seekDBStageProjectionAfterPrompt,
				seekDBStageProjectionAfterContext,
				seekDBStageProjectionAfterClear,
				seekDBStageProjectionBeforeCommit,
			},
			call: func(conversationID string) error {
				_, err := store.CommitPromptProjectionContext(
					t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(),
					seekDBMemoryProjection(1, 2, 3, "injected-memory"),
					seekDBCompactionWindow(conversationID, 2, "context-injected-projection"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
		{
			name:           "tiered",
			conversationID: "conversation-rollback-tiered",
			stages: []seekDBWriteStage{
				seekDBStageTieredAfterPrompt,
				seekDBStageTieredAfterContext,
				seekDBStageTieredAfterClear,
				seekDBStageTieredBeforeCommit,
			},
			call: func(conversationID string) error {
				_, err := store.CommitTieredCompactionContext(
					t.Context(), conversationID, 1, 1, seekDBCompactionBoundary(), "injected tiered", 2,
					seekDBFullProjection(1, 2, 3, 2),
					seekDBCompactionWindow(conversationID, 2, "context-injected-tiered"),
					historyruntime.PromptLaneRespond,
				)
				return err
			},
		},
	}

	for _, test := range cases {
		seedSeekDBCompactionTranscript(t, database, test.conversationID)
		seedSeekDBCompactionRuntimeState(
			t, runtimeStore, test.conversationID, 1, "rollback-"+test.name,
		)
		baseline := loadSeekDBCompactionDomainSnapshot(
			t, database, runtimeStore, test.conversationID, historyruntime.PromptLaneRespond,
		)
		for _, stage := range test.stages {
			stage := stage
			store.seekDBWriteHook = func(got seekDBWriteStage) error {
				if got == stage {
					return errInjected
				}
				return nil
			}
			if err := test.call(test.conversationID); !errors.Is(err, errInjected) {
				t.Fatalf("%s failure at %s = %v, want injected error", test.name, stage, err)
			}
			store.seekDBWriteHook = nil
			assertSeekDBCompactionDomainUnchanged(
				t, database, runtimeStore, test.conversationID, baseline,
			)
		}
	}
	store.seekDBWriteHook = nil
}

type seekDBPromptSnapshot struct {
	revision           uint64
	summary            *string
	cutoff             uint64
	projectionRevision uint64
	projection         historyprojection.State
	updatedAtUnixMS    int64
}

type seekDBCompactionDomainSnapshot struct {
	prompt              seekDBPromptSnapshot
	context             historyruntime.ContextWindowRecord
	contextFound        bool
	continuation        historyruntime.LaneContinuationRecord
	continuationFound   bool
	completeMessageRows int
}

type seekDBPromptStateExpectation struct {
	revision           uint64
	summary            *string
	cutoff             uint64
	projectionRevision uint64
	projection         historyprojection.State
}

func loadSeekDBCompactionDomainSnapshot(
	t *testing.T,
	database *sql.DB,
	runtimeStore *historyruntime.Store,
	conversationID, lane string,
) seekDBCompactionDomainSnapshot {
	t.Helper()
	snapshot := seekDBCompactionDomainSnapshot{
		prompt: loadSeekDBPromptSnapshot(t, database, conversationID),
	}
	var err error
	snapshot.context, snapshot.contextFound, err = runtimeStore.LoadContextWindowContext(
		t.Context(), conversationID, lane,
	)
	if err != nil {
		t.Fatalf("load context window snapshot: %v", err)
	}
	snapshot.continuation, snapshot.continuationFound, err = runtimeStore.LoadLaneContinuationContext(
		t.Context(), conversationID, lane,
	)
	if err != nil {
		t.Fatalf("load continuation snapshot: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ?`, conversationID,
	).Scan(&snapshot.completeMessageRows); err != nil {
		t.Fatalf("count complete transcript: %v", err)
	}
	return snapshot
}

func loadSeekDBPromptSnapshot(t *testing.T, database *sql.DB, conversationID string) seekDBPromptSnapshot {
	t.Helper()
	var revision, cutoff, projectionRevision int64
	var summary sql.NullString
	var encoded []byte
	var snapshot seekDBPromptSnapshot
	if err := database.QueryRowContext(t.Context(), `
SELECT revision, summary, cutoff_message_sequence, projection_revision,
       CAST(projection_state AS CHAR), updated_at_ms
FROM prompt_windows WHERE conversation_id = ?`, conversationID).Scan(
		&revision, &summary, &cutoff, &projectionRevision, &encoded, &snapshot.updatedAtUnixMS,
	); err != nil {
		t.Fatalf("load SeekDB prompt snapshot: %v", err)
	}
	if revision <= 0 || cutoff < 0 || projectionRevision <= 0 || snapshot.updatedAtUnixMS < 0 {
		t.Fatalf("invalid stored SeekDB prompt snapshot numbers: %d/%d/%d/%d", revision, cutoff, projectionRevision, snapshot.updatedAtUnixMS)
	}
	snapshot.revision = uint64(revision)
	snapshot.cutoff = uint64(cutoff)
	snapshot.projectionRevision = uint64(projectionRevision)
	if summary.Valid {
		snapshot.summary = seekDBString(summary.String)
	}
	projection, err := historyprojection.Decode(encoded)
	if err != nil {
		t.Fatalf("decode stored projection: %v", err)
	}
	snapshot.projection = projection
	return snapshot
}

func assertSeekDBPromptState(t *testing.T, got seekDBPromptSnapshot, want seekDBPromptStateExpectation) {
	t.Helper()
	if got.revision != want.revision || got.cutoff != want.cutoff ||
		got.projectionRevision != want.projectionRevision ||
		!reflect.DeepEqual(got.summary, want.summary) ||
		!reflect.DeepEqual(got.projection, want.projection) {
		t.Fatalf("prompt state = %#v, want %#v", got, want)
	}
}

func assertSeekDBCompactionDomainUnchanged(
	t *testing.T,
	database *sql.DB,
	runtimeStore *historyruntime.Store,
	conversationID string,
	want seekDBCompactionDomainSnapshot,
) {
	t.Helper()
	got := loadSeekDBCompactionDomainSnapshot(
		t, database, runtimeStore, conversationID, historyruntime.PromptLaneRespond,
	)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compaction domain after rejected mutation = %#v, want %#v", got, want)
	}
}

func seedSeekDBCompactionTranscript(t *testing.T, database *sql.DB, conversationID string) {
	t.Helper()
	now := int64(1_786_600_000_000)
	characterID := "character-" + conversationID
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`, conversationID, characterID, now, now); err != nil {
		t.Fatalf("seed compaction conversation: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, 'character')`, characterID, conversationID); err != nil {
		t.Fatalf("seed compaction character binding: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO prompt_windows(
  conversation_id, revision, summary, cutoff_message_sequence,
  projection_revision, projection_state, updated_at_ms
) VALUES (?, 1, NULL, 0, 1, ?, ?)`,
		conversationID, `{"version":1,"omissions":[]}`, now,
	); err != nil {
		t.Fatalf("seed compaction prompt window: %v", err)
	}
	for turnIndex := 1; turnIndex <= 2; turnIndex++ {
		turnID := fmt.Sprintf("%s-turn-%d", conversationID, turnIndex)
		created := now + int64(turnIndex*100)
		if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, sequence, status, origin, extraction_state,
  extraction_attempt_count, extraction_next_attempt_at_ms, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, 'completed', 'user', 'ineligible', 0, 0, ?, ?)`,
			turnID, conversationID, turnIndex, created, created,
		); err != nil {
			t.Fatalf("seed compaction turn %d: %v", turnIndex, err)
		}
		userSequence := (turnIndex-1)*2 + 1
		assistantSequence := userSequence + 1
		if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversation_messages(
  id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, 'user', ?, ?, ?)`,
			fmt.Sprintf("%s-message-%d", conversationID, userSequence), conversationID, turnID,
			userSequence, fmt.Sprintf("user-%d", turnIndex), `[]`, created,
		); err != nil {
			t.Fatalf("seed compaction user message %d: %v", userSequence, err)
		}
		if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversation_messages(
  id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, 'assistant', ?, ?, ?)`,
			fmt.Sprintf("%s-message-%d", conversationID, assistantSequence), conversationID, turnID,
			assistantSequence, fmt.Sprintf("assistant-%d", turnIndex), `[]`, created+1,
		); err != nil {
			t.Fatalf("seed compaction assistant message %d: %v", assistantSequence, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit compaction transcript seed: %v", err)
	}
}

func seedSeekDBCompactionRuntimeState(
	t *testing.T,
	runtimeStore *historyruntime.Store,
	conversationID string,
	revision uint64,
	suffix string,
) {
	t.Helper()
	continuation := seekDBCompactionContinuation(
		conversationID, historyruntime.PromptLaneRespond, revision, suffix,
	)
	if _, err := runtimeStore.SaveLaneContinuationContext(t.Context(), continuation); err != nil {
		t.Fatalf("seed compaction continuation: %v", err)
	}
	window := seekDBCompactionWindow(conversationID, revision, "context-"+suffix)
	if _, err := runtimeStore.SaveContextWindowContext(t.Context(), window); err != nil {
		t.Fatalf("seed compaction context window: %v", err)
	}
}

func seedSeekDBBoundaryActiveTurn(t *testing.T, database *sql.DB, conversationID string) {
	t.Helper()
	turnID := seekDBBoundaryActiveTurnID(conversationID)
	const (
		turnSequence    = 3
		messageSequence = 5
	)
	now := int64(1_786_600_001_000)
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, sequence, status, origin, extraction_state,
  extraction_attempt_count, extraction_next_attempt_at_ms, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, 'planning', 'user', 'ineligible', 0, 0, ?, ?)`,
		turnID, conversationID, turnSequence, now, now,
	); err != nil {
		t.Fatalf("seed boundary active turn: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
INSERT INTO conversation_messages(
  id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, 'user', 'active user message', ?, ?)`,
		conversationID+"-message-5", conversationID, turnID, messageSequence, `[]`, now,
	); err != nil {
		t.Fatalf("seed boundary active message: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit boundary active turn seed: %v", err)
	}
}

func seekDBBoundaryActiveTurnID(conversationID string) string {
	return conversationID + "-turn-active"
}

func seekDBCompactionContinuation(
	conversationID, lane string,
	revision uint64,
	suffix string,
) historyruntime.LaneContinuationRecord {
	return historyruntime.LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               lane,
		PreviousResponseID: "response-" + suffix,
		RequestShapeHash:   seekDBCompactionHash("shape-" + suffix),
		InputPrefixHash:    seekDBCompactionHash("input-" + suffix),
		ResponseItemHash:   seekDBCompactionHash("response-" + suffix),
		WindowRevision:     revision,
	}
}

func seekDBCompactionWindow(
	conversationID string,
	revision uint64,
	windowID string,
) historyruntime.ContextWindowRecord {
	return historyruntime.ContextWindowRecord{
		ConversationID:       conversationID,
		Lane:                 historyruntime.PromptLaneRespond,
		WindowNumber:         revision,
		FirstWindowID:        "context-first",
		WindowID:             windowID,
		LastTrigger:          "compaction_committed",
		PromptWindowRevision: revision,
	}
}

func seekDBMemoryProjection(start, end, recent uint64, memoryID string) historyprojection.State {
	return historyprojection.State{
		Version: historyprojection.Version,
		Omissions: []historyprojection.Omission{{
			StartMessageSequence: start,
			EndMessageSequence:   end,
			Reason:               "memory_committed",
			MemoryID:             memoryID,
		}},
		RecentTailStartSequence: recent,
	}
}

func seekDBFullProjection(start, end, recent, compactRevision uint64) historyprojection.State {
	return historyprojection.State{
		Version: historyprojection.Version,
		Omissions: []historyprojection.Omission{{
			StartMessageSequence: start,
			EndMessageSequence:   end,
			Reason:               "full_compact",
			CompactRevision:      compactRevision,
		}},
		RecentTailStartSequence: recent,
	}
}

func seekDBCompactionBoundary() historytranscript.TranscriptBoundary {
	return historytranscript.TranscriptBoundary{TurnSequence: 2, MessageSequence: 4}
}

func assertSeekDBContinuation(
	t *testing.T,
	runtimeStore *historyruntime.Store,
	want historyruntime.LaneContinuationRecord,
) {
	t.Helper()
	got, found, err := runtimeStore.LoadLaneContinuationContext(
		t.Context(), want.ConversationID, want.Lane,
	)
	if err != nil || !found {
		t.Fatalf("load continuation = (%#v, %v, %v)", got, found, err)
	}
	want.UpdatedAtUnixMS = got.UpdatedAtUnixMS
	if got != want {
		t.Fatalf("continuation = %#v, want %#v", got, want)
	}
}

func assertSeekDBActiveAndCompleteTranscript(
	t *testing.T,
	store *historytranscript.Store,
	conversationID string,
	wantActiveSequences []uint64,
	wantCompleteCount int,
) {
	t.Helper()
	active, err := store.LoadConversationPromptContext(t.Context(), conversationID)
	if err != nil {
		t.Fatalf("load active prompt: %v", err)
	}
	complete, err := store.LoadConversationContext(t.Context(), conversationID)
	if err != nil {
		t.Fatalf("load complete transcript: %v", err)
	}
	if len(active.Messages) != len(wantActiveSequences) || len(complete.Messages) != wantCompleteCount {
		t.Fatalf("active/complete message counts = %d/%d, want %d/%d", len(active.Messages), len(complete.Messages), len(wantActiveSequences), wantCompleteCount)
	}
	for index, sequence := range wantActiveSequences {
		if active.Messages[index].Sequence != sequence {
			t.Fatalf("active message %d sequence = %d, want %d", index, active.Messages[index].Sequence, sequence)
		}
	}
}

func equalSeekDBContextWindowIgnoringTimestamp(
	got, want historyruntime.ContextWindowRecord,
) bool {
	want.UpdatedAtUnixMS = got.UpdatedAtUnixMS
	return reflect.DeepEqual(got, want)
}

func seekDBString(value string) *string { return &value }

func seekDBCompactionHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func newSeekDBCompactionTestStores(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
) (*Store, *historyruntime.Store, *historytranscript.Store) {
	t.Helper()
	store, err := NewSeekDBStore(database, queryLimit)
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore, err := historyruntime.NewSeekDBStore(database, queryLimit)
	if err != nil {
		t.Fatal(err)
	}
	transcriptStore, err := historytranscript.NewSeekDBStore(database, queryLimit)
	if err != nil {
		t.Fatal(err)
	}
	return store, runtimeStore, transcriptStore
}

func openCompactionSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	config := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-compaction"),
		Address:       reserveCompactionLoopbackAddress(t),
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
		t.Fatalf("open real SeekDB compaction runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveCompactionLoopbackAddress(t *testing.T) string {
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

func closeCompactionSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB compaction runtime: %v", err)
	}
}
