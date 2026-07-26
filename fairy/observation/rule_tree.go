package observation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/interaction"
)

type TriggerKind string

const (
	TriggerDirect             TriggerKind = "direct"
	TriggerPublicAmbient      TriggerKind = "public_ambient"
	TriggerDesktopObservation TriggerKind = "desktop_observation"
	TriggerScheduler          TriggerKind = "scheduler"
)

type PipelineKind string

const (
	PipelineReject        PipelineKind = "reject"
	PipelineSilent        PipelineKind = "silent"
	PipelineDirectTurn    PipelineKind = "direct_turn"
	PipelineParticipation PipelineKind = "participation"
	PipelineObservation   PipelineKind = "observation"
	PipelineReact         PipelineKind = "react"
	PipelineInitiate      PipelineKind = "initiate"
)

// TriggerPayload is sealed so endpoint adapters cannot invent a new payload
// shape without changing the Core-owned interaction package.
type TriggerPayload interface{ triggerPayload() }

type DirectTrigger struct{ Input string }

func (DirectTrigger) triggerPayload() {}

type PublicAmbientTrigger struct{ MessageID string }

func (PublicAmbientTrigger) triggerPayload() {}

type DesktopObservationTriggerPayload struct{ Observation DesktopObservation }

func (DesktopObservationTriggerPayload) triggerPayload() {}

type SchedulerTrigger struct{ ScheduleID string }

func (SchedulerTrigger) triggerPayload() {}

type TriggerEnvelope struct {
	Kind           TriggerKind
	ConversationID string
	Resolved       interaction.Resolved
	Payload        TriggerPayload
	EvidenceIDs    []string
	CreatedAt      time.Time
	SpeechEnabled  bool
}

func (e TriggerEnvelope) Validate(now time.Time) error {
	if strings.TrimSpace(e.ConversationID) == "" {
		return errors.New("trigger conversation id is required")
	}
	if e.CreatedAt.IsZero() || e.CreatedAt.After(now.Add(time.Second)) {
		return errors.New("trigger creation time is invalid")
	}
	if e.Payload == nil {
		return errors.New("trigger payload is required")
	}
	if err := e.Resolved.Validate(); err != nil {
		return fmt.Errorf("trigger interaction is invalid: %w", err)
	}
	switch e.Kind {
	case TriggerDirect:
		payload, ok := e.Payload.(DirectTrigger)
		if !ok || strings.TrimSpace(payload.Input) == "" || !e.Resolved.AllowsPersonalMemory() {
			return errors.New("direct trigger requires a private interaction")
		}
	case TriggerPublicAmbient:
		payload, ok := e.Payload.(PublicAmbientTrigger)
		if !ok || strings.TrimSpace(payload.MessageID) == "" || e.Resolved.AllowsPersonalMemory() {
			return errors.New("public ambient trigger requires a public interaction")
		}
	case TriggerDesktopObservation:
		payload, ok := e.Payload.(DesktopObservationTriggerPayload)
		if !ok || e.Resolved.Endpoint != interaction.EndpointDesktop || !e.Resolved.AllowsPersonalMemory() {
			return errors.New("desktop trigger requires a private desktop interaction")
		}
		if err := payload.Observation.Validate(now); err != nil {
			return err
		}
	case TriggerScheduler:
		payload, ok := e.Payload.(SchedulerTrigger)
		if !ok || strings.TrimSpace(payload.ScheduleID) == "" || !e.Resolved.AllowsPersonalMemory() {
			return errors.New("scheduler trigger requires a private interaction")
		}
	default:
		return fmt.Errorf("unknown trigger kind %q", e.Kind)
	}
	return nil
}

type TriggerRoute struct {
	Pipeline PipelineKind
	Branch   string
}

// RouteCoreTrigger is the single validation and entry-selection boundary used
// by Core. It selects a registered pipeline but never executes business nodes.
func RouteCoreTrigger(envelope TriggerEnvelope, privacy DesktopPrivacyState, evidenceValid bool, now time.Time) (TriggerRoute, error) {
	if err := envelope.Validate(now); err != nil {
		return TriggerRoute{}, err
	}
	tree, err := NewCoreRuleTree()
	if err != nil {
		return TriggerRoute{}, err
	}
	pipeline, branch, err := tree.Evaluate(RuleContext{
		Trigger: envelope, Privacy: privacy, TriggerValid: true, EvidenceValid: evidenceValid,
	})
	if err != nil {
		return TriggerRoute{}, err
	}
	if pipeline == PipelineReject {
		return TriggerRoute{}, fmt.Errorf("trigger rejected by rule branch %q", branch)
	}
	return TriggerRoute{Pipeline: pipeline, Branch: branch}, nil
}

type RuleContext struct {
	Trigger          TriggerEnvelope
	Privacy          DesktopPrivacyState
	AttentionBudget  int
	AllowsInitiation bool
	TriggerValid     bool
	EvidenceValid    bool
}

type RuleBranch struct {
	Name string
	When func(RuleContext) bool
	Then PipelineKind
}

type RuleTree struct {
	branches []RuleBranch
	compiled bool
}

func NewRuleTree(branches ...RuleBranch) (RuleTree, error) {
	if len(branches) == 0 {
		return RuleTree{}, errors.New("rule tree requires branches")
	}
	seen := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		if strings.TrimSpace(branch.Name) == "" || branch.When == nil || branch.Then == "" {
			return RuleTree{}, errors.New("rule branch requires name, condition and action")
		}
		if _, ok := seen[branch.Name]; ok {
			return RuleTree{}, fmt.Errorf("duplicate rule branch %q", branch.Name)
		}
		seen[branch.Name] = struct{}{}
	}
	return RuleTree{branches: append([]RuleBranch(nil), branches...), compiled: true}, nil
}

func (t RuleTree) Evaluate(ctx RuleContext) (PipelineKind, string, error) {
	if !t.compiled {
		return "", "", errors.New("rule tree is not compiled")
	}
	for _, branch := range t.branches {
		if branch.When(ctx) {
			return branch.Then, branch.Name, nil
		}
	}
	return "", "", errors.New("rule tree selected no pipeline")
}

func NewCoreRuleTree() (RuleTree, error) {
	return NewRuleTree(
		RuleBranch{Name: "reject_invalid", When: func(ctx RuleContext) bool { return !ctx.TriggerValid || !ctx.EvidenceValid }, Then: PipelineReject},
		RuleBranch{Name: "privacy_silent", When: func(ctx RuleContext) bool {
			return (ctx.Trigger.Kind == TriggerDesktopObservation || ctx.Trigger.Kind == TriggerScheduler) && ctx.Privacy != DesktopPrivacyNormal
		}, Then: PipelineSilent},
		RuleBranch{Name: "direct", When: func(ctx RuleContext) bool { return ctx.Trigger.Kind == TriggerDirect }, Then: PipelineDirectTurn},
		RuleBranch{Name: "public_ambient", When: func(ctx RuleContext) bool { return ctx.Trigger.Kind == TriggerPublicAmbient }, Then: PipelineParticipation},
		RuleBranch{Name: "observation", When: func(ctx RuleContext) bool {
			return ctx.Trigger.Kind == TriggerDesktopObservation || ctx.Trigger.Kind == TriggerScheduler
		}, Then: PipelineObservation},
		RuleBranch{Name: "reject_unknown", When: func(RuleContext) bool { return true }, Then: PipelineReject},
	)
}
