package model

import "testing"

func TestNewCacheKeyInputHashesOnlyStableInstructions(t *testing.T) {
	first := NewCacheKeyInput(PromptLaneParticipate, "model-1", "conversation-1", "stable instructions")
	second := NewCacheKeyInput(PromptLaneParticipate, "model-1", "conversation-1", "stable instructions")
	changed := NewCacheKeyInput(PromptLaneParticipate, "model-1", "conversation-1", "changed instructions")
	if first.StablePromptHash == "" || first.StablePromptHash != second.StablePromptHash {
		t.Fatalf("stable hashes = %q, %q", first.StablePromptHash, second.StablePromptHash)
	}
	if changed.StablePromptHash == first.StablePromptHash {
		t.Fatal("changed instructions did not change stable prompt hash")
	}
}

func TestNewCacheKeyInputWithStablePrefixHashesOnlyStableItems(t *testing.T) {
	prefix := []PromptItem{
		{Type: PromptItemContextData, Content: `{"contextType":"character","revision":2}`},
		{Type: PromptItemContextData, Content: `{"contextType":"interaction","memoryPolicy":"public"}`},
	}
	first, err := NewCacheKeyInputWithStablePrefix(PromptLaneSocialFeedback, "model-1", "conversation-1", "stable instructions", prefix)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCacheKeyInputWithStablePrefix(PromptLaneSocialFeedback, "model-1", "conversation-1", "stable instructions", append([]PromptItem(nil), prefix...))
	if err != nil {
		t.Fatal(err)
	}
	if first.StablePromptHash == "" || first.StablePromptHash != second.StablePromptHash {
		t.Fatalf("stable hashes = %q, %q", first.StablePromptHash, second.StablePromptHash)
	}

	// Dynamic evidence is intentionally not an input to the prefix builder.
	dynamicA := PromptItem{Type: PromptItemContextData, Content: `{"reply":"A"}`}
	dynamicB := PromptItem{Type: PromptItemContextData, Content: `{"reply":"B"}`}
	requestA := CompiledPromptRequest{Input: append(append([]PromptItem(nil), prefix...), dynamicA), CacheInput: &first}
	requestB := CompiledPromptRequest{Input: append(append([]PromptItem(nil), prefix...), dynamicB), CacheInput: &second}
	if requestA.CacheInput.StablePromptHash != requestB.CacheInput.StablePromptHash {
		t.Fatal("dynamic feedback tail changed stable prefix identity")
	}

	changedPrefix := append([]PromptItem(nil), prefix...)
	changedPrefix[0].Content = `{"contextType":"character","revision":3}`
	changed, err := NewCacheKeyInputWithStablePrefix(PromptLaneSocialFeedback, "model-1", "conversation-1", "stable instructions", changedPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if changed.StablePromptHash == first.StablePromptHash {
		t.Fatal("changed stable context did not change prefix identity")
	}
}

func TestNewCacheKeyInputWithStablePrefixRejectsDynamicItems(t *testing.T) {
	tests := []PromptItem{
		{Type: PromptItemUserMessage, Content: "hello"},
		{Type: PromptItemToolResult, ToolCallID: "call-1", Content: "result"},
		{Type: PromptItemContextData},
		{Type: PromptItemContextData, Content: "text", Parts: &PromptContentParts{{Type: PromptContentImage, ImageDataURL: "data:image/png;base64,AA=="}}},
	}
	for _, item := range tests {
		if _, err := NewCacheKeyInputWithStablePrefix(PromptLaneSocialLearn, "model-1", "conversation-1", "stable instructions", []PromptItem{item}); err == nil {
			t.Fatalf("dynamic item unexpectedly accepted: %#v", item)
		}
	}
}

func TestBuildPromptCacheKeyIsDeterministicAndRevisionScoped(t *testing.T) {
	input := CacheKeyInput{Lane: PromptLaneRespond, Model: "model-1", ConversationID: "conversation-1", CharacterRevision: 2, ProfileRevision: 3, PromptRevision: 4, StablePromptHash: "prompt-a"}
	first, err := BuildPromptCacheKey(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPromptCacheKey(input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == LaneCacheKey(input.ConversationID, input.Lane) {
		t.Fatalf("key = %q, second = %q; expected deterministic v2 key distinct from legacy", first, second)
	}
	input.ProfileRevision++
	third, err := BuildPromptCacheKey(input)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("profile revision did not change cache key")
	}
	input.ProfileRevision--
	input.StablePromptHash = "prompt-b"
	fourth, err := BuildPromptCacheKey(input)
	if err != nil {
		t.Fatal(err)
	}
	if fourth == first {
		t.Fatal("stable prompt hash did not change cache key")
	}
}

func TestBuildPromptCacheKeyRejectsMissingStableIdentity(t *testing.T) {
	_, err := BuildPromptCacheKey(CacheKeyInput{Lane: PromptLaneRespond, Model: "model-1"})
	if err == nil {
		t.Fatal("missing conversation and seed unexpectedly accepted")
	}
}

func TestBuildLegacyLaneCacheKeyPreservesCompatibility(t *testing.T) {
	if got := BuildLegacyLaneCacheKey("c1", PromptLaneCompact); got != "fairy:c1:compact" {
		t.Fatalf("got %q", got)
	}
}
