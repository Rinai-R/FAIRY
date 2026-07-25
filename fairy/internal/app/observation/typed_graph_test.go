package observation

import (
	"context"
	"testing"
	"time"
)

func TestCompileDesktopTypedGraphTreatsCapabilitiesAsConditionalNodes(t *testing.T) {
	now := time.Now()
	rulebook := privateDesktopRulebook(now)
	observation := DesktopObservation{
		ObservationID: "obs-typed", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerLifecycle,
		Lifecycle: DesktopLifecycleReturned, Privacy: DesktopPrivacyNormal,
	}
	graph, err := CompileDesktopTypedGraph(rulebook, observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"normalize", "attention", "silent", "react", "initiate", "final"} {
		if !graph.HasNode(key) {
			t.Fatalf("compiled graph omitted %q", key)
		}
	}
	if graph.NodeCount() != 6 {
		t.Fatalf("node count = %d, want 6", graph.NodeCount())
	}
	result, err := graph.Invoke(context.Background(), "normalize", "final", DesktopGraphState{Observation: observation})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Normalized || result.KnowledgeSelected || result.MemorySelected || result.PlannerSelected || result.Persisted || result.Action != DesktopActionInitiate {
		t.Fatalf("execution result = %#v", result)
	}
}

func TestCompileDesktopTypedGraphDoesNotCreateForbiddenNodes(t *testing.T) {
	now := time.Now()
	rulebook := privateDesktopRulebook(now)
	rulebook.AllowsKnowledge = false
	rulebook.AllowsPersonalMemory = false
	rulebook.AllowsPlanner = false
	rulebook.AllowsInitiation = false
	graph, err := CompileDesktopTypedGraph(rulebook, DesktopObservation{
		ObservationID: "obs-minimal", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerPeriodic,
		Privacy: DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if graph.NodeCount() != 5 || !graph.HasNode("normalize") || !graph.HasNode("attention") || !graph.HasNode("silent") || !graph.HasNode("react") || !graph.HasNode("final") {
		t.Fatalf("minimal graph has %d nodes", graph.NodeCount())
	}
	for _, key := range []string{"knowledge", "memory", "planner", "respond", "persist"} {
		if graph.HasNode(key) {
			t.Fatalf("minimal graph included forbidden node %q", key)
		}
	}
}

func TestDesktopTypedGraphUsesAttentionDecisionBeforePlanner(t *testing.T) {
	now := time.Now()
	observation := DesktopObservation{
		ObservationID: "obs-budget", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerLifecycle,
		Lifecycle: DesktopLifecycleReturned, Privacy: DesktopPrivacyNormal,
	}
	graph, err := CompileDesktopTypedGraph(privateDesktopRulebook(now), observation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := graph.Invoke(context.Background(), "normalize", "final", DesktopGraphState{
		Observation: observation, AttentionDecision: DesktopActionSilent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != DesktopActionSilent || result.PlannerSelected || result.ResponseReady || result.Persisted {
		t.Fatalf("execution result = %#v", result)
	}
}

func TestDesktopTypedGraphInitiateNodeCallsCoreHook(t *testing.T) {
	now := time.Now()
	observation := DesktopObservation{
		ObservationID: "obs-initiate", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerLifecycle,
		Lifecycle: DesktopLifecycleReturned, Privacy: DesktopPrivacyNormal,
	}
	graph, err := CompileDesktopTypedGraph(privateDesktopRulebook(now), observation)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := graph.Invoke(context.Background(), "normalize", "final", DesktopGraphState{
		Observation: observation, AttentionDecision: DesktopActionInitiate,
		Initiate: func(_ context.Context, got DesktopObservation) error {
			called = got.ObservationID == observation.ObservationID
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !result.InitiationScheduled {
		t.Fatalf("called=%t result=%#v", called, result)
	}
}

func TestCompileDesktopTypedGraphPrivacyStopsAfterAttention(t *testing.T) {
	now := time.Now()
	graph, err := CompileDesktopTypedGraph(privateDesktopRulebook(now), DesktopObservation{
		ObservationID: "obs-private", TimestampUnixMS: now.UnixMilli(), Trigger: DesktopTriggerLifecycle,
		Lifecycle: DesktopLifecycleReturned, Privacy: DesktopPrivacyLocked,
	})
	if err != nil {
		t.Fatal(err)
	}
	if graph.NodeCount() != 4 || !graph.HasNode("silent") || graph.HasNode("planner") || graph.HasNode("memory") || graph.HasNode("react") {
		t.Fatalf("privacy graph has %d nodes", graph.NodeCount())
	}
}
