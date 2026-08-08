package conversation

import (
	"errors"

	"fairy/agent/conversation/contextplan"
	"fairy/agent/conversation/lifecycle"
	historycompaction "fairy/context/history/compaction"
	historyprojection "fairy/context/history/projection"
	history "fairy/context/history/transcript"
	"fairy/runtime/model"
)

func (s *Service) coordinatePressureCompaction(
	conversationID string,
	promptTokens uint64,
	cacheObservation model.CachedTokenObservation,
	predictive bool,
) error {
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return err
	}
	policy := contextplan.PolicyFromContextWindow(connection.ContextWindowTokens)
	if promptTokens < policy.SoftInputTokens {
		return nil
	}
	afterTokens := promptTokens
	resolved, err := s.ResolveInteraction(conversationID)
	if err != nil {
		return err
	}
	if resolved.AllowsPersonalMemory() {
		afterTokens, err = s.commitAvailableL2Projection(
			conversationID, promptTokens, policy.TargetInputTokens,
			cacheObservation, policy.HardPressure(promptTokens),
		)
		if err != nil {
			return err
		}
		if afterTokens <= policy.TargetInputTokens {
			return nil
		}
	}
	if !policy.HardPressure(afterTokens) {
		return nil
	}
	_, err = s.compactConversation(conversationID, "hard_watermark")
	if errors.Is(err, ErrTurnInProgress) && predictive {
		return nil
	}
	return err
}

func (s *Service) commitAvailableL2Projection(
	conversationID string,
	currentTokens, targetTokens uint64,
	cacheObservation model.CachedTokenObservation,
	hardPressure bool,
) (uint64, error) {
	for attempt := 0; attempt < 2; attempt++ {
		bootstrap, err := s.memory.turn.promptContext.LoadConversationPrompt(conversationID)
		if err != nil {
			return currentTokens, err
		}
		coverage, err := s.memory.turn.memoryCoverage.LoadCommittedMemoryCoverage(conversationID)
		if err != nil {
			return currentTokens, err
		}
		recentTail := recentTailStartSequence(bootstrap.Messages, 2)
		plan := contextplan.PlanMemoryProjection(contextplan.MemoryProjectionInput{
			Coverage: coverage, ExistingProjection: bootstrap.PromptWindow.Projection,
			AllowsPersonalMemory: true, RecentTailStartSequence: recentTail,
			CurrentTokens: currentTokens, TargetTokens: targetTokens,
			CacheObservation: cacheObservation, ExpectedFutureCalls: 2,
			HardPressure: hardPressure,
		})
		if len(plan.Omissions) == 0 {
			return currentTokens, nil
		}
		projection := bootstrap.PromptWindow.Projection
		projection.Omissions = append(append([]historyprojection.Omission(nil), projection.Omissions...), plan.Omissions...)
		projection.RecentTailStartSequence = recentTail
		existingWindow, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
		if err != nil {
			return currentTokens, err
		}
		contextWindow := contextplan.NextCommittedWindow(
			conversationID, bootstrap.PromptWindow.Revision+1, existingWindow, found,
		)
		contextWindow.LastTrigger = "memory_projection_committed"
		_, err = s.memory.turn.contextRetention.CommitPromptProjection(
			conversationID,
			bootstrap.PromptWindow.Revision,
			bootstrap.PromptWindow.ProjectionRevision,
			projection,
			contextWindow,
			string(model.PromptLaneRespond),
		)
		if errors.Is(err, historycompaction.ErrPromptWindowRevisionChanged) {
			continue
		}
		if err != nil {
			return currentTokens, err
		}
		s.loopMetrics.compactionApplied("l2")
		if len(bootstrap.Messages) > 0 {
			s.appendRuntimeLedger(
				conversationID, bootstrap.Messages[len(bootstrap.Messages)-1].TurnID,
				runtimeLedgerEventCompaction, lifecycle.StateCompleted, "",
				runtimeCompactionLedgerMetadata(
					"l2", "committed_memory", watermarkName(hardPressure),
					plan.CandidateCount, len(plan.Omissions),
					plan.ReleasedTokens, plan.InvalidatedCacheTokens,
					cacheObservation, model.CacheMissing(),
					currentTokens, plan.AfterTokens,
					bootstrap.PromptWindow.ProjectionRevision+1,
				),
			)
		}
		if s.retention != nil {
			s.retention.TakeCommittedCoverage(conversationID)
		}
		return plan.AfterTokens, nil
	}
	return currentTokens, historycompaction.ErrPromptWindowRevisionChanged
}

func watermarkName(hard bool) string {
	if hard {
		return "hard"
	}
	return "soft"
}

func recentTailStartSequence(messages []history.MessageRecord, keepTurns int) uint64 {
	if len(messages) == 0 {
		return 0
	}
	if keepTurns < 1 {
		keepTurns = 1
	}
	turns := make(map[string]struct{}, keepTurns)
	start := messages[len(messages)-1].Sequence
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		turns[message.TurnID] = struct{}{}
		if len(turns) > keepTurns {
			break
		}
		start = message.Sequence
	}
	return start
}
