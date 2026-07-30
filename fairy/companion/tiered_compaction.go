package companion

import (
	"errors"

	"fairy/memory"
	"fairy/model"
)

func (s *CompanionService) coordinatePressureCompaction(
	conversationID string,
	promptTokens uint64,
	cacheObservation model.CachedTokenObservation,
	predictive bool,
) error {
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return err
	}
	policy := compactionPolicyFromContextWindow(connection.ContextWindowTokens)
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
			cacheObservation, policy.hardPressure(promptTokens),
		)
		if err != nil {
			return err
		}
		if afterTokens <= policy.TargetInputTokens {
			return nil
		}
	}
	if !policy.hardPressure(afterTokens) {
		return nil
	}
	_, err = s.compactConversation(conversationID, "hard_watermark")
	if errors.Is(err, ErrTurnInProgress) && predictive {
		return nil
	}
	return err
}

func (s *CompanionService) commitAvailableL2Projection(
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
		coverage, err := s.memory.turn.contextRetention.LoadCommittedMemoryCoverage(conversationID)
		if err != nil {
			return currentTokens, err
		}
		recentTail := recentTailStartSequence(bootstrap.Messages, 2)
		plan := planL2MemoryCompaction(l2PlanningInput{
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
		projection.Omissions = append(append([]memory.PromptProjectionOmission(nil), projection.Omissions...), plan.Omissions...)
		projection.RecentTailStartSequence = recentTail
		existingWindow, found, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
		if err != nil {
			return currentTokens, err
		}
		contextWindow := nextCompactionCommittedContextWindowRecord(
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
		if errors.Is(err, memory.ErrPromptWindowRevisionChanged) {
			continue
		}
		if err != nil {
			return currentTokens, err
		}
		if len(bootstrap.Messages) > 0 {
			s.appendRuntimeLedger(
				conversationID, bootstrap.Messages[len(bootstrap.Messages)-1].TurnID,
				runtimeLedgerEventCompaction, turnStateCompleted, "",
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
			s.retention.takeCommittedCoverage(conversationID)
		}
		return plan.AfterTokens, nil
	}
	return currentTokens, memory.ErrPromptWindowRevisionChanged
}

func watermarkName(hard bool) string {
	if hard {
		return "hard"
	}
	return "soft"
}

func recentTailStartSequence(messages []memory.MessageRecord, keepTurns int) uint64 {
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
