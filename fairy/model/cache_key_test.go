package model

import "testing"

func TestBuildPromptCacheKeyIsDeterministicAndRevisionScoped(t *testing.T) {
	input := CacheKeyInput{Lane: PromptLaneRespond, Model: "model-1", ConversationID: "conversation-1", CharacterRevision: 2, ProfileRevision: 3, PromptRevision: 4}
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
