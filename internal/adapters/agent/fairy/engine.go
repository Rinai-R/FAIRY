package fairy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	messages, err := buildGenerateActMessages(input, plan)
	if err != nil {
		return agent.ActOutput{}, err
	}
	draft, err := e.generateActFromMessages(ctx, input, messages)
	if err != nil {
		return agent.ActOutput{}, err
	}
	rewriteMessages, err := buildRewriteActMessages(input, plan, draft)
	if err != nil {
		return agent.ActOutput{}, err
	}
	return e.generateActFromMessages(ctx, input, rewriteMessages)
}

func (e *Engine) planActs(ctx context.Context, input agent.ActInput) (actPlan, error) {
	cacheKey, err := actPlanCacheKey(input)
	if err != nil {
		return actPlan{}, err
	}
	if plan, ok := e.cachedActPlan(cacheKey); ok {
		return plan, nil
	}
	messages, err := buildActPlanMessages(input)
	if err != nil {
		return actPlan{}, err
	}
	var lastErr error
	attemptMessages := messages
	for attempt := 1; attempt <= maxJSONRepairAttempts; attempt++ {
		content, err := e.completeJSON(ctx, input.Request.Runtime.Agent, attemptMessages)
		if err != nil {
			return actPlan{}, err
		}
		out, err := parseActPlan(content)
		if err == nil {
			err = validateActPlan(out)
		}
		if err == nil {
			e.storeActPlan(cacheKey, out)
			return out, nil
		}
		lastErr = err
		if attempt < maxJSONRepairAttempts {
			attemptMessages = buildRepairMessages(messages, content, err)
		}
	}
	return actPlan{}, fmt.Errorf("FAIRY ActPlan 输出连续不符合合约: %w", lastErr)
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

func (e *Engine) generateActFromMessages(ctx context.Context, input agent.ActInput, messages []llm.Message) (agent.ActOutput, error) {
	var lastErr error
	attemptMessages := messages
	for attempt := 1; attempt <= maxJSONRepairAttempts; attempt++ {
		content, err := e.completeJSON(ctx, input.Request.Runtime.Agent, attemptMessages)
		if err != nil {
			return agent.ActOutput{}, err
		}
		out, err := parseActOutput(content)
		if err == nil {
			err = validateFairyActOutput(input, out)
		}
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt < maxJSONRepairAttempts {
			attemptMessages = buildRepairMessages(messages, content, err)
		}
	}
	return agent.ActOutput{}, fmt.Errorf("FAIRY GenerateAct 输出连续不符合合约: %w", lastErr)
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
