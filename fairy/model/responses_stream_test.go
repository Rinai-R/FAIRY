package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResponsesStreamBoundsAccumulatedText(t *testing.T) {
	state := &responsesStreamState{}
	delivered := 0
	_, err := state.handle(
		fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, strings.Repeat("x", MaxModelStreamPayloadBytes+1)),
		func(StreamEvent) { delivered++ },
	)
	if !errors.Is(err, ErrModelStreamCapacity) || delivered != 0 {
		t.Fatalf("error=%v delivered=%d", err, delivered)
	}
}

func TestResponsesStreamBoundsFunctionCallsAndArguments(t *testing.T) {
	state := &responsesStreamState{}
	for index := 0; index < MaxModelFunctionCalls; index++ {
		payload := fmt.Sprintf(
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call-%d","name":"tool","arguments":"{}"}}`,
			index,
		)
		if _, err := state.handle(payload, nil); err != nil {
			t.Fatalf("call %d error = %v", index, err)
		}
	}
	_, err := state.handle(
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"overflow","name":"tool","arguments":"{}"}}`,
		nil,
	)
	if !errors.Is(err, ErrModelStreamCapacity) {
		t.Fatalf("call capacity error = %v", err)
	}

	arguments := strings.Repeat("x", MaxModelFunctionArgumentsBytes+1)
	state = &responsesStreamState{}
	_, err = state.handle(
		fmt.Sprintf(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call","name":"tool","arguments":%q}}`, arguments),
		nil,
	)
	if !errors.Is(err, ErrModelStreamCapacity) {
		t.Fatalf("argument capacity error = %v", err)
	}
}

func TestResponsesStreamBoundsCompletedResponseBeforeParsing(t *testing.T) {
	state := &responsesStreamState{}
	payload := fmt.Sprintf(
		`{"type":"response.completed","response":{"padding":%q}}`,
		strings.Repeat("x", MaxModelCompletedResponseBytes),
	)
	if _, err := state.handle(payload, nil); !errors.Is(err, ErrModelStreamCapacity) {
		t.Fatalf("completed response error = %v", err)
	}
}

func TestResponsesStreamBoundsCompletedTextWhileExtracting(t *testing.T) {
	state := &responsesStreamState{}
	payload := fmt.Sprintf(
		`{"type":"response.completed","response":{"id":"response","output":[{"type":"message","content":[{"text":%q}]}]}}`,
		strings.Repeat("x", MaxModelStreamPayloadBytes+1),
	)
	if _, err := state.handle(payload, nil); !errors.Is(err, ErrModelStreamCapacity) {
		t.Fatalf("completed text error = %v", err)
	}
}
