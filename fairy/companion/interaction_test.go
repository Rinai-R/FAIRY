package companion

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/persona"

	"fairy/session"
)

type interactionBindingStoreStub struct {
	mu       sync.Mutex
	bindings map[string]session.Binding
	calls    map[string]int
}

func (s *interactionBindingStoreStub) LookupEndpointForConversation(conversationID string) (session.Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[conversationID]++
	binding, found := s.bindings[conversationID]
	return binding, found, nil
}

func (s *interactionBindingStoreStub) callCount(conversationID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[conversationID]
}

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

func TestInteractionBindingCacheBoundsLRUAndRejectsConflict(t *testing.T) {
	cache := newInteractionBindingCache(2)
	binding := publicAmbientBinding()
	if err := cache.Put("conversation-1", binding); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put("conversation-2", binding); err != nil {
		t.Fatal(err)
	}
	if _, found := cache.Get("conversation-1"); !found {
		t.Fatal("expected conversation-1 cache hit")
	}
	if err := cache.Put("conversation-3", binding); err != nil {
		t.Fatal(err)
	}
	if got := cache.Len(); got != 2 {
		t.Fatalf("cache length = %d, want 2", got)
	}
	if _, found := cache.Get("conversation-2"); found {
		t.Fatal("least recently used binding was not evicted")
	}
	if _, found := cache.Get("conversation-1"); !found {
		t.Fatal("recently used binding was evicted")
	}
	conflict := session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience:     session.AudienceSingle,
			Initiation:   session.InitiationDirect,
			Presentation: session.PresentationEmbodied,
		},
	}
	if err := cache.Put("conversation-1", conflict); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("conflicting binding error = %v", err)
	}
}

func TestInteractionBindingCacheReloadsEvictedBindingFromDurableStore(t *testing.T) {
	binding := publicAmbientBinding()
	store := &interactionBindingStoreStub{
		bindings: map[string]session.Binding{
			"conversation-1": binding,
			"conversation-2": binding,
			"conversation-3": binding,
		},
		calls: make(map[string]int),
	}
	service := NewCompanionService()
	service.interactions = newInteractionBindingCache(2)
	service.memory.ambient.bindings = store

	for _, conversationID := range []string{"conversation-1", "conversation-2"} {
		resolved, err := service.ResolveInteraction(conversationID)
		if err != nil || resolved != publicAmbientResolved() {
			t.Fatalf("ResolveInteraction(%q) = %#v, %v", conversationID, resolved, err)
		}
	}
	if _, found := service.BoundInteraction("conversation-1"); !found {
		t.Fatal("expected conversation-1 cache hit")
	}
	if _, err := service.ResolveInteraction("conversation-3"); err != nil {
		t.Fatal(err)
	}
	if _, found := service.BoundInteraction("conversation-2"); found {
		t.Fatal("least recently used durable binding was not evicted")
	}
	resolved, err := service.ResolveInteraction("conversation-2")
	if err != nil || resolved != publicAmbientResolved() {
		t.Fatalf("reloaded resolved = %#v, %v", resolved, err)
	}
	if got := store.callCount("conversation-2"); got != 2 {
		t.Fatalf("durable lookup count = %d, want 2", got)
	}
	if got := service.interactions.Len(); got != 2 {
		t.Fatalf("cache length after reload = %d, want 2", got)
	}
}

func TestInteractionBindingCacheConcurrentAccessRemainsBounded(t *testing.T) {
	const capacity = 8
	cache := newInteractionBindingCache(capacity)
	binding := publicAmbientBinding()
	var workers sync.WaitGroup
	for index := 0; index < 64; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			conversationID := fmt.Sprintf("conversation-%d", index)
			if err := cache.Put(conversationID, binding); err != nil {
				t.Errorf("Put(%q): %v", conversationID, err)
				return
			}
			cache.Get(conversationID)
		}(index)
	}
	workers.Wait()
	if got := cache.Len(); got != capacity {
		t.Fatalf("cache length = %d, want %d", got, capacity)
	}
}

func TestOutputCapabilitiesAreExplicitLiveSessionFacts(t *testing.T) {
	service := NewCompanionService()
	if service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("missing live capability defaulted to supported")
	}
	if err := service.BindOutputCapabilities("owner-1", "conversation-1", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if !service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("advertised sticker capability was not retained")
	}
	if err := service.BindOutputCapabilities("owner-2", "conversation-1", session.OutputCapabilities{}); err != nil {
		t.Fatal(err)
	}
	if !service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("false capability from a second owner overwrote a live true capability")
	}
	if err := service.BindOutputCapabilities("owner-1", "conversation-1", session.OutputCapabilities{}); err != nil {
		t.Fatal(err)
	}
	if service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("same owner capability did not replace its prior value")
	}
	if got := len(service.outputCapabilities["conversation-1"]); got != 2 {
		t.Fatalf("lease count after same-owner replacement = %d, want 2", got)
	}
	service.UnbindOutputCapabilities("owner-1", "conversation-1")
	if _, exists := service.outputCapabilities["conversation-1"]["owner-1"]; exists {
		t.Fatal("owner lease remained after unbind")
	}
	if err := service.BindOutputCapabilities("owner-2", "conversation-1", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	service.UnbindOutputCapabilities("owner-1", "conversation-1")
	if !service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("idempotent unbind removed another owner's capability")
	}
	service.UnbindOutputCapabilities("owner-2", "conversation-1")
	if service.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("last owner unbind did not restore zero capabilities")
	}
	if _, exists := service.outputCapabilities["conversation-1"]; exists {
		t.Fatal("empty conversation capability registry entry was retained")
	}
	if err := service.BindOutputCapabilities(" ", "conversation-1", session.OutputCapabilities{}); err == nil {
		t.Fatal("blank owner capability binding accepted")
	}
	if err := service.BindOutputCapabilities("owner-1", " ", session.OutputCapabilities{}); err == nil {
		t.Fatal("blank conversation capability binding accepted")
	}
}

func TestOutputCapabilitiesAreClearedWhenCompanionCloses(t *testing.T) {
	service := NewCompanionService()
	if err := service.BindOutputCapabilities("owner-1", "conversation-1", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if service.OutputCapabilities("conversation-1").Sticker || len(service.outputCapabilities) != 0 {
		t.Fatal("closing Companion retained process-local capability leases")
	}
}

func TestOutputCapabilityLeasesBoundReplaceAndRecover(t *testing.T) {
	service := NewCompanionService()
	service.outputCapabilityCapacity = 2
	if err := service.BindOutputCapabilities("owner-1", "conversation-1", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.BindOutputCapabilities("owner-2", "conversation-1", session.OutputCapabilities{}); err != nil {
		t.Fatal(err)
	}
	if err := service.BindOutputCapabilities("owner-1", "conversation-1", session.OutputCapabilities{}); err != nil {
		t.Fatalf("replacement at capacity error = %v", err)
	}
	if err := service.BindOutputCapabilities("owner-3", "conversation-2", session.OutputCapabilities{Sticker: true}); !errors.Is(err, ErrOutputCapabilityCapacity) {
		t.Fatalf("overload error = %v, want ErrOutputCapabilityCapacity", err)
	}
	if service.outputCapabilityLeases != 2 || len(service.outputCapabilities) != 1 {
		t.Fatalf("leases=%d conversations=%d, want 2/1", service.outputCapabilityLeases, len(service.outputCapabilities))
	}
	service.UnbindOutputCapabilities("owner-2", "conversation-1")
	if service.outputCapabilityLeases != 1 {
		t.Fatalf("leases after unbind = %d, want 1", service.outputCapabilityLeases)
	}
	if err := service.BindOutputCapabilities("owner-3", "conversation-2", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatalf("Bind after release error = %v", err)
	}
	if service.outputCapabilityLeases != 2 || len(service.outputCapabilities) != 2 {
		t.Fatalf("leases=%d conversations=%d after replacement", service.outputCapabilityLeases, len(service.outputCapabilities))
	}
	service.clearOutputCapabilities()
	if service.outputCapabilityLeases != 0 || len(service.outputCapabilities) != 0 {
		t.Fatalf("clear retained leases=%d conversations=%d", service.outputCapabilityLeases, len(service.outputCapabilities))
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
