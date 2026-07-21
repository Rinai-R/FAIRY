package companion

import (
	"strings"
	"testing"

	"fairy/character"
	"fairy/memory"
	"fairy/profile"
)

func TestGroupSurfaceHasPublicKnowledgeButNoPersonalMemoryTool(t *testing.T) {
	groupPolicy, err := InteractionPolicyForSurface(SurfaceIMGroup)
	if err != nil {
		t.Fatal(err)
	}
	groupTools := RespondToolSpecsForPolicy(true, groupPolicy)
	if len(groupTools) != 2 || groupTools[0].Name != toolPublicMemorySearch || groupTools[1].Name != toolWebSearch {
		t.Fatalf("group tools = %#v, want public_memory_search + web_search", groupTools)
	}
	instructions := RespondInstructionsForPolicy(true, groupPolicy)
	if strings.Contains(instructions, "profile, preference, experience") || strings.Contains(instructions, "Character, profile") || strings.Contains(instructions, "personal memories") || !strings.Contains(instructions, toolPublicMemorySearch) {
		t.Fatalf("group instructions violate public memory policy: %s", instructions)
	}
	desktopPolicy, err := InteractionPolicyForSurface(SurfaceDesktop)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range RespondToolSpecsForPolicy(true, desktopPolicy) {
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
	desktop, err := surfaceChannelSegment(SurfaceDesktop)
	if err != nil {
		t.Fatal(err)
	}
	group, err := surfaceChannelSegment(SurfaceIMGroup)
	if err != nil {
		t.Fatal(err)
	}
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

func TestInteractionPolicyForSurface(t *testing.T) {
	tests := []struct {
		surface SurfaceKind
		want    InteractionPolicy
	}{
		{SurfaceDesktop, InteractionPolicy{Audience: AudiencePrivate, Initiation: InitiationDirect, Presentation: PresentationEmbodied}},
		{SurfaceIMPrivate, InteractionPolicy{Audience: AudiencePrivate, Initiation: InitiationDirect, Presentation: PresentationChat}},
		{SurfaceIMGroup, InteractionPolicy{Audience: AudiencePublic, Initiation: InitiationAmbient, Presentation: PresentationChat}},
	}
	for _, test := range tests {
		got, err := InteractionPolicyForSurface(test.surface)
		if err != nil || got != test.want {
			t.Fatalf("policy(%q) = %#v, %v; want %#v", test.surface, got, err, test.want)
		}
	}
	if _, err := InteractionPolicyForSurface(SurfaceKind("discord_group")); err == nil {
		t.Fatal("unknown surface policy must fail")
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
	for _, i := range []int{0, 1, 3} {
		if desktopSlots[i].Items[0].Content != prefixA[i].Content {
			t.Fatalf("desktop stable[%d] drifted from BuildStablePrefixItems", i)
		}
		if desktopSlots[i].Items[0].Content != groupSlots[i].Items[0].Content {
			t.Fatalf("stable prefix item %d differs across surfaces", i)
		}
	}
	if groupSlots[2].Present || groupSlots[2].OmitReason != "public_surface" || desktopSlots[2].Items[0].Content != prefixA[2].Content {
		t.Fatalf("profile projection desktop=%#v group=%#v", desktopSlots[2], groupSlots[2])
	}
	if desktopSlots[4].Items[0].Content == groupSlots[4].Items[0].Content {
		t.Fatal("surface slots should differ between desktop and im_group")
	}
}

func TestGroupPromptOmitsPreferredNameWhilePrivateRetainsIt(t *testing.T) {
	name := "PRIVATE-NAME-NEVER-GROUP"
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	userProfile := &profile.Snapshot{Revision: 1, PreferredName: &name}
	group, err := BuildRespondInput(record, userProfile, memory.PromptWindowRecord{}, nil, states, memory.RetrievalContext{}, SurfaceIMGroup)
	if err != nil {
		t.Fatal(err)
	}
	private, err := BuildRespondInput(record, userProfile, memory.PromptWindowRecord{}, nil, states, memory.RetrievalContext{}, SurfaceIMPrivate)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range group {
		if strings.Contains(item.Content, name) || strings.Contains(item.Content, `"contextType":"user_profile"`) {
			t.Fatalf("group prompt leaked profile: %s", item.Content)
		}
	}
	found := false
	for _, item := range private {
		found = found || strings.Contains(item.Content, name)
	}
	if !found {
		t.Fatal("private prompt lost preferred name")
	}
}

func TestGroupCompactionOmitsProfileWhilePrivateRetainsIt(t *testing.T) {
	name := "PRIVATE-COMPACTION-NAME"
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}
	userProfile := &profile.Snapshot{Revision: 1, PreferredName: &name}
	messages := []memory.MessageRecord{{Role: "user", Content: "群聊消息", Sequence: 1}}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	group, err := BuildCompactInput(record, userProfile, memory.PromptWindowRecord{}, messages, states, SurfaceIMGroup)
	if err != nil {
		t.Fatal(err)
	}
	private, err := BuildCompactInput(record, userProfile, memory.PromptWindowRecord{}, messages, states, SurfaceDesktop)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range group {
		if strings.Contains(item.Content, name) || strings.Contains(item.Content, `"contextType":"user_profile"`) {
			t.Fatalf("group compact prompt leaked profile: %s", item.Content)
		}
	}
	found := false
	for _, item := range private {
		found = found || strings.Contains(item.Content, name)
	}
	if !found {
		t.Fatal("private compact prompt lost preferred name")
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
