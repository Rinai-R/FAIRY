package tool

import (
	"testing"

	"fairy/runtime/model"
	"fairy/transport/session"
)

func TestRouteCallEnforcesInteractionAndAvailability(t *testing.T) {
	private := session.Resolved{Memory: session.MemoryPersonal}
	public := session.Resolved{Memory: session.MemoryPublic, Facts: session.Facts{Initiation: session.InitiationAmbient}}
	tests := []struct {
		name         string
		call         model.FunctionCall
		availability RuntimeAvailability
		resolved     session.Resolved
		disposition  CallDisposition
		status       string
	}{
		{name: "private memory", call: model.FunctionCall{Name: MemorySearch, Arguments: `{"query":"偏好"}`}, resolved: private, disposition: CallReady, status: "ok"},
		{name: "private memory denied in public", call: model.FunctionCall{Name: MemorySearch, Arguments: `{"query":"偏好"}`}, resolved: public, disposition: CallReject, status: "rejected"},
		{name: "disabled web becomes result", call: model.FunctionCall{Name: WebSearch, Arguments: `{"query":"新闻"}`}, resolved: private, disposition: CallResult, status: "disabled"},
		{name: "desktop empty args", call: model.FunctionCall{Name: DesktopObserve, Arguments: `{}`}, availability: RuntimeAvailability{Desktop: true}, resolved: private, disposition: CallReady, status: "ok"},
		{name: "desktop rejects fields", call: model.FunctionCall{Name: DesktopObserve, Arguments: `{"query":"secret"}`}, availability: RuntimeAvailability{Desktop: true}, resolved: private, disposition: CallResult, status: "args_invalid"},
		{name: "unknown is model visible", call: model.FunctionCall{Name: "unknown", Arguments: `{"query":"x"}`}, resolved: private, disposition: CallResult, status: "not_whitelisted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := RouteCall(test.call, test.availability, test.resolved)
			if result.Disposition != test.disposition || result.Status != test.status {
				t.Fatalf("route = %#v", result)
			}
		})
	}
}

func TestSpecsForRuntimeOwnsOptionalToolSchemas(t *testing.T) {
	tools := SpecsForRuntime(RuntimeAvailability{Desktop: true, Sticker: true}, session.Resolved{Memory: session.MemoryPersonal})
	if tools[len(tools)-2].Name != DesktopObserve || tools[len(tools)-1].Name != StickerSearch {
		t.Fatalf("optional tools = %#v", tools)
	}
}
