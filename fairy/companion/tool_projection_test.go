package companion

import (
	"errors"
	"testing"

	"fairy/memory"
)

func TestMergeRetrievalContextDeduplicatesAndPreservesPriority(t *testing.T) {
	base := memory.RetrievalContext{
		PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "personal-1"}},
		Knowledge:        []memory.RetrievedKnowledge{{ID: "knowledge-1"}},
		SocialMemories:   memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{ID: "social-1"}}},
		SemanticStatus:   "ready",
	}
	extra := memory.RetrievalContext{
		PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "personal-1"}, {ID: "personal-2"}},
		Knowledge:        []memory.RetrievedKnowledge{{ID: "knowledge-1"}, {ID: "knowledge-2"}},
		SocialMemories:   memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{ID: "social-1"}, {ID: "social-2"}}},
		SemanticStatus:   "used",
	}
	merged := mergeRetrievalContext(base, extra)
	if len(merged.PersonalMemories) != 2 || len(merged.Knowledge) != 2 || len(merged.SocialMemories.Entries) != 2 {
		t.Fatalf("merged context = %#v", merged)
	}
	if merged.SemanticStatus != "used" {
		t.Fatalf("semantic status = %q", merged.SemanticStatus)
	}
}

func TestBoundSocialToolRetrievalKeepsFeedbackCoverageWithinLimit(t *testing.T) {
	base := memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "base-1"}, {ID: "base-2"},
	}}
	extra := memory.RetrievalContext{
		SocialMemories: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
			{ID: "base-2"}, {ID: "tool-1"}, {ID: "tool-2"},
		}},
		Knowledge: []memory.RetrievedKnowledge{
			{ID: "base-2"}, {ID: "tool-1"}, {ID: "tool-2"},
		},
	}

	got := boundSocialToolRetrieval(base, extra, 3)
	if len(got.SocialMemories.Entries) != 2 || got.SocialMemories.Entries[0].ID != "base-2" || got.SocialMemories.Entries[1].ID != "tool-1" {
		t.Fatalf("bounded social entries = %#v", got.SocialMemories.Entries)
	}
	if len(got.Knowledge) != 2 || got.Knowledge[0].ID != "base-2" || got.Knowledge[1].ID != "tool-1" {
		t.Fatalf("bounded social knowledge = %#v", got.Knowledge)
	}
	merged := mergeSocialMemory(base, got.SocialMemories)
	if len(merged.Entries) != 3 {
		t.Fatalf("merged feedback entries = %#v", merged.Entries)
	}
}

func TestWebAndErrorProjectionPreserveExistingShape(t *testing.T) {
	batch, err := newWebSearchBatch("conversation", "turn", "call", []WebSearchHit{{Title: "Title", URL: "https://example.com", Snippet: "Snippet"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	web := retrievalFromWebSearchBatch(batch)
	if len(web.Knowledge) != 1 || web.Knowledge[0].ID != batch.Sources[0].ID || web.Knowledge[0].Statement != "Title — Snippet" || len(web.Knowledge[0].Sources) != 1 || web.Knowledge[0].Sources[0].FetchedAtUnixMS != 1 {
		t.Fatalf("web projection = %#v", web)
	}
	failure := retrievalFromToolError(toolMemorySearch, errors.New("unavailable"))
	if len(failure.Knowledge) != 1 || failure.Knowledge[0].ID != "tool-error-memory_search" || failure.Knowledge[0].Statement != "memory_search failed: unavailable" {
		t.Fatalf("error projection = %#v", failure)
	}
}
