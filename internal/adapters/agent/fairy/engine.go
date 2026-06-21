package fairy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/llm"
)

type Engine struct {
	model     llm.Adapter
	planMu    sync.Mutex
	planCache map[string]actPlan
}

type Options struct {
	Model llm.Adapter
}

func NewEngine(options Options) *Engine {
	return &Engine{
		model:     options.Model,
		planCache: map[string]actPlan{},
	}
}

func (e *Engine) GenerateAct(ctx context.Context, input agent.ActInput) (agent.ActOutput, error) {
	if err := input.Validate(); err != nil {
		return agent.ActOutput{}, err
	}
	plan, err := e.planActs(ctx, input)
	if err != nil {
		return agent.ActOutput{}, err
	}
	currentPlan := selectCurrentActPlan(input, plan)
	messages, err := buildGenerateActMessages(input, plan)
	if err != nil {
		return agent.ActOutput{}, err
	}
	draft, err := e.generateActFromMessages(ctx, input, agent.ActTraceStepGenerateActDraft, messages)
	if err != nil {
		return agent.ActOutput{}, err
	}
	draft = attachActPlanToOutput(draft, currentPlan)
	rewriteMessages, err := buildRewriteActMessages(input, plan, draft)
	if err != nil {
		return agent.ActOutput{}, err
	}
	out, err := e.generateActFromMessages(ctx, input, agent.ActTraceStepRewriteAct, rewriteMessages, func(out agent.ActOutput) error {
		return validateRewriteActPreservesDraft(draft, out)
	})
	if err != nil {
		return agent.ActOutput{}, err
	}
	return attachActPlanToOutput(out, currentPlan), nil
}

func (e *Engine) planActs(ctx context.Context, input agent.ActInput) (actPlan, error) {
	started := time.Now()
	cacheKey, err := actPlanCacheKey(input)
	if err != nil {
		emitActTrace(input, agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentActPlanFailed,
			Level:      app.RuntimeEventLevelError,
			Step:       agent.ActTraceStepActPlan,
			Message:    "ActPlan 准备失败。",
			Detail:     err.Error(),
			DurationMS: fairyTraceDurationMS(started),
		})
		return actPlan{}, err
	}
	if plan, ok := e.cachedActPlan(cacheKey); ok {
		emitActTrace(input, agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentActPlanCacheHit,
			Level:      app.RuntimeEventLevelInfo,
			Step:       agent.ActTraceStepActPlan,
			Message:    "ActPlan 缓存命中。",
			DurationMS: fairyTraceDurationMS(started),
		})
		return plan, nil
	}
	messages, err := buildActPlanMessages(input)
	if err != nil {
		emitActTrace(input, agent.ActTraceEvent{
			Type:       app.RuntimeEventTypeAgentActPlanFailed,
			Level:      app.RuntimeEventLevelError,
			Step:       agent.ActTraceStepActPlan,
			Message:    "ActPlan prompt 构造失败。",
			Detail:     err.Error(),
			DurationMS: fairyTraceDurationMS(started),
		})
		return actPlan{}, err
	}
	var lastErr error
	attemptMessages := messages
	for attempt := 1; attempt <= maxJSONRepairAttempts; attempt++ {
		content, err := e.completeJSON(ctx, input.Request.Runtime.Agent, attemptMessages)
		if err != nil {
			if llm.IsEmptyContentError(err) {
				lastErr = err
				if attempt < maxJSONRepairAttempts {
					emitActTrace(input, agent.ActTraceEvent{
						Type:       app.RuntimeEventTypeAgentActPlanRetry,
						Level:      app.RuntimeEventLevelWarn,
						Step:       agent.ActTraceStepActPlan,
						Message:    "ActPlan 输出缺少 JSON 正文，正在修正重试。",
						Detail:     err.Error(),
						RetryCount: attempt,
						DurationMS: fairyTraceDurationMS(started),
					})
					attemptMessages = buildRepairMessages(messages, "", err)
					continue
				}
				break
			}
			emitActTrace(input, agent.ActTraceEvent{
				Type:       app.RuntimeEventTypeAgentActPlanFailed,
				Level:      app.RuntimeEventLevelError,
				Step:       agent.ActTraceStepActPlan,
				Message:    "ActPlan 调用失败。",
				Detail:     err.Error(),
				DurationMS: fairyTraceDurationMS(started),
			})
			return actPlan{}, err
		}
		out, err := parseActPlan(content)
		if err == nil {
			err = validateActPlan(out)
		}
		if err == nil {
			e.storeActPlan(cacheKey, out)
			emitActTrace(input, agent.ActTraceEvent{
				Type:       app.RuntimeEventTypeAgentActPlanDone,
				Level:      app.RuntimeEventLevelInfo,
				Step:       agent.ActTraceStepActPlan,
				Message:    "ActPlan 已生成。",
				DurationMS: fairyTraceDurationMS(started),
			})
			return out, nil
		}
		lastErr = err
		if attempt < maxJSONRepairAttempts {
			emitActTrace(input, agent.ActTraceEvent{
				Type:       app.RuntimeEventTypeAgentActPlanRetry,
				Level:      app.RuntimeEventLevelWarn,
				Step:       agent.ActTraceStepActPlan,
				Message:    "ActPlan 输出不符合合约，正在修正重试。",
				Detail:     err.Error(),
				RetryCount: attempt,
				DurationMS: fairyTraceDurationMS(started),
			})
			attemptMessages = buildRepairMessages(messages, content, err)
		}
	}
	finalErr := fmt.Errorf("FAIRY ActPlan 输出连续不符合合约: %w", lastErr)
	emitActTrace(input, agent.ActTraceEvent{
		Type:       app.RuntimeEventTypeAgentActPlanFailed,
		Level:      app.RuntimeEventLevelError,
		Step:       agent.ActTraceStepActPlan,
		Message:    "ActPlan 输出连续不符合合约。",
		Detail:     finalErr.Error(),
		DurationMS: fairyTraceDurationMS(started),
	})
	return actPlan{}, finalErr
}

func (e *Engine) cachedActPlan(key string) (actPlan, bool) {
	e.planMu.Lock()
	defer e.planMu.Unlock()
	plan, ok := e.planCache[key]
	return plan, ok
}

func (e *Engine) storeActPlan(key string, plan actPlan) {
	e.planMu.Lock()
	defer e.planMu.Unlock()
	if e.planCache == nil {
		e.planCache = map[string]actPlan{}
	}
	e.planCache[key] = plan
}

func attachActPlanToOutput(output agent.ActOutput, plan actPlanItem) agent.ActOutput {
	if strings.TrimSpace(plan.TeachingGoal) != "" && strings.TrimSpace(output.Node.TeachingGoal) == "" {
		output.Node.TeachingGoal = strings.TrimSpace(plan.TeachingGoal)
	}
	if len(output.Node.MustCover) == 0 {
		output.Node.MustCover = compactStrings(plan.MustCover)
	}
	if strings.TrimSpace(plan.MisconceptionToAddress) != "" && strings.TrimSpace(output.Node.MisconceptionToAddress) == "" {
		output.Node.MisconceptionToAddress = strings.TrimSpace(plan.MisconceptionToAddress)
	}
	if strings.TrimSpace(plan.ExampleOrCounterexample) != "" && strings.TrimSpace(output.Node.ExampleOrCounterexample) == "" {
		output.Node.ExampleOrCounterexample = strings.TrimSpace(plan.ExampleOrCounterexample)
	}
	return output
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		out = append(out, trimmed)
		seen[trimmed] = true
	}
	return out
}

func validateRewriteActPreservesDraft(draft agent.ActOutput, rewritten agent.ActOutput) error {
	if draft.Decision != "" {
		if rewritten.Decision == "" {
			return fmt.Errorf("rewrite decision 不能为空，必须保留草稿 decision: %s", draft.Decision)
		}
		if rewritten.Decision != draft.Decision {
			return fmt.Errorf("rewrite decision 必须保留草稿值: %s != %s", rewritten.Decision, draft.Decision)
		}
	}
	return nil
}

func actPlanCacheKey(input agent.ActInput) (string, error) {
	characters := make([]app.Character, len(input.Request.Characters))
	copy(characters, input.Request.Characters)
	for index := range characters {
		characters[index].Runtime.Agent.APIKey = ""
		characters[index].Runtime.Voice.Extra = nil
	}
	body, err := json.Marshal(struct {
		SessionID    string                 `json:"session_id,omitempty"`
		Topic        string                 `json:"topic,omitempty"`
		Material     app.MaterialContext    `json:"material,omitempty"`
		LearningGoal string                 `json:"learning_goal,omitempty"`
		Prompt       app.PromptConfig       `json:"prompt,omitempty"`
		Characters   []app.Character        `json:"characters,omitempty"`
		Expressions  []app.ExpressionOption `json:"expressions,omitempty"`
		Language     app.LanguagePlan       `json:"language,omitempty"`
	}{
		SessionID:    input.Session.ID,
		Topic:        input.Request.Topic,
		Material:     materialContextForInput(input),
		LearningGoal: input.Request.LearningGoal,
		Prompt:       input.Request.Prompt,
		Characters:   characters,
		Expressions:  expressionOptionsForInput(input),
		Language:     input.Request.Runtime.Language,
	})
	if err != nil {
		return "", fmt.Errorf("生成 ActPlan 缓存 key 失败: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (e *Engine) generateActFromMessages(ctx context.Context, input agent.ActInput, step string, messages []llm.Message, extraValidate ...func(agent.ActOutput) error) (agent.ActOutput, error) {
	started := time.Now()
	var lastErr error
	attemptMessages := messages
	for attempt := 1; attempt <= maxJSONRepairAttempts; attempt++ {
		content, err := e.completeJSON(ctx, input.Request.Runtime.Agent, attemptMessages)
		if err != nil {
			if llm.IsEmptyContentError(err) {
				lastErr = err
				if attempt < maxJSONRepairAttempts {
					emitActTrace(input, agent.ActTraceEvent{
						Type:       fairyTraceRetryType(step),
						Level:      app.RuntimeEventLevelWarn,
						Step:       step,
						Message:    fairyTraceRetryMessage(step),
						Detail:     err.Error(),
						RetryCount: attempt,
						DurationMS: fairyTraceDurationMS(started),
					})
					attemptMessages = buildRepairMessages(messages, "", err)
					continue
				}
				break
			}
			emitActTrace(input, agent.ActTraceEvent{
				Type:       fairyTraceFailedType(step),
				Level:      app.RuntimeEventLevelError,
				Step:       step,
				Message:    fairyTraceFailedMessage(step),
				Detail:     err.Error(),
				DurationMS: fairyTraceDurationMS(started),
			})
			return agent.ActOutput{}, err
		}
		out, err := parseActOutput(content)
		if err == nil {
			err = validateFairyActOutput(input, out)
		}
		if err == nil {
			for _, validate := range extraValidate {
				if validate == nil {
					continue
				}
				if validateErr := validate(out); validateErr != nil {
					err = validateErr
					break
				}
			}
		}
		if err == nil {
			emitActTrace(input, agent.ActTraceEvent{
				Type:       fairyTraceCompletedType(step),
				Level:      app.RuntimeEventLevelInfo,
				Step:       step,
				Message:    fairyTraceCompletedMessage(step),
				DurationMS: fairyTraceDurationMS(started),
			})
			return out, nil
		}
		lastErr = err
		if attempt < maxJSONRepairAttempts {
			emitActTrace(input, agent.ActTraceEvent{
				Type:       fairyTraceRetryType(step),
				Level:      app.RuntimeEventLevelWarn,
				Step:       step,
				Message:    fairyTraceRetryMessage(step),
				Detail:     err.Error(),
				RetryCount: attempt,
				DurationMS: fairyTraceDurationMS(started),
			})
			attemptMessages = buildRepairMessages(messages, content, err)
		}
	}
	finalErr := agent.NewContractError(fmt.Errorf("FAIRY GenerateAct 输出连续不符合合约: %w", lastErr))
	emitActTrace(input, agent.ActTraceEvent{
		Type:       fairyTraceFailedType(step),
		Level:      app.RuntimeEventLevelError,
		Step:       step,
		Message:    fairyTraceFailedMessage(step),
		Detail:     finalErr.Error(),
		DurationMS: fairyTraceDurationMS(started),
	})
	return agent.ActOutput{}, finalErr
}

func emitActTrace(input agent.ActInput, event agent.ActTraceEvent) {
	if input.Trace == nil {
		return
	}
	if event.DurationMS < 0 {
		event.DurationMS = 0
	}
	input.Trace(event)
}

func fairyTraceDurationMS(started time.Time) int64 {
	duration := time.Since(started).Milliseconds()
	if duration < 1 {
		return 1
	}
	return duration
}

func fairyTraceCompletedType(step string) string {
	switch step {
	case agent.ActTraceStepRewriteAct:
		return app.RuntimeEventTypeAgentRewriteDone
	default:
		return app.RuntimeEventTypeAgentDraftDone
	}
}

func fairyTraceRetryType(step string) string {
	switch step {
	case agent.ActTraceStepRewriteAct:
		return app.RuntimeEventTypeAgentRewriteRetry
	default:
		return app.RuntimeEventTypeAgentDraftRetry
	}
}

func fairyTraceFailedType(step string) string {
	switch step {
	case agent.ActTraceStepRewriteAct:
		return app.RuntimeEventTypeAgentRewriteFailed
	default:
		return app.RuntimeEventTypeAgentDraftFailed
	}
}

func fairyTraceCompletedMessage(step string) string {
	switch step {
	case agent.ActTraceStepRewriteAct:
		return "RewriteAct 已完成角色口吻改写。"
	default:
		return "GenerateAct 草稿已生成。"
	}
}

func fairyTraceRetryMessage(step string) string {
	switch step {
	case agent.ActTraceStepRewriteAct:
		return "RewriteAct 输出不符合合约，正在修正重试。"
	default:
		return "GenerateAct 草稿不符合合约，正在修正重试。"
	}
}

func fairyTraceFailedMessage(step string) string {
	switch step {
	case agent.ActTraceStepRewriteAct:
		return "RewriteAct 失败。"
	default:
		return "GenerateAct 草稿失败。"
	}
}

func (e *Engine) Discuss(ctx context.Context, input agent.DiscussInput) (agent.Output, error) {
	if err := input.Validate(); err != nil {
		return agent.Output{}, err
	}
	messages, err := buildDiscussMessages(input)
	if err != nil {
		return agent.Output{}, err
	}
	var lastErr error
	attemptMessages := messages
	for attempt := 1; attempt <= maxJSONRepairAttempts; attempt++ {
		content, err := e.completeJSON(ctx, input.Turn.Runtime.Agent, attemptMessages)
		if err != nil {
			return agent.Output{}, err
		}
		out, err := parseDiscussOutput(content)
		if err == nil {
			err = validateFairyDiscussOutput(out)
		}
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt < maxJSONRepairAttempts {
			attemptMessages = buildRepairMessages(messages, content, err)
		}
	}
	return agent.Output{}, fmt.Errorf("FAIRY Discuss 输出连续不符合合约: %w", lastErr)
}

func (e *Engine) Check(_ context.Context) health.Result {
	start := time.Now()
	status := health.StatusOK
	message := "FAIRY agent 可用"
	if e.model == nil {
		status = health.StatusDown
		message = "FAIRY agent 缺少 llm adapter"
	} else if err := e.model.Validate(agentProfileToLLMProfile(app.AgentProfile{})); err != nil {
		status = health.StatusDown
		message = err.Error()
	}
	return health.Result{
		Domain:    "agent",
		Provider:  string(agent.ProviderFairy),
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		Message:   message,
		CheckedAt: time.Now().UTC(),
	}
}
