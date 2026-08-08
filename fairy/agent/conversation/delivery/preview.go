package delivery

import (
	"strings"

	"fairy/agent/reply"
	"fairy/runtime/model"
)

// PreviewAccumulator only releases a preview after the complete strict reply
// envelope validates. Partial JSON and non-text provider events remain private.
type PreviewAccumulator struct {
	text      strings.Builder
	states    []reply.VisualState
	published bool
}

func NewPreviewAccumulator(states []reply.VisualState) *PreviewAccumulator {
	return &PreviewAccumulator{states: states}
}

func (a *PreviewAccumulator) Observe(event model.StreamEvent) (reply.CompiledReply, bool) {
	if a == nil || a.published || event.Type != "text_delta" || event.Data == "" {
		return reply.CompiledReply{}, false
	}
	a.text.WriteString(event.Data)
	preview, err := reply.CompileReply(a.text.String(), a.states)
	if err != nil {
		return reply.CompiledReply{}, false
	}
	a.published = true
	return preview, true
}
