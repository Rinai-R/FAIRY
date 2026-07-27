package initiative

import (
	"errors"
	"time"

	"fairy/session"
)

const MaxDesktopGraphNodes = 12

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

type DesktopGraphNodeKind string

const (
	DesktopNodeNormalize DesktopGraphNodeKind = "normalize_observation"
	DesktopNodeEvaluate  DesktopGraphNodeKind = "evaluate_attention"
	DesktopNodeKnowledge DesktopGraphNodeKind = "retrieve_knowledge"
	DesktopNodeMemory    DesktopGraphNodeKind = "retrieve_memory"
	DesktopNodePlanner   DesktopGraphNodeKind = "planner"
	DesktopNodeRespond   DesktopGraphNodeKind = "respond"
	DesktopNodePersist   DesktopGraphNodeKind = "persist"
	DesktopNodeSilent    DesktopGraphNodeKind = "silent"
	DesktopNodeReact     DesktopGraphNodeKind = "react"
	DesktopNodeFinal     DesktopGraphNodeKind = "final"
	DesktopNodeInitiate  DesktopGraphNodeKind = "initiate"
)

type DesktopGraphNode struct {
	ID       string               `json:"id"`
	Kind     DesktopGraphNodeKind `json:"kind"`
	Depends  []string             `json:"dependsOn,omitempty"`
	OmitCode string               `json:"omitCode,omitempty"`
}

type DesktopGraphPlan struct {
	Nodes       []DesktopGraphNode       `json:"nodes"`
	Action      DesktopObservationAction `json:"action"`
	OmitReasons []string                 `json:"omitReasons,omitempty"`
	Diagnostics []DesktopGraphDiagnostic `json:"diagnostics,omitempty"`
}

type DesktopGraphDiagnostic struct {
	Node   string `json:"node"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

func CompileDesktopGraph(rulebook DesktopRulebook, sample DesktopObservation) (DesktopGraphPlan, error) {
	if rulebook.Resolved.Endpoint != session.EndpointDesktop || rulebook.Resolved.Facts.Audience != session.AudienceSingle || !rulebook.Resolved.AllowsPersonalMemory() {
		return DesktopGraphPlan{}, errors.New("desktop observation graph requires private desktop interaction")
	}
	if rulebook.Now.IsZero() {
		rulebook.Now = time.Now()
	}
	if err := sample.Validate(rulebook.Now); err != nil {
		return DesktopGraphPlan{}, err
	}
	if _, err := CompileDesktopTypedGraph(rulebook, sample); err != nil {
		return DesktopGraphPlan{}, err
	}
	plan := DesktopGraphPlan{Nodes: []DesktopGraphNode{{ID: "normalize", Kind: DesktopNodeNormalize}, {ID: "attention", Kind: DesktopNodeEvaluate, Depends: []string{"normalize"}}}}
	if rulebook.AttentionBudget <= 0 || sample.Privacy != DesktopPrivacyNormal {
		plan.Action = DesktopActionSilent
		plan.OmitReasons = append(plan.OmitReasons, "attention_budget_or_privacy")
		return plan, nil
	}
	if rulebook.AllowsKnowledge {
		plan.Nodes = append(plan.Nodes, DesktopGraphNode{ID: "knowledge", Kind: DesktopNodeKnowledge, Depends: []string{"attention"}})
	}
	if rulebook.AllowsPersonalMemory {
		plan.Nodes = append(plan.Nodes, DesktopGraphNode{ID: "memory", Kind: DesktopNodeMemory, Depends: []string{"attention"}})
	}
	if rulebook.AllowsPlanner && rulebook.AllowsInitiation && sample.Lifecycle == DesktopLifecycleReturned {
		plan.Nodes = append(plan.Nodes, DesktopGraphNode{ID: "initiate", Kind: DesktopNodeInitiate, Depends: []string{"attention"}})
		plan.Action = DesktopActionInitiate
		plan.OmitReasons = append(plan.OmitReasons, "retrieval_planner_respond_owned_by_turn_engine")
		return plan, nil
	}
	plan.Action = DesktopActionReact
	plan.OmitReasons = append(plan.OmitReasons, "planner_or_initiation_not_allowed")
	return plan, nil
}
