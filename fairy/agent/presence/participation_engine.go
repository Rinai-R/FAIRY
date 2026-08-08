package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/transport/session"
)

const protocolCompileRetries = 2

type DecisionHost interface {
	LoadConversationActivity(conversationID string, nowUnixMS int64) (history.ConversationActivity, error)
	ResolveInteraction(conversationID string) (session.Resolved, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ListSocialPersonNotes(context.Context, string, string, []string) ([]social.SocialPersonNote, error)
	RetrieveSocialMemoryContext(context.Context, string, string, string) (social.SocialMemoryContext, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
}

type Engine struct {
	host   DecisionHost
	tracer ParticipationTraceObserver
}

func NewEngine(host DecisionHost, tracer ParticipationTraceObserver) *Engine {
	return &Engine{host: host, tracer: tracer}
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
	trace := newParticipationTrace(e.tracer, request.Messages)
	contextSpan := trace.start("参与上下文准备", "context", nil)
	compiled, err := e.prepareParticipation(ctx, request)
	if err != nil {
		status, attributes := participationTraceFailure(ctx, participationTraceContextUnavailable)
		trace.finish(contextSpan, status, attributes)
		return ParticipationResult{}, err
	}
	trace.finish(contextSpan, "completed", map[string]string{"itemCount": fmt.Sprint(len(compiled.Input))})

	var firstCompileErr error
	usage := make([]model.LaneModelUsage, 0)
	for attempt := 1; attempt <= protocolCompileRetries+1; attempt++ {
		modelSpan := trace.start("参与模型调用", "model", map[string]string{
			"attempt": fmt.Sprint(attempt), "lane": string(model.PromptLaneParticipate),
		})
		events, executeErr := e.host.ExecuteRequest(ctx, compiled)
		if executeErr != nil {
			status, attributes := participationTraceFailure(ctx, participationTraceModelFailed)
			attributes["attempt"] = fmt.Sprint(attempt)
			attributes["lane"] = string(model.PromptLaneParticipate)
			trace.finish(modelSpan, status, attributes)
			return ParticipationResult{}, fmt.Errorf("executing participation decision: %w", executeErr)
		}
		trace.finish(modelSpan, "completed", participationModelTraceAttributes(attempt, events))

		compileSpan := trace.start("参与结果编译", "compile", map[string]string{"attempt": fmt.Sprint(attempt)})
		if len(model.FunctionCallsFromEvents(events)) != 0 {
			trace.finish(compileSpan, "failed", map[string]string{
				"attempt": fmt.Sprint(attempt), "errorCode": participationTraceInvalidDecision,
			})
			return ParticipationResult{}, errors.New("participation decision returned tool calls")
		}
		usage = append(usage, model.LaneUsageFromEvents(model.PromptLaneParticipate, events, 0)...)
		result, compileErr := CompileParticipation(model.CollectTextFromEvents(events), request.Messages)
		if compileErr == nil {
			if result.Action == ParticipationReply && request.EvaluationReason == ParticipationReasonMessage && (result.TargetMessageID == nil || !isNewTarget(request.Messages, *result.TargetMessageID)) {
				result = ParticipationResult{Action: ParticipationSilent, Usage: usage}
			} else {
				result.Usage = usage
			}
			trace.finish(compileSpan, "completed", map[string]string{
				"attempt": fmt.Sprint(attempt), "action": string(result.Action),
			})
			return result, nil
		}
		trace.finish(compileSpan, "failed", map[string]string{
			"attempt": fmt.Sprint(attempt), "errorCode": participationTraceInvalidDecision,
		})
		if attempt == 1 {
			firstCompileErr = compileErr
		}
		if attempt > protocolCompileRetries {
			return ParticipationResult{}, fmt.Errorf("participation decision remained invalid after %d retries: first attempt: %v; final attempt: %w", protocolCompileRetries, firstCompileErr, compileErr)
		}
	}
	return ParticipationResult{}, errors.New("participation decision retry loop exhausted")
}

func (e *Engine) prepareParticipation(ctx context.Context, request ParticipationRequest) (model.CompiledPromptRequest, error) {
	resolved, err := e.host.ResolveInteraction(request.ConversationID)
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, err
	}
	if resolved.Memory != session.MemoryPublic || !resolved.AllowsAmbientParticipation() {
		return model.CompiledPromptRequest{CacheInput: nil}, errors.New("participation requires a public ambient interaction")
	}
	now := time.Now().UnixMilli()
	activity, err := e.host.LoadConversationActivity(request.ConversationID, now)
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, fmt.Errorf("loading ambient conversation activity: %w", err)
	}
	record, err := e.host.ActiveCharacter(activity.Conversation.CharacterID)
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, err
	}
	presence, err := DeriveRecentPresenceFromActivity(activity, now)
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, err
	}
	input, err := BuildParticipationInputWithActivity(record, resolved, request.EvaluationReason, request.Messages, request.CacheMessages, presence, now, activity)
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, err
	}
	behavior, err := e.behaviorContext(ctx, activity.Conversation.CharacterID, request.ConversationID, request.Messages)
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, err
	}
	if behavior != nil {
		input = append(input, *behavior)
	}
	notes, err := e.host.ListSocialPersonNotes(ctx, activity.Conversation.CharacterID, request.ConversationID, SenderIDs(request.Messages))
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, fmt.Errorf("listing social person notes: %w", err)
	}
	if len(notes) > 0 {
		item, notesErr := character.EncodeSocialPersonNotes(notes)
		if notesErr != nil {
			return model.CompiledPromptRequest{CacheInput: nil}, notesErr
		}
		input = append(input, item)
	}
	connection, err := e.host.ModelConnection()
	if err != nil {
		return model.CompiledPromptRequest{CacheInput: nil}, err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneParticipate)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneParticipate, connection.Model, request.ConversationID, ParticipationInstructions)
	cacheInput.CharacterRevision = record.Revision
	return model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneParticipate, Model: connection.Model,
			Instructions: ParticipationInstructions, MaxOutputTokens: ParticipationMaxOutputTokens,
			PromptCacheKey: cacheKey,
		},
		Input:      input,
		CacheInput: &cacheInput,
	}, nil
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

func BehaviorItem(retrieved social.SocialMemoryContext) (*model.PromptItem, error) {
	behaviors := make([]social.SocialMemoryEntry, 0, 3)
	for _, entry := range retrieved.Entries {
		if entry.Kind != social.SocialMemoryBehavior {
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
	item, err := character.EncodeSocialMemoryContext(social.SocialMemoryContext{Entries: behaviors})
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
