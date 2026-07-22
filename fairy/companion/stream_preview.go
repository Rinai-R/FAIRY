package companion

import (
	"strings"

	"fairy/model"
)

// streamPreviewAccumulator only releases a preview after the complete strict
// reply envelope validates. Partial JSON and non-text provider events remain private.
type streamPreviewAccumulator struct {
	text      strings.Builder
	states    []VisualState
	published bool
}

func newStreamPreviewAccumulator(states []VisualState) *streamPreviewAccumulator {
	return &streamPreviewAccumulator{states: states}
}

func (a *streamPreviewAccumulator) Observe(event model.StreamEvent) (CompiledReply, bool) {
	if a == nil || a.published || event.Type != "text_delta" || event.Data == "" {
		return CompiledReply{}, false
	}
	a.text.WriteString(event.Data)
	preview, err := CompileReply(a.text.String(), a.states)
	if err != nil {
		return CompiledReply{}, false
	}
	a.published = true
	return preview, true
}
