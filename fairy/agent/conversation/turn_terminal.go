package conversation

import (
	"errors"
	"slices"
	"strings"
	"time"

	"fairy/agent/conversation/contextplan"
	"fairy/agent/conversation/lifecycle"
	"fairy/agent/reply"
	history "fairy/context/history/transcript"
	"fairy/transport/session"
)

type turnExecution struct {
	engine        *TurnEngine
	service       *Service
	request       SubmitCompiledTurnRequest
	persisted     history.PersistedTurn
	life          *lifecycle.Lifecycle
	finalDelivery *reply.Delivery
	beatIndex     int
}

type turnLearningPolicy struct {
	extraction bool
	social     bool
	knowledge  bool
}

func resolveTurnLearningPolicy(resolved session.Resolved) turnLearningPolicy {
	if resolved.IsEvaluation() {
		return turnLearningPolicy{}
	}
	return turnLearningPolicy{
		extraction: resolved.AllowsPersonalMemory(),
		social:     resolved.AllowsAmbientParticipation() && !resolved.AllowsPersonalMemory(),
		knowledge:  true,
	}
}

func (x *turnExecution) persist(gathered *turnContext, resolved session.Resolved, turnStarted time.Time) (TurnOutcome, error) {
	reply := gathered.reply
	profileRevision := gathered.profileRevision
	events := slices.Clone(gathered.events)
	fullRequest := gathered.fullRequest
	finalUsage := slices.Clone(gathered.finalUsage)
	bootstrap := gathered.bootstrap
	learning := resolveTurnLearningPolicy(resolved)
	if _, err := x.service.memory.turn.turns.CompleteExpressionTurnForPolicy(
		x.request.ConversationID,
		x.persisted.ID,
		reply.DisplayText,
		memoryExpressionParts(reply.Chains),
		learning.extraction,
	); err != nil {
		return x.service.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, nil, err)
	}
	if _, err := x.service.publishLife(x.life, func() (session.Event, error) {
		return x.life.Complete(lifecycle.Completion{
			Text:                reply.DisplayText,
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
	x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTerminal, lifecycle.StateCompleted, "", runtimeTerminalLedgerMetadata("completed", reply, finalUsage))
	contextWindow, err := x.service.recordObservedContextWindow(x.request.ConversationID, bootstrap.PromptWindow.Revision, finalUsage)
	if err != nil {
		x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventContextWindow, lifecycle.StateCompleted, "CONTEXT_WINDOW_STATE_FAILED", runtimeFailureLedgerMetadata("CONTEXT_WINDOW_STATE_FAILED", err, false))
	} else {
		x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventContextWindow, lifecycle.StateCompleted, "", contextplan.WindowLedgerMetadata(contextWindow))
	}
	if err := x.service.updateContinuationState(x.request.ConversationID, gathered.connectionConfig.Capabilities.CacheRetention, bootstrap.PromptWindow.Revision, fullRequest, reply.DisplayText, events); err != nil {
		x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventContinuation, lifecycle.StateCompleted, "CONTINUATION_STATE_FAILED", runtimeFailureLedgerMetadata("CONTINUATION_STATE_FAILED", err, false))
	}
	if x.service.retention != nil {
		knowledgeTasks := gathered.knowledgeTasks
		if !learning.knowledge {
			knowledgeTasks = nil
		}
		x.service.retention.ObserveCompletedTurn(RetentionCompletion{
			ConversationID:        x.request.ConversationID,
			ExtractPersonalMemory: learning.extraction,
			KnowledgeTasks:        knowledgeTasks,
		})
	}
	if learning.social && x.service.ambientReplies != nil && strings.TrimSpace(reply.DisplayText) != "" {
		candidates := socialMemoryFeedbackCandidates(gathered.socialFeedbackContext)
		if len(candidates) > 0 {
			x.service.ambientReplies.ObserveAmbientReply(AmbientReply{
				CharacterID: bootstrap.Conversation.CharacterID, ConversationID: x.request.ConversationID,
				TurnID: x.persisted.ID, Candidates: candidates, ReplyText: reply.DisplayText,
			})
		}
	}
	x.service.scheduleAutoCompaction(x.request.ConversationID, events)
	return TurnOutcome{
		ConversationID:  x.request.ConversationID,
		TurnID:          x.persisted.ID,
		ResponseText:    reply.DisplayText,
		VisualState:     reply.VisualState,
		Chains:          reply.Chains,
		RespondMigrated: true,
	}, nil
}

func (x *turnExecution) fail(code string, cause error) (TurnOutcome, error) {
	s := x.service
	if errors.Is(cause, ErrTurnInterrupted) {
		var published []reply.ReplyChain
		planned := 0
		if x.finalDelivery != nil {
			published = x.finalDelivery.Snapshot()
			planned = x.finalDelivery.PlannedCount()
		}
		prefix := ""
		if len(published) > 0 {
			compiled, err := reply.CompiledReplyFromChains(published)
			if err != nil {
				return s.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, cause, err)
			}
			prefix = compiled.DisplayText
		}
		if _, err := s.memory.turn.turns.InterruptExpressionTurn(
			x.request.ConversationID,
			x.persisted.ID,
			prefix,
			memoryExpressionParts(published),
		); err != nil {
			return s.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, cause, err)
		}
		if _, err := s.publishLife(x.life, x.life.Interrupt); err != nil {
			return TurnOutcome{}, errors.Join(cause, err)
		}
		s.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTerminal, lifecycle.StateInterrupted, code, runtimeInterruptedTerminalLedgerMetadata(planned, published))
		return TurnOutcome{}, cause
	}
	if err := s.memory.turn.turns.FailTurn(x.request.ConversationID, x.persisted.ID, code, cause.Error(), false); err != nil {
		return s.terminalPersistenceFailure(x.life, x.request.ConversationID, x.persisted.ID, cause, err)
	}
	if _, err := s.publishLife(x.life, func() (session.Event, error) {
		return x.life.Fail(code, cause.Error(), false)
	}); err != nil {
		return TurnOutcome{}, errors.Join(cause, err)
	}
	s.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTerminal, lifecycle.StateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
	return TurnOutcome{}, cause
}

func (x *turnExecution) transition(state lifecycle.State) error {
	if _, err := x.service.publishLife(x.life, func() (session.Event, error) {
		return x.life.Transition(state)
	}); err != nil {
		return err
	}
	x.service.appendRuntimeLedger(x.request.ConversationID, x.persisted.ID, runtimeLedgerEventTransition, state, "", map[string]any{
		"source": "turn_lifecycle",
	})
	return nil
}
