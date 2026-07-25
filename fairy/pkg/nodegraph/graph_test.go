package nodegraph

import (
	"context"
	"errors"
	"testing"
)

type testState struct {
	Route string
	Order []string
}

func TestBuilderComposesTypedStepsAndDeclaredBranch(t *testing.T) {
	step := func(key string) Node[testState] {
		return Step(key, key, func(_ context.Context, state testState) (testState, error) {
			state.Order = append(state.Order, key)
			return state, nil
		})
	}
	g, err := New[testState](8).
		Nodes(step("start"), step("left"), step("right"), step("end")).
		Path("left", "end").
		Path("right", "end").
		Branch("start", func(_ context.Context, state testState) (string, error) { return state.Route, nil }, "left", "right").
		Compile()
	if err != nil {
		t.Fatal(err)
	}
	result, err := g.Invoke(context.Background(), "start", "end", testState{Route: "right"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "right", "end"}
	if len(result.Order) != len(want) {
		t.Fatalf("order = %#v", result.Order)
	}
	for i := range want {
		if result.Order[i] != want[i] {
			t.Fatalf("order = %#v", result.Order)
		}
	}
}

func TestGraphRejectsCycleUndeclaredEndpointAndMutationAfterCompile(t *testing.T) {
	noop := func(key string) Node[testState] {
		return Step(key, key, func(_ context.Context, state testState) (testState, error) { return state, nil })
	}
	cycle := New[testState](4).Nodes(noop("a"), noop("b")).Path("a", "b").Path("b", "a")
	if _, err := cycle.Compile(); err == nil {
		t.Fatal("Compile accepted cycle")
	}

	b := New[testState](4).Nodes(noop("a"), noop("b"))
	g, err := b.Path("a", "b").Compile()
	if err != nil || g.NodeCount() != 2 {
		t.Fatalf("Compile() = %#v, %v", g, err)
	}
	if _, err := b.Nodes(noop("c")).Compile(); !errors.Is(err, ErrCompiled) {
		t.Fatalf("mutation error = %v", err)
	}

	bad, err := New[testState](4).
		Nodes(noop("start"), noop("end")).
		Branch("start", func(context.Context, testState) (string, error) { return "missing", nil }, "end").
		Compile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Invoke(context.Background(), "start", "end", testState{}); err == nil {
		t.Fatal("Invoke accepted undeclared branch result")
	}
}

func TestBuilderRejectsUnknownNodeAndDuplicateEdge(t *testing.T) {
	noop := func(key string) Node[testState] {
		return Step(key, key, func(_ context.Context, state testState) (testState, error) { return state, nil })
	}

	if _, err := New[testState](2).Nodes(noop("a")).Path("a", "missing").Compile(); err == nil {
		t.Fatal("Compile accepted an edge to an unknown node")
	}

	if _, err := New[testState](2).
		Nodes(noop("a"), noop("b")).
		Path("a", "b").
		Path("a", "b").
		Compile(); err == nil {
		t.Fatal("Compile accepted a duplicate edge")
	}
}

func TestInvokeHonorsContextCancellationBeforeRunningNode(t *testing.T) {
	ran := false
	g, err := New[testState](1).
		Nodes(Step("only", "only", func(_ context.Context, state testState) (testState, error) {
			ran = true
			return state, nil
		})).
		Compile()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Invoke(ctx, "only", "only", testState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Invoke error = %v", err)
	}
	if ran {
		t.Fatal("Invoke ran a node after context cancellation")
	}
}

func TestNodeFailureStopsFollowingSteps(t *testing.T) {
	failed := errors.New("stop")
	g, err := New[testState](3).
		Nodes(
			Step("a", "a", func(_ context.Context, state testState) (testState, error) { return state, failed }),
			Step("b", "b", func(_ context.Context, state testState) (testState, error) {
				state.Order = append(state.Order, "b")
				return state, nil
			}),
		).
		Path("a", "b").Compile()
	if err != nil {
		t.Fatal(err)
	}
	result, err := g.Invoke(context.Background(), "a", "b", testState{})
	if !errors.Is(err, failed) || len(result.Order) != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestInvokeObservedEmitsStructureWithoutState(t *testing.T) {
	g, err := New[testState](2).
		Nodes(
			Step("a", "attention", func(_ context.Context, state testState) (testState, error) { return state, nil }),
			Step("b", "respond", func(_ context.Context, state testState) (testState, error) { return state, nil }),
		).
		Path("a", "b").
		Compile()
	if err != nil {
		t.Fatal(err)
	}

	var events []Event
	_, err = g.InvokeObserved(context.Background(), "a", "b", testState{Route: "private-body"}, func(event Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0] != (Event{Node: "a", Kind: "attention", Status: EventStarted}) || events[3] != (Event{Node: "b", Kind: "respond", Status: EventCompleted}) {
		t.Fatalf("events = %#v", events)
	}
}
