package companion

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"fairy/memory"
	replyapp "fairy/reply"
	"fairy/sociallearning"

	domain "fairy/interaction"
)

type turnExecution struct {
	engine          *TurnEngine
	service         *CompanionService
	request         SubmitCompiledTurnRequest
	persisted       memory.PersistedTurn
	life            *TurnLifecycle
	speechFlow      *replyapp.SpeechPipeline
	finalDelivery   *replyapp.Delivery
	speechPlayIndex int
	speechRequested bool
}

func (x *turnExecution) declarePersistNode(ctx context.Context, gathered *turnGraphState, resolved domain.Resolved, turnStarted time.Time) {
	_, _ = x.engine.declareOutcomeNode(ctx, gathered, x.request.ConversationID, x.persisted.ID, "persist", "complete_turn", TurnStateCompleted, func() (TurnOutcome, error) {
		reply := gathered.reply
		profileRevision := gathered.profileRevision
		events := slices.Clone(gathered.events)
		fullRequest := gathered.fullRequest
		finalUsage := slices.Clone(gathered.finalUsage)
		ingestSnapshots := slices.Clone(gathered.ingestSnapshots)
		bootstrap := gathered.bootstrap
		if _, err := x.service.memory.turn.turns.CompleteTurn(x.request.ConversationID, x.persisted.ID, reply.DisplayText); err != nil {
			return x.service.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, nil, err)
		}
		if _, err := x.service.publishLife(x.life, func() (TurnEvent, error) {
			return x.life.Complete(TurnCompletion{
				Text:                reply.DisplayText,
				SpeechText:          reply.SpeechText,
				CharacterRevision:   gathered.character.Revision,
				UserProfileRevision: profileRevision,
				Usage:               finalUsage,
				VisualState:         reply.VisualState,
				Chains:              reply.Chains,
			})
		}); err != nil {
			return x.fail("INVALID_STATE_TRANSITION", err)
		}
		x.service.loopMetrics.completed(time.Since(turnStarted))
		x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTerminal, TurnStateCompleted, "", runtimeTerminalLedgerMetadata("completed", reply, finalUsage))
		contextWindow, err := x.service.recordObservedContextWindow(x.request.ConversationID, bootstrap.PromptWindow.Revision, finalUsage)
		if err != nil {
			x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventContextWindow, TurnStateCompleted, "CONTEXT_WINDOW_STATE_FAILED", runtimeFailureLedgerMetadata("CONTEXT_WINDOW_STATE_FAILED", err, false))
		} else {
			x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventContextWindow, TurnStateCompleted, "", runtimeContextWindowLedgerMetadata(contextWindow))
		}
		if err := x.service.updateContinuationState(x.request.ConversationID, gathered.connectionConfig.Capabilities.CacheRetention, bootstrap.PromptWindow.Revision, fullRequest, reply.DisplayText, events); err != nil {
			x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventContinuation, TurnStateCompleted, "CONTINUATION_STATE_FAILED", runtimeFailureLedgerMetadata("CONTINUATION_STATE_FAILED", err, false))
		}
		if resolved.AllowsPersonalMemory() {
			x.service.scheduleBackgroundExtraction(x.request.ConversationID)
		}
		if resolved.AllowsAmbientParticipation() && !resolved.AllowsPersonalMemory() && x.service.socialFeedback != nil && strings.TrimSpace(reply.DisplayText) != "" {
			entryIDs := []string(nil)
			if gathered.socialContext != nil {
				entryIDs = sociallearning.MemoryEntryIDs(gathered.socialContext.Memory)
			}
			x.service.socialFeedback.Register(sociallearning.FeedbackRegistration{
				CharacterID: bootstrap.Conversation.CharacterID, ConversationID: x.request.ConversationID,
				TurnID: x.persisted.ID, EntryIDs: entryIDs, ReplyText: reply.DisplayText,
			})
		}
		x.service.scheduleAutoCompaction(x.request.ConversationID, events)
		x.service.scheduleKnowledgeIngest(ingestSnapshots)
		return TurnOutcome{
			ConversationID:  x.request.ConversationID,
			TurnID:          x.persisted.ID,
			ResponseText:    reply.DisplayText,
			SpeechText:      reply.SpeechText,
			SpeechRequested: x.request.SpeechEnabled,
			VisualState:     reply.VisualState,
			Chains:          reply.Chains,
			RespondMigrated: true,
		}, nil
	})
}

func (x *turnExecution) fail(code string, cause error) (TurnOutcome, error) {
	if x.speechFlow != nil {
		x.speechFlow.Close()
	}
	s := x.service
	if errors.Is(cause, ErrTurnInterrupted) {
		var published []ReplyChain
		planned := 0
		if x.finalDelivery != nil {
			published = x.finalDelivery.Snapshot()
			planned = x.finalDelivery.PlannedCount()
		}
		prefix := ""
		if len(published) > 0 {
			reply, err := compiledReplyFromChains(published)
			if err != nil {
				return s.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, cause, err)
			}
			prefix = reply.DisplayText
		}
		if _, err := s.memory.turn.turns.InterruptTurn(x.request.ConversationID, x.persisted.ID, prefix); err != nil {
			return s.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, cause, err)
		}
		if _, err := s.publishLife(x.life, x.life.Interrupt); err != nil {
			return TurnOutcome{}, errors.Join(cause, err)
		}
		s.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTerminal, TurnStateInterrupted, code, runtimeInterruptedTerminalLedgerMetadata(planned, published))
		return TurnOutcome{}, cause
	}
	if err := s.memory.turn.turns.FailTurn(x.request.ConversationID, x.persisted.ID, code, cause.Error(), false); err != nil {
		return s.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, cause, err)
	}
	if _, err := s.publishLife(x.life, func() (TurnEvent, error) {
		return x.life.Fail(code, cause.Error(), false)
	}); err != nil {
		return TurnOutcome{}, errors.Join(cause, err)
	}
	s.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
	return TurnOutcome{}, cause
}

func (x *turnExecution) transition(state TurnState) error {
	if _, err := x.service.publishLife(x.life, func() (TurnEvent, error) {
		return x.life.Transition(state)
	}); err != nil {
		return err
	}
	x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTransition, state, "", map[string]any{
		"source": "turn_lifecycle",
	})
	return nil
}
