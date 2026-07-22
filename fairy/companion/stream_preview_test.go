package companion

import (
	"testing"

	"fairy/model"
)

func TestStreamPreviewAccumulatorReleasesOnlyCompleteStrictReply(t *testing.T) {
	accumulator := newStreamPreviewAccumulator(visualStates("idle"))
	unsafeParts := []model.StreamEvent{
		{Type: "text_delta", Data: `{"chains":[{"visualState":"idle","text":"半`},
		{Type: "function_calls", Data: `secret-tool-arguments`},
	}
	for _, event := range unsafeParts {
		if preview, ok := accumulator.Observe(event); ok {
			t.Fatalf("partial/internal event produced preview: %#v", preview)
		}
	}
	preview, ok := accumulator.Observe(model.StreamEvent{Type: "text_delta", Data: `截完整"}]}`})
	if !ok {
		t.Fatal("complete strict reply did not produce preview")
	}
	if len(preview.Chains) != 1 || preview.Chains[0].Text != "半截完整" {
		t.Fatalf("preview = %#v", preview)
	}
	if _, ok := accumulator.Observe(model.StreamEvent{Type: "text_delta", Data: `ignored`}); ok {
		t.Fatal("accumulator published more than one preview")
	}
}

func TestStreamPreviewAccumulatorRejectsUnknownFields(t *testing.T) {
	accumulator := newStreamPreviewAccumulator(visualStates("idle"))
	if preview, ok := accumulator.Observe(model.StreamEvent{Type: "text_delta", Data: `{"chains":[{"visualState":"idle","text":"你好","reasoning":"private"}]}`}); ok {
		t.Fatalf("unknown private field produced preview: %#v", preview)
	}
}
