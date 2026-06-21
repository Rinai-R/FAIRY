package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Rinai-R/FAIRY/internal/app"
)

type Provider string

const (
	ProviderMock  Provider = "mock"
	ProviderCodex Provider = "codex"
	ProviderFairy Provider = "fairy-agent"
)

type ActDecision string

const (
	ActDecisionContinue       ActDecision = "continue"
	ActDecisionSummarize      ActDecision = "summarize"
	ActDecisionFreeDiscussion ActDecision = "free_discussion"
)

type Engine interface {
	GenerateAct(ctx context.Context, input ActInput) (ActOutput, error)
	Discuss(ctx context.Context, input DiscussInput) (Output, error)
}

type ActTraceHook func(ActTraceEvent)

type ActTraceEvent struct {
	Type       string
	Level      string
	Step       string
	Message    string
	Detail     string
	RetryCount int
	DurationMS int64
}

const (
	ActTraceStepActPlan          = "actplan"
	ActTraceStepGenerateActDraft = "generate_act_draft"
	ActTraceStepRewriteAct       = "rewrite_act"
)

type contractError struct {
	err error
}

func NewContractError(err error) error {
	if err == nil {
		return nil
	}
	return contractError{err: err}
}

func IsContractError(err error) bool {
	var target contractError
	return errors.As(err, &target)
}

func (e contractError) Error() string {
	return e.err.Error()
}

func (e contractError) Unwrap() error {
	return e.err
}

type ActInput struct {
	Request       app.SceneGenerateRequest `json:"request"`
	Session       app.Session              `json:"session,omitempty"`
	Workflow      app.TeachingWorkflow     `json:"workflow,omitempty"`
	PlannedNode   app.TeachingWorkflowNode `json:"planned_node,omitempty"`
	PreviousNode  app.TeachingWorkflowNode `json:"previous_node,omitempty"`
	Choice        app.SceneChoice          `json:"choice,omitempty"`
	Material      app.MaterialContext      `json:"material,omitempty"`
	Expressions   []app.ExpressionOption   `json:"expressions,omitempty"`
	CoveredPoints []string                 `json:"covered_points,omitempty"`
	ActIndex      int                      `json:"act_index"`
	Correction    string                   `json:"correction,omitempty"`
	Trace         ActTraceHook             `json:"-"`
}

func (input ActInput) Validate() error {
	if len(input.Request.Characters) == 0 {
		return errors.New("characters 不能为空")
	}
	if strings.TrimSpace(input.Request.Characters[0].ID) == "" {
		return errors.New("characters[0].id 不能为空")
	}
	if input.ActIndex < 1 {
		return fmt.Errorf("act_index 必须大于 0: %d", input.ActIndex)
	}
	return nil
}

type ActOutput struct {
	Node          app.TeachingWorkflowNode `json:"node"`
	Decision      ActDecision              `json:"decision"`
	CoveredPoints []string                 `json:"covered_points,omitempty"`
	Summary       string                   `json:"summary,omitempty"`
}

func (output ActOutput) Validate() error {
	if strings.TrimSpace(output.Node.ID) == "" {
		return errors.New("node.id 不能为空")
	}
	if strings.TrimSpace(output.Node.Kind) == "" {
		return errors.New("node.kind 不能为空")
	}
	if strings.TrimSpace(output.Node.Title) == "" {
		return errors.New("node.title 不能为空")
	}
	switch output.Decision {
	case ActDecisionContinue, ActDecisionSummarize, ActDecisionFreeDiscussion:
		return nil
	default:
		return fmt.Errorf("decision 不支持: %s", output.Decision)
	}
}

type DiscussInput struct {
	Turn            app.TurnRequest          `json:"turn"`
	Workflow        app.TeachingWorkflow     `json:"workflow,omitempty"`
	CurrentNode     app.TeachingWorkflowNode `json:"current_node,omitempty"`
	MaterialSummary string                   `json:"material_summary,omitempty"`
	SessionSummary  string                   `json:"session_summary,omitempty"`
}

func (input DiscussInput) Validate() error {
	if strings.TrimSpace(input.Turn.User.UserID) == "" {
		return errors.New("user.user_id 不能为空")
	}
	if strings.TrimSpace(input.Turn.User.Text) == "" {
		return errors.New("user.text 不能为空")
	}
	if len(input.Turn.Characters) == 0 && strings.TrimSpace(input.Turn.Character.ID) == "" {
		return errors.New("character 不能为空")
	}
	return nil
}

type Output struct {
	DisplayText  string            `json:"display_text"`
	SpeechText   string            `json:"speech_text"`
	Segments     []app.Segment     `json:"segments"`
	Emotion      string            `json:"emotion"`
	Expression   string            `json:"expression"`
	Motion       string            `json:"motion"`
	Voice        app.VoicePlan     `json:"voice"`
	MemoryWrites []app.MemoryWrite `json:"memory_writes"`
}
