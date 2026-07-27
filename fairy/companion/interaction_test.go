package companion

import (
	"errors"
	"strings"
	"testing"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/persona"

	"fairy/session"
)

func TestInteractionMemoryPolicySelectsToolsAndInstructions(t *testing.T) {
	public := publicAmbientResolved()
	tools := respondToolSpecsForInteraction(true, public)
	if len(tools) != 4 || tools[0].Name != toolPublicMemorySearch || tools[1].Name != toolSocialContextSearch || tools[2].Name != toolSocialExpressionSelect || tools[3].Name != toolWebSearch {
		t.Fatalf("public tools = %#v", tools)
	}
	instructions := respondInstructionsForInteraction(true, public)
	if strings.Contains(instructions, "personal memories") || !strings.Contains(instructions, toolPublicMemorySearch) || !strings.Contains(instructions, toolSocialContextSearch) || !strings.Contains(instructions, toolSocialExpressionSelect) || !strings.Contains(instructions, "PUBLIC GROUP IDENTITY OVERRIDE") || !strings.Contains(instructions, "high-performance robot") || !strings.Contains(instructions, "Keep emoji light") {
		t.Fatalf("public instructions violate memory policy: %s", instructions)
	}
	privateInstructions := respondInstructionsForInteraction(true, desktopResolved())
	if strings.Contains(privateInstructions, "PUBLIC GROUP IDENTITY OVERRIDE") {
		t.Fatalf("private instructions inherited public identity boundary: %s", privateInstructions)
	}
	for _, tool := range respondToolSpecsForInteraction(true, desktopResolved()) {
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
	if desktop.Presentation != session.PresentationEmbodied || ownerIM.Presentation != session.PresentationChat {
		t.Fatalf("presentations = %#v / %#v", desktop, ownerIM)
	}
	if desktop.MemoryPolicy != session.MemoryPersonal || ownerIM.MemoryPolicy != session.MemoryPersonal || publicIM.MemoryPolicy != session.MemoryPublic {
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
	if !strings.Contains(desktop.MemoryVisibilityHint, "public social history") || !strings.Contains(publicIM.MemoryVisibilityHint, "this group") {
		t.Fatalf("memory visibility hints private=%q public=%q", desktop.MemoryVisibilityHint, publicIM.MemoryVisibilityHint)
	}
}

func TestStablePrefixAndProfileProjectionFollowResolvedInteraction(t *testing.T) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	prefix, err := persona.BuildStablePrefixItems(record, nil, states)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := persona.BuildRespondContextSlots(record, nil, memory.PromptWindowRecord{Revision: 1}, nil, states, memory.RetrievalContext{}, desktopResolved())
	if err != nil {
		t.Fatal(err)
	}
	public, err := persona.BuildRespondContextSlots(record, nil, memory.PromptWindowRecord{Revision: 1}, nil, states, memory.RetrievalContext{}, publicAmbientResolved())
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
	profileSnapshot := &config.ProfileSnapshot{Revision: 1, PreferredName: &name}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	public, err := persona.BuildRespondInput(record, profileSnapshot, memory.PromptWindowRecord{}, nil, states, memory.RetrievalContext{}, publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	personal, err := persona.BuildRespondInput(record, profileSnapshot, memory.PromptWindowRecord{}, nil, states, memory.RetrievalContext{}, ownerIMResolved())
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateNameProjection(t, public, personal, name)

	messages := []memory.MessageRecord{{Role: "user", Content: "消息", Sequence: 1}}
	publicCompact, err := buildCompactInput(record, profileSnapshot, memory.PromptWindowRecord{}, messages, states, publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	personalCompact, err := buildCompactInput(record, profileSnapshot, memory.PromptWindowRecord{}, messages, states, desktopResolved())
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateNameProjection(t, publicCompact, personalCompact, name)
}

func TestBindResolveInteractionAndMissingBindingFailure(t *testing.T) {
	service := NewCompanionService()
	service.memory = participationMemoryPorts(&participationMemory{binding: publicAmbientBinding(), found: true})
	resolved, err := service.ResolveInteraction("conv-durable")
	if err != nil || resolved != publicAmbientResolved() {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	service.memory = participationMemoryPorts(&participationMemory{})
	resolved, err = service.ResolveInteraction("conv-durable")
	if err != nil || resolved != publicAmbientResolved() {
		t.Fatalf("cached resolved = %#v, %v", resolved, err)
	}
	if _, err := service.ResolveInteraction("missing"); err == nil || !strings.Contains(err.Error(), "no interaction binding") {
		t.Fatalf("missing binding error = %v", err)
	}
	service.memory = participationMemoryPorts(&participationMemory{lookupErr: errors.New("db down")})
	if _, err := service.ResolveInteraction("db-error"); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("lookup error = %v", err)
	}
}

func TestOutputCapabilitiesAreExplicitLiveSessionFacts(t *testing.T) {
	service := NewCompanionService()
	if service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("missing live capability defaulted to supported")
	}
	if err := service.BindOutputCapabilities("conversation-1", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if !service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("advertised sticker capability was not retained")
	}
	if err := service.BindOutputCapabilities("conversation-1", session.OutputCapabilities{}); err != nil {
		t.Fatal(err)
	}
	if service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("latest live session capability did not replace prior value")
	}
	if err := service.BindOutputCapabilities(" ", session.OutputCapabilities{}); err == nil {
		t.Fatal("blank conversation capability binding accepted")
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

func desktopResolved() session.Resolved {
	return session.Resolved{Endpoint: session.EndpointDesktop, Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}, Principal: session.PrincipalOwner, Memory: session.MemoryPersonal}
}

func ownerIMResolved() session.Resolved {
	return session.Resolved{Endpoint: session.EndpointIM, Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat, PrincipalNamespace: "qq.onebot", PrincipalDigest: strings.Repeat("a", 64)}, Principal: session.PrincipalOwner, Memory: session.MemoryPersonal}
}

func publicAmbientBinding() session.Binding {
	return session.Binding{Endpoint: session.EndpointIM, Facts: session.Facts{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat}}
}

func publicAmbientResolved() session.Resolved {
	return session.Resolved{Endpoint: session.EndpointIM, Facts: publicAmbientBinding().Facts, Principal: session.PrincipalNone, Memory: session.MemoryPublic}
}
