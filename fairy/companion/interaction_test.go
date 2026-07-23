package companion

import (
	"errors"
	"strings"
	"testing"

	"fairy/character"
	"fairy/interaction"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

func TestInteractionMemoryPolicySelectsToolsAndInstructions(t *testing.T) {
	public := publicAmbientResolved()
	tools := RespondToolSpecsForInteraction(true, public)
	if len(tools) != 2 || tools[0].Name != toolPublicMemorySearch || tools[1].Name != toolWebSearch {
		t.Fatalf("public tools = %#v", tools)
	}
	instructions := RespondInstructionsForInteraction(true, public)
	if strings.Contains(instructions, "personal memories") || !strings.Contains(instructions, toolPublicMemorySearch) || !strings.Contains(instructions, "PUBLIC GROUP IDENTITY OVERRIDE") || !strings.Contains(instructions, "high-performance robot") {
		t.Fatalf("public instructions violate memory policy: %s", instructions)
	}
	privateInstructions := RespondInstructionsForInteraction(true, desktopResolved())
	if strings.Contains(privateInstructions, "PUBLIC GROUP IDENTITY OVERRIDE") {
		t.Fatalf("private instructions inherited public identity boundary: %s", privateInstructions)
	}
	for _, tool := range RespondToolSpecsForInteraction(true, desktopResolved()) {
		if tool.Name == toolMemorySearch {
			return
		}
	}
	t.Fatal("personal interaction lost memory_search")
}

func TestInteractionPresentationAndMemoryAreIndependent(t *testing.T) {
	desktop, err := interactionSegment(desktopResolved())
	if err != nil {
		t.Fatal(err)
	}
	ownerIM, err := interactionSegment(ownerIMResolved())
	if err != nil {
		t.Fatal(err)
	}
	publicIM, err := interactionSegment(publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	if desktop.Presentation != interaction.PresentationEmbodied || ownerIM.Presentation != interaction.PresentationChat {
		t.Fatalf("presentations = %#v / %#v", desktop, ownerIM)
	}
	if desktop.MemoryPolicy != interaction.MemoryPersonal || ownerIM.MemoryPolicy != interaction.MemoryPersonal || publicIM.MemoryPolicy != interaction.MemoryPublic {
		t.Fatalf("memory policies = %q/%q/%q", desktop.MemoryPolicy, ownerIM.MemoryPolicy, publicIM.MemoryPolicy)
	}
	if desktop.PresenceProjection != presencePrivateCompanion || ownerIM.PresenceProjection != presencePrivateCompanion {
		t.Fatalf("private projections = %q/%q", desktop.PresenceProjection, ownerIM.PresenceProjection)
	}
	if publicIM.PresenceProjection != presencePublicPeer {
		t.Fatalf("public projection = %q", publicIM.PresenceProjection)
	}
	if !strings.Contains(desktop.PresenceGuidance, "private owner interaction") || !strings.Contains(publicIM.PresenceGuidance, "public social setting") {
		t.Fatalf("presence guidance private=%q public=%q", desktop.PresenceGuidance, publicIM.PresenceGuidance)
	}
}

func TestStablePrefixAndProfileProjectionFollowResolvedInteraction(t *testing.T) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	prefix, err := BuildStablePrefixItems(record, nil, states)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := BuildRespondContextSlots(record, nil, memory.PromptWindowRecord{Revision: 1}, nil, states, memory.RetrievalContext{}, desktopResolved())
	if err != nil {
		t.Fatal(err)
	}
	public, err := BuildRespondContextSlots(record, nil, memory.PromptWindowRecord{Revision: 1}, nil, states, memory.RetrievalContext{}, publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1, 3} {
		if personal[index].Items[0].Content != prefix[index].Content || personal[index].Items[0].Content != public[index].Items[0].Content {
			t.Fatalf("stable prefix item %d drifted", index)
		}
	}
	if !strings.Contains(personal[4].Items[0].Content, `"presenceProjection":"private_companion"`) {
		t.Fatalf("personal interaction slot = %s", personal[4].Items[0].Content)
	}
	if !strings.Contains(public[4].Items[0].Content, `"presenceProjection":"public_peer"`) {
		t.Fatalf("public interaction slot = %s", public[4].Items[0].Content)
	}
	if public[2].Present || public[2].OmitReason != "public_interaction" || !personal[2].Present {
		t.Fatalf("profile projection personal=%#v public=%#v", personal[2], public[2])
	}
}

func TestPublicPromptAndCompactionOmitPrivateProfile(t *testing.T) {
	name := "PRIVATE-NAME"
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}
	profileSnapshot := &profile.Snapshot{Revision: 1, PreferredName: &name}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	public, err := BuildRespondInput(record, profileSnapshot, memory.PromptWindowRecord{}, nil, states, memory.RetrievalContext{}, publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	personal, err := BuildRespondInput(record, profileSnapshot, memory.PromptWindowRecord{}, nil, states, memory.RetrievalContext{}, ownerIMResolved())
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateNameProjection(t, public, personal, name)

	messages := []memory.MessageRecord{{Role: "user", Content: "消息", Sequence: 1}}
	publicCompact, err := BuildCompactInput(record, profileSnapshot, memory.PromptWindowRecord{}, messages, states, publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	personalCompact, err := BuildCompactInput(record, profileSnapshot, memory.PromptWindowRecord{}, messages, states, desktopResolved())
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateNameProjection(t, publicCompact, personalCompact, name)
}

func TestBindResolveInteractionAndMissingBindingFailure(t *testing.T) {
	service := NewCompanionService()
	service.memory = &participationMemory{binding: publicAmbientBinding(), found: true}
	resolved, err := service.ResolveInteraction("conv-durable")
	if err != nil || resolved != publicAmbientResolved() {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	service.memory = &participationMemory{}
	resolved, err = service.ResolveInteraction("conv-durable")
	if err != nil || resolved != publicAmbientResolved() {
		t.Fatalf("cached resolved = %#v, %v", resolved, err)
	}
	if _, err := service.ResolveInteraction("missing"); err == nil || !strings.Contains(err.Error(), "no interaction binding") {
		t.Fatalf("missing binding error = %v", err)
	}
	service.memory = &participationMemory{lookupErr: errors.New("db down")}
	if _, err := service.ResolveInteraction("db-error"); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("lookup error = %v", err)
	}
}

func assertPrivateNameProjection(t *testing.T, public, personal []model.PromptItem, name string) {
	t.Helper()
	for _, item := range public {
		if strings.Contains(item.Content, name) {
			t.Fatalf("public prompt leaked profile: %s", item.Content)
		}
	}
	for _, item := range personal {
		if strings.Contains(item.Content, name) {
			return
		}
	}
	t.Fatal("personal prompt lost profile")
}

func desktopResolved() interaction.Resolved {
	return interaction.Resolved{Endpoint: interaction.EndpointDesktop, Facts: interaction.Facts{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}, Principal: interaction.PrincipalOwner, Memory: interaction.MemoryPersonal}
}

func ownerIMResolved() interaction.Resolved {
	return interaction.Resolved{Endpoint: interaction.EndpointIM, Facts: interaction.Facts{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationChat, PrincipalNamespace: "qq.onebot", PrincipalDigest: strings.Repeat("a", 64)}, Principal: interaction.PrincipalOwner, Memory: interaction.MemoryPersonal}
}

func publicAmbientBinding() interaction.Binding {
	return interaction.Binding{Endpoint: interaction.EndpointIM, Facts: interaction.Facts{Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient, Presentation: interaction.PresentationChat}}
}

func publicAmbientResolved() interaction.Resolved {
	return interaction.Resolved{Endpoint: interaction.EndpointIM, Facts: publicAmbientBinding().Facts, Principal: interaction.PrincipalNone, Memory: interaction.MemoryPublic}
}
