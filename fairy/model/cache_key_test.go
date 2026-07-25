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
