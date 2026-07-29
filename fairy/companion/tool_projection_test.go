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
