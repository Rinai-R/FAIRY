package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/interaction"
	"fairy/memory"
	"fairy/model"
)

// ParticipationEngine owns public ambient reply|wait|silent decisions.
type ParticipationEngine struct {
	host *CompanionService
}

func (e *ParticipationEngine) DecideParticipation(ctx context.Context, request ParticipationRequest) (ParticipationResult, error) {
	s := e.host
	if ctx == nil {
		return ParticipationResult{}, errors.New("context is required")
	}
	if err := ValidateParticipationRequest(request); err != nil {
		return ParticipationResult{}, err
	}
	if s == nil || s.memoryPort() == nil || s.modelPort() == nil || s.characterCatalog() == nil || s.configSource() == nil {
		return ParticipationResult{}, ErrRespondRuntimeNotMigrated
	}
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, err
	}
	if resolved.Memory != interaction.MemoryPublic || !resolved.AllowsAmbientParticipation() {
		return ParticipationResult{}, errors.New("participation requires a public ambient interaction")
	}
	bootstrap, err := s.memoryPort().LoadConversation(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, fmt.Errorf("loading ambient conversation: %w", err)
	}
	record, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return ParticipationResult{}, err
	}
	presence, err := DeriveRecentPresence(bootstrap.Messages, time.Now().UnixMilli())
	if err != nil {
		return ParticipationResult{}, err
	}
	input, err := BuildParticipationInputWithSignals(record, resolved, request.EvaluationReason, request.Messages, request.CacheMessages, presence, time.Now().UnixMilli(), bootstrap.Messages)
	if err != nil {
		return ParticipationResult{}, err
	}
	behaviorItem, err := s.participationBehaviorContext(ctx, bootstrap.Conversation.CharacterID, request.ConversationID, request.Messages)
	if err != nil {
		return ParticipationResult{}, err
	}
	if behaviorItem != nil {
		input = append(input, *behaviorItem)
	}
	notes, err := s.memoryPort().ListSocialPersonNotes(ctx, bootstrap.Conversation.CharacterID, request.ConversationID, ambientSenderIDs(request.Messages))
	if err != nil {
		return ParticipationResult{}, fmt.Errorf("listing social person notes: %w", err)
	}
	if len(notes) > 0 {
		notesItem, notesErr := encodeSocialPersonNotes(notes)
		if notesErr != nil {
			return ParticipationResult{}, notesErr
		}
		input = append(input, notesItem)
	}
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return ParticipationResult{}, err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneParticipate)
	}
	compiledRequest := model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneParticipate, Model: connection.Model,
			Instructions: ParticipationInstructions, MaxOutputTokens: ParticipationMaxOutputTokens,
			PromptCacheKey: cacheKey,
		},
		Input: input,
	}
	var (
		firstCompileErr error
		usage           []LaneModelUsage
	)
	for attempt := 1; attempt <= maxProtocolCompileRetries+1; attempt++ {
		events, executeErr := s.modelPort().ExecuteRequestContext(ctx, compiledRequest)
		if executeErr != nil {
			return ParticipationResult{}, fmt.Errorf("executing participation decision: %w", executeErr)
		}
		if len(model.FunctionCallsFromEvents(events)) != 0 {
			return ParticipationResult{}, errors.New("participation decision returned tool calls")
		}
		usage = append(usage, laneUsageFromEventsForLane(model.PromptLaneParticipate, events, 0)...)
		result, compileErr := CompileParticipation(model.CollectTextFromEvents(events), request.Messages)
		if compileErr == nil {
			if result.Action == ParticipationReply && request.EvaluationReason == ParticipationReasonMessage && (result.TargetMessageID == nil || !isNewParticipationTarget(request.Messages, *result.TargetMessageID)) {
				return ParticipationResult{Action: ParticipationSilent, Usage: usage}, nil
			}
			result.Usage = usage
			return result, nil
		}
		if attempt == 1 {
			firstCompileErr = compileErr
		}
		if attempt <= maxProtocolCompileRetries {
			continue
		}
		return ParticipationResult{}, fmt.Errorf("participation decision remained invalid after %d retries: first attempt: %v; final attempt: %w", maxProtocolCompileRetries, firstCompileErr, compileErr)
	}
	return ParticipationResult{}, errors.New("participation decision retry loop exhausted")
}

func (s *CompanionService) participationBehaviorContext(ctx context.Context, characterID, conversationID string, messages []AmbientObservation) (*model.PromptItem, error) {
	if s == nil || s.memoryPort() == nil {
		return nil, nil
	}
	query := participationBehaviorQuery(messages)
	if query == "" {
		query = "群聊互动"
	}
	retrieved, err := s.memoryPort().RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
	if err != nil {
		return nil, fmt.Errorf("retrieving participation social behavior: %w", err)
	}
	behaviors := make([]memory.SocialMemoryEntry, 0, 3)
	for _, entry := range retrieved.Entries {
		if entry.Kind != memory.SocialMemoryBehavior {
			continue
		}
		behaviors = append(behaviors, entry)
		if len(behaviors) >= 3 {
			break
		}
	}
	if len(behaviors) == 0 {
		return nil, nil
	}
	item, err := encodeSocialMemoryContext(memory.SocialMemoryContext{Entries: behaviors})
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func participationBehaviorQuery(messages []AmbientObservation) string {
	const maxMessages = 3
	const maxRunesPerMessage = 60
	parts := make([]string, 0, maxMessages)
	for index := len(messages) - 1; index >= 0 && len(parts) < maxMessages; index-- {
		text := strings.TrimSpace(messages[index].Text)
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > maxRunesPerMessage {
			text = string(runes[:maxRunesPerMessage])
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return ""
	}
	// Reverse so older → newer order for a stable situation cue.
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, " ")
}

func isNewParticipationTarget(messages []AmbientObservation, targetMessageID string) bool {
	for _, message := range messages {
		if message.IsNew && message.MessageID == targetMessageID {
			return true
		}
	}
	return false
}
