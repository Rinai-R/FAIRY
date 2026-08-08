package initiative

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/session"
)

type TriggerKind string

const (
	TriggerPublicAmbient      TriggerKind = "public_ambient"
	TriggerDesktopObservation TriggerKind = "desktop_observation"
	TriggerScheduler          TriggerKind = "scheduler"
)

type PipelineKind string

const (
	PipelineSilent        PipelineKind = "silent"
	PipelineParticipation PipelineKind = "participation"
	PipelineObservation   PipelineKind = "observation"
)

// TriggerPayload is sealed so endpoint adapters cannot invent a new payload
// shape without changing the Core-owned interaction package.
type TriggerPayload interface{ triggerPayload() }

type PublicAmbientTrigger struct{ MessageID string }

func (PublicAmbientTrigger) triggerPayload() {}

type DesktopObservationTriggerPayload struct{ Observation DesktopObservation }

func (DesktopObservationTriggerPayload) triggerPayload() {}

type SchedulerTrigger struct{ ScheduleID string }

func (SchedulerTrigger) triggerPayload() {}

type TriggerEnvelope struct {
	Kind           TriggerKind
	ConversationID string
	Resolved       session.Resolved
	Payload        TriggerPayload
	EvidenceIDs    []string
	CreatedAt      time.Time
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
	case TriggerPublicAmbient:
		payload, ok := e.Payload.(PublicAmbientTrigger)
		if !ok || strings.TrimSpace(payload.MessageID) == "" || e.Resolved.AllowsPersonalMemory() {
			return errors.New("public ambient trigger requires a public interaction")
		}
	case TriggerDesktopObservation:
		payload, ok := e.Payload.(DesktopObservationTriggerPayload)
		if !ok || e.Resolved.Endpoint != session.EndpointDesktop || !e.Resolved.AllowsPersonalMemory() {
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

// RouteTrigger validates the trigger and directly selects Initiative's domain
// entry. It does not build or traverse an intermediate rule structure.
func RouteTrigger(envelope TriggerEnvelope, privacy DesktopPrivacyState, evidenceValid bool, now time.Time) (TriggerRoute, error) {
	if err := envelope.Validate(now); err != nil {
		return TriggerRoute{}, err
	}
	if !evidenceValid {
		return TriggerRoute{}, errors.New("trigger evidence is invalid")
	}
	switch envelope.Kind {
	case TriggerPublicAmbient:
		return TriggerRoute{Pipeline: PipelineParticipation, Branch: "public_ambient"}, nil
	case TriggerDesktopObservation, TriggerScheduler:
		if privacy != DesktopPrivacyNormal {
			return TriggerRoute{Pipeline: PipelineSilent, Branch: "privacy_silent"}, nil
		}
		return TriggerRoute{Pipeline: PipelineObservation, Branch: "observation"}, nil
	default:
		return TriggerRoute{}, fmt.Errorf("trigger kind %q has no initiative route", envelope.Kind)
	}
}
