package companion

import (
	"context"
	"testing"

	"fairy/model"
)

type gateDesktopCoordinator struct{ available bool }

func (coordinator gateDesktopCoordinator) Available(string) bool { return coordinator.available }
func (gateDesktopCoordinator) Observe(context.Context, DesktopToolRequest) (DesktopToolEvidence, error) {
	return DesktopToolEvidence{}, nil
}
func (gateDesktopCoordinator) CancelTurn(context.Context, string, string) error { return nil }

func TestDesktopToolRequiresVisionPrivateDesktopAndAvailableSurface(t *testing.T) {
	available := gateDesktopCoordinator{available: true}
	if !desktopToolAllowed(true, desktopResolved(), available, "conversation-1") {
		t.Fatal("valid private desktop tool was rejected")
	}
	for name, allowed := range map[string]bool{
		"vision disabled": desktopToolAllowed(false, desktopResolved(), available, "conversation-1"),
		"surface missing": desktopToolAllowed(true, desktopResolved(), gateDesktopCoordinator{}, "conversation-1"),
		"owner IM":        desktopToolAllowed(true, ownerIMResolved(), available, "conversation-1"),
		"public IM":       desktopToolAllowed(true, publicAmbientResolved(), available, "conversation-1"),
	} {
		if allowed {
			t.Fatalf("%s unexpectedly enabled desktop tool", name)
		}
	}
	tools := respondToolSpecsForRuntime(false, desktopResolved(), true)
	if tools[len(tools)-1].Name != toolDesktopObserve {
		t.Fatalf("runtime tools = %#v", tools)
	}
}

func TestDesktopToolArgumentsAndLedgerRedaction(t *testing.T) {
	if err := validateDesktopToolArguments(`{}`); err != nil {
		t.Fatal(err)
	}
	if err := validateDesktopToolArguments(`{"query":"secret"}`); err == nil {
		t.Fatal("desktop tool accepted arguments")
	}
	left := desktopToolPromptItems("call-1", `{}`, DesktopToolEvidence{MediaType: "image/png", DataURL: "data:image/png;base64,AAAA"})
	right := desktopToolPromptItems("call-1", `{}`, DesktopToolEvidence{MediaType: "image/png", DataURL: "data:image/png;base64,BBBB"})
	if runtimeHash(redactPromptImagesForLedger(left)) != runtimeHash(redactPromptImagesForLedger(right)) {
		t.Fatal("runtime ledger identity depends on raw desktop image")
	}
	parts := *left[1].Parts
	if parts[1].Type != model.PromptContentImage || parts[1].ImagePurpose != "desktop_observation" {
		t.Fatalf("desktop result parts = %#v", parts)
	}
}
