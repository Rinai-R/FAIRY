package companion

import (
	"strings"
	"testing"

	"fairy/character"
	"fairy/memory"
)

func TestGroupSurfaceHasNoPersonalMemoryTool(t *testing.T) {
	groupTools := RespondToolSpecsForSurface(true, SurfaceIMGroup)
	if len(groupTools) != 1 || groupTools[0].Name != toolWebSearch {
		t.Fatalf("group tools = %#v, want web_search only", groupTools)
	}
	if strings.Contains(RespondInstructionsForSurface(true, SurfaceIMGroup), toolMemorySearch) {
		t.Fatal("group instructions expose memory_search")
	}
	for _, tool := range RespondToolSpecsForSurface(true, SurfaceDesktop) {
		if tool.Name == toolMemorySearch {
			return
		}
	}
	t.Fatal("desktop tool schema lost memory_search")
}

func TestNormalizeSurface(t *testing.T) {
	got, err := NormalizeSurface("")
	if err != nil || got != SurfaceDesktop {
		t.Fatalf("empty = %q %v, want desktop", got, err)
	}
	got, err = NormalizeSurface("im_private")
	if err != nil || got != SurfaceIMPrivate {
		t.Fatalf("im_private = %q %v", got, err)
	}
	if _, err := NormalizeSurface("web_widget"); err == nil {
		t.Fatal("want error for unknown surface")
	}
}

func TestSurfaceChannelSegmentsDifferByKind(t *testing.T) {
	desktop := surfaceChannelSegment(SurfaceDesktop)
	group := surfaceChannelSegment(SurfaceIMGroup)
	if desktop.Kind != "desktop" || group.Kind != "im_group" {
		t.Fatalf("kinds = %#v %#v", desktop, group)
	}
	if !strings.Contains(desktop.OutputContract, "desktop pet") {
		t.Fatalf("desktop contract = %q", desktop.OutputContract)
	}
	if !strings.Contains(group.OutputContract, "group IM") {
		t.Fatalf("group contract = %q", group.OutputContract)
	}
	if strings.Contains(group.MemoryVisibilityHint, "broad") {
		t.Fatalf("group should not claim broad visibility: %q", group.MemoryVisibilityHint)
	}
}

func TestBuildRespondContextSlotsRejectsUnknownSurface(t *testing.T) {
	_, err := BuildRespondContextSlots(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"},
		nil,
		memory.PromptWindowRecord{Revision: 1},
		nil,
		[]VisualState{{ID: "idle", Description: "待机"}},
		memory.RetrievalContext{},
		SurfaceKind("web_widget"),
	)
	if err == nil {
		t.Fatal("BuildRespondContextSlots() error = nil, want unsupported surface")
	}
}

func TestStablePrefixUnaffectedBySurface(t *testing.T) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	prefixA, err := BuildStablePrefixItems(record, nil, states)
	if err != nil {
		t.Fatal(err)
	}
	desktopSlots, err := BuildRespondContextSlots(record, nil, memory.PromptWindowRecord{Revision: 1}, nil, states, memory.RetrievalContext{}, SurfaceDesktop)
	if err != nil {
		t.Fatal(err)
	}
	groupSlots, err := BuildRespondContextSlots(record, nil, memory.PromptWindowRecord{Revision: 1}, nil, states, memory.RetrievalContext{}, SurfaceIMGroup)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if desktopSlots[i].Items[0].Content != prefixA[i].Content {
			t.Fatalf("desktop stable[%d] drifted from BuildStablePrefixItems", i)
		}
		if desktopSlots[i].Items[0].Content != groupSlots[i].Items[0].Content {
			t.Fatalf("stable prefix item %d differs across surfaces", i)
		}
	}
	if desktopSlots[4].Items[0].Content == groupSlots[4].Items[0].Content {
		t.Fatal("surface slots should differ between desktop and im_group")
	}
}

func TestBindAndResolveSurface(t *testing.T) {
	service := NewCompanionService()
	if err := service.BindSurface("conv-1", SurfaceIMGroup); err != nil {
		t.Fatal(err)
	}
	got, err := service.ResolveSurface("conv-1", "")
	if err != nil || got != SurfaceIMGroup {
		t.Fatalf("bound resolve = %q %v", got, err)
	}
	got, err = service.ResolveSurface("conv-1", SurfaceDesktop)
	if err != nil || got != SurfaceDesktop {
		t.Fatalf("override = %q %v", got, err)
	}
	got, err = service.ResolveSurface("other", "")
	if err != nil || got != SurfaceDesktop {
		t.Fatalf("unbound = %q %v", got, err)
	}
	if err := service.BindSurface("conv-1", SurfaceKind("nope")); err == nil {
		t.Fatal("want bind error")
	}
}
