package participation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/character"
	"fairy/config"
	domain "fairy/internal/domain/interaction"
	"fairy/internal/domain/persona"
	"fairy/memory"
	"fairy/model"
)

const protocolCompileRetries = 2

type DecisionHost interface {
	LoadConversation(conversationID string) (memory.ConversationBootstrap, error)
	ResolveInteraction(conversationID string) (domain.Resolved, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ListSocialPersonNotes(context.Context, string, string, []string) ([]memory.SocialPersonNote, error)
	RetrieveSocialMemoryContext(context.Context, string, string, string) (memory.SocialMemoryContext, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
}

type Engine struct {
	host DecisionHost
}

func NewEngine(host DecisionHost) *Engine {
	return &Engine{host: host}
}

func (e *Engine) DecideParticipation(ctx context.Context, request ParticipationRequest) (ParticipationResult, error) {
	if ctx == nil {
		return ParticipationResult{}, errors.New("context is required")
	}
	if err := ValidateParticipationRequest(request); err != nil {
		return ParticipationResult{}, err
	}
	if e == nil || e.host == nil {
		return ParticipationResult{}, errors.New("participation runtime is not configured")
	}
	resolved, err := e.host.ResolveInteraction(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, err
	}
	if resolved.Memory != domain.MemoryPublic || !resolved.AllowsAmbientParticipation() {
		return ParticipationResult{}, errors.New("participation requires a public ambient interaction")
	}
	bootstrap, err := e.host.LoadConversation(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, fmt.Errorf("loading ambient conversation: %w", err)
	}
	record, err := e.host.ActiveCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return ParticipationResult{}, err
	}
	now := time.Now().UnixMilli()
	presence, err := DeriveRecentPresence(bootstrap.Messages, now)
	if err != nil {
		return ParticipationResult{}, err
	}
	input, err := BuildParticipationInputWithSignals(record, resolved, request.EvaluationReason, request.Messages, request.CacheMessages, presence, now, bootstrap.Messages)
	if err != nil {
		return ParticipationResult{}, err
	}
	behavior, err := e.behaviorContext(ctx, bootstrap.Conversation.CharacterID, request.ConversationID, request.Messages)
	if err != nil {
		return ParticipationResult{}, err
	}
	if behavior != nil {
		input = append(input, *behavior)
	}
	notes, err := e.host.ListSocialPersonNotes(ctx, bootstrap.Conversation.CharacterID, request.ConversationID, SenderIDs(request.Messages))
	if err != nil {
		return ParticipationResult{}, fmt.Errorf("listing social person notes: %w", err)
	}
	if len(notes) > 0 {
		item, notesErr := persona.EncodeSocialPersonNotes(notes)
		if notesErr != nil {
			return ParticipationResult{}, notesErr
		}
		input = append(input, item)
	}
	connection, err := e.host.ModelConnection()
	if err != nil {
		return ParticipationResult{}, err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneParticipate)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneParticipate, connection.Model, request.ConversationID, ParticipationInstructions)
	cacheInput.CharacterRevision = record.Revision
	compiled := model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneParticipate, Model: connection.Model,
			Instructions: ParticipationInstructions, MaxOutputTokens: ParticipationMaxOutputTokens,
			PromptCacheKey: cacheKey,
		},
		Input:      input,
		CacheInput: &cacheInput,
	}
	var firstCompileErr error
	usage := make([]model.LaneModelUsage, 0)
	for attempt := 1; attempt <= protocolCompileRetries+1; attempt++ {
		events, executeErr := e.host.ExecuteRequest(ctx, compiled)
		if executeErr != nil {
			return ParticipationResult{}, fmt.Errorf("executing participation decision: %w", executeErr)
		}
		if len(model.FunctionCallsFromEvents(events)) != 0 {
			return ParticipationResult{}, errors.New("participation decision returned tool calls")
		}
		usage = append(usage, model.LaneUsageFromEvents(model.PromptLaneParticipate, events, 0)...)
		result, compileErr := CompileParticipation(model.CollectTextFromEvents(events), request.Messages)
		if compileErr == nil {
			if result.Action == ParticipationReply && request.EvaluationReason == ParticipationReasonMessage && (result.TargetMessageID == nil || !isNewTarget(request.Messages, *result.TargetMessageID)) {
				return ParticipationResult{Action: ParticipationSilent, Usage: usage}, nil
			}
			result.Usage = usage
			return result, nil
		}
		if attempt == 1 {
			firstCompileErr = compileErr
		}
		if attempt > protocolCompileRetries {
			return ParticipationResult{}, fmt.Errorf("participation decision remained invalid after %d retries: first attempt: %v; final attempt: %w", protocolCompileRetries, firstCompileErr, compileErr)
		}
	}
	return ParticipationResult{}, errors.New("participation decision retry loop exhausted")
}

func (e *Engine) behaviorContext(ctx context.Context, characterID, conversationID string, messages []AmbientObservation) (*model.PromptItem, error) {
	query := BehaviorQuery(messages)
	if query == "" {
		query = "群聊互动"
	}
	retrieved, err := e.host.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
	if err != nil {
		return nil, fmt.Errorf("retrieving participation social behavior: %w", err)
	}
	return BehaviorItem(retrieved)
}

func BehaviorItem(retrieved memory.SocialMemoryContext) (*model.PromptItem, error) {
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
	item, err := persona.EncodeSocialMemoryContext(memory.SocialMemoryContext{Entries: behaviors})
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func BehaviorQuery(messages []AmbientObservation) string {
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
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, " ")
}

func isNewTarget(messages []AmbientObservation, targetMessageID string) bool {
	for _, message := range messages {
		if message.IsNew && message.MessageID == targetMessageID {
			return true
		}
	}
	return false
}
