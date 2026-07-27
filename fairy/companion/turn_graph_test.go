package companion

import (
	"context"
	"errors"
	"testing"
)

func TestTurnGraphProgramCompilesAndRunsOneOrderedPath(t *testing.T) {
	state := &turnGraphState{}
	program := newTurnGraphProgram(NewCompanionService(), "conversation-1", "turn-1")
	state.program = program
	var order []string
	for _, key := range []string{"interpreting", "gathering"} {
		key := key
		program.add(turnStep(key, key, func(_ context.Context, state *turnGraphState) (*turnGraphState, error) {
			order = append(order, key)
			return state, nil
		}), turnStateInterpreting)
	}
	for _, key := range []string{"planning", "responding", "persist"} {
		key := key
		program.addOutcome(key, key, turnStatePlanning, func() (TurnOutcome, error) {
			order = append(order, key)
			return TurnOutcome{TurnID: key}, nil
		})
	}

	outcome, err := program.run(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"interpreting", "gathering", "planning", "responding", "persist"}
	if len(order) != len(want) {
		t.Fatalf("order = %#v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order = %#v", order)
		}
	}
	if outcome.TurnID != "persist" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestTurnGraphProgramStopsAfterNodeFailure(t *testing.T) {
	state := &turnGraphState{}
	program := newTurnGraphProgram(NewCompanionService(), "conversation-1", "turn-1")
	state.program = program
	failed := errors.New("planning failed")
	responded := false
	program.addOutcome("planning", "planning", turnStatePlanning, func() (TurnOutcome, error) {
		return TurnOutcome{}, failed
	})
	program.addOutcome("responding", "responding", turnStateResponding, func() (TurnOutcome, error) {
		responded = true
		return TurnOutcome{}, nil
	})

	if _, err := program.run(t.Context(), state); !errors.Is(err, failed) {
		t.Fatalf("error = %v", err)
	}
	if responded {
		t.Fatal("responding ran after planning failure")
	}
}
