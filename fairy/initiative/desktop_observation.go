package initiative

import (
	"errors"
	"time"

	"fairy/session"
)

type (
	DesktopObservation        = session.DesktopObservation
	DesktopObservationTrigger = session.DesktopObservationTrigger
	DesktopPrivacyState       = session.DesktopPrivacyState
	DesktopLifecycleEvent     = session.DesktopLifecycleEvent
	DesktopActivityCategory   = session.DesktopActivityCategory
)

const (
	DesktopActivityUnknown   = session.DesktopActivityUnknown
	DesktopActivityWorking   = session.DesktopActivityWorking
	DesktopActivityReading   = session.DesktopActivityReading
	DesktopActivityCommuning = session.DesktopActivityCommuning
	DesktopActivityIdle      = session.DesktopActivityIdle

	DesktopLifecycleNone       = session.DesktopLifecycleNone
	DesktopLifecycleReturned   = session.DesktopLifecycleReturned
	DesktopLifecycleLeft       = session.DesktopLifecycleLeft
	DesktopLifecyclePrivacyOn  = session.DesktopLifecyclePrivacyOn
	DesktopLifecyclePrivacyOff = session.DesktopLifecyclePrivacyOff

	DesktopPrivacyNormal       = session.DesktopPrivacyNormal
	DesktopPrivacyLocked       = session.DesktopPrivacyLocked
	DesktopPrivacyMeeting      = session.DesktopPrivacyMeeting
	DesktopPrivacyDoNotDisturb = session.DesktopPrivacyDoNotDisturb
	DesktopPrivacyProtected    = session.DesktopPrivacyProtected

	DesktopTriggerPeriodic  = session.DesktopTriggerPeriodic
	DesktopTriggerLifecycle = session.DesktopTriggerLifecycle
)

type DesktopObservationAction string

const (
	DesktopActionSilent   DesktopObservationAction = "silent"
	DesktopActionReact    DesktopObservationAction = "react"
	DesktopActionInitiate DesktopObservationAction = "initiate"
)

type DesktopRulebook struct {
	Resolved             session.Resolved
	Trigger              DesktopObservationTrigger
	Privacy              DesktopPrivacyState
	AllowsKnowledge      bool
	AllowsPersonalMemory bool
	AllowsSocialMemory   bool
	AllowsPlanner        bool
	AllowsInitiation     bool
	AttentionBudget      int
	MinSpacing           time.Duration
	Now                  time.Time
}

// DesktopObservationStep is a compatibility projection for the existing
// Session response's "nodes" field. It is diagnostic data only and is never
// compiled or executed.
type DesktopObservationStep struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Depends  []string `json:"dependsOn,omitempty"`
	OmitCode string   `json:"omitCode,omitempty"`
}

type DesktopObservationResult struct {
	Nodes       []DesktopObservationStep       `json:"nodes"`
	Action      DesktopObservationAction       `json:"action"`
	OmitReasons []string                       `json:"omitReasons,omitempty"`
	Diagnostics []DesktopObservationDiagnostic `json:"diagnostics,omitempty"`
}

type DesktopObservationDiagnostic struct {
	Node   string `json:"node"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// DecideDesktopObservation validates the private desktop capability and
// returns the desired domain action. The returned node list preserves the
// existing Session JSON contract; it has no control-flow semantics.
func DecideDesktopObservation(rulebook DesktopRulebook, observation DesktopObservation) (DesktopObservationResult, error) {
	if rulebook.Resolved.Endpoint != session.EndpointDesktop || rulebook.Resolved.Facts.Audience != session.AudienceSingle || !rulebook.Resolved.AllowsPersonalMemory() {
		return DesktopObservationResult{}, errors.New("desktop observation requires private desktop interaction")
	}
	if rulebook.Now.IsZero() {
		rulebook.Now = time.Now()
	}
	if err := observation.Validate(rulebook.Now); err != nil {
		return DesktopObservationResult{}, err
	}

	result := DesktopObservationResult{Nodes: []DesktopObservationStep{
		{ID: "normalize", Kind: "normalize_observation"},
		{ID: "attention", Kind: "evaluate_attention", Depends: []string{"normalize"}},
	}}
	if rulebook.AttentionBudget <= 0 || observation.Privacy != DesktopPrivacyNormal {
		result.Action = DesktopActionSilent
		result.OmitReasons = append(result.OmitReasons, "attention_budget_or_privacy")
		return result, nil
	}
	if rulebook.AllowsKnowledge {
		result.Nodes = append(result.Nodes, DesktopObservationStep{ID: "knowledge", Kind: "retrieve_knowledge", Depends: []string{"attention"}})
	}
	if rulebook.AllowsPersonalMemory {
		result.Nodes = append(result.Nodes, DesktopObservationStep{ID: "memory", Kind: "retrieve_memory", Depends: []string{"attention"}})
	}
	if rulebook.AllowsPlanner && rulebook.AllowsInitiation && observation.Lifecycle == DesktopLifecycleReturned {
		result.Nodes = append(result.Nodes, DesktopObservationStep{ID: "initiate", Kind: "initiate", Depends: []string{"attention"}})
		result.Action = DesktopActionInitiate
		result.OmitReasons = append(result.OmitReasons, "retrieval_planner_respond_owned_by_turn_engine")
		return result, nil
	}
	result.Action = DesktopActionReact
	result.OmitReasons = append(result.OmitReasons, "planner_or_initiation_not_allowed")
	return result, nil
}

func desktopObservationDiagnostics(action DesktopObservationAction) []DesktopObservationDiagnostic {
	actionKind := string(action)
	return []DesktopObservationDiagnostic{
		{Node: "normalize", Kind: "normalize_observation", Status: "started"},
		{Node: "normalize", Kind: "normalize_observation", Status: "completed"},
		{Node: "attention", Kind: "evaluate_attention", Status: "started"},
		{Node: "attention", Kind: "evaluate_attention", Status: "completed"},
		{Node: actionKind, Kind: actionKind, Status: "started"},
		{Node: actionKind, Kind: actionKind, Status: "completed"},
		{Node: "final", Kind: "final", Status: "started"},
		{Node: "final", Kind: "final", Status: "completed"},
	}
}
