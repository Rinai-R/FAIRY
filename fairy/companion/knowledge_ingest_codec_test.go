package companion

import (
	"strings"
	"testing"

	"fairy/memory"
)

func knowledgeIngestCodecBatch(t *testing.T) memory.KnowledgeIngestBatch {
	t.Helper()
	batch, err := newWebSearchBatch(
		"conversation", "turn", "call",
		[]WebSearchHit{
			{Title: "来源一", URL: "https://one.example/item", Snippet: "这是第一条足够完整的公开来源摘要。"},
			{Title: "来源二", URL: "https://two.example/item", Snippet: "这是第二条足够完整的公开来源摘要。"},
		},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return memoryKnowledgeIngestBatch(batch)
}

func knowledgeIngestCodecDocuments(t *testing.T, batch memory.KnowledgeIngestBatch) []memory.KnowledgeDocument {
	t.Helper()
	documents, err := (testKnowledgeDocumentFetcher{}).FetchBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	return documents
}

func TestBuildKnowledgeIngestInputContainsOnlyCurrentBatch(t *testing.T) {
	batch := knowledgeIngestCodecBatch(t)
	documents := knowledgeIngestCodecDocuments(t, batch)
	items, err := buildKnowledgeIngestInput(batch, documents)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	content := items[0].Content
	for _, expected := range []string{batch.ID, documents[0].Chunks[0].ID, documents[1].Chunks[0].ID, "https://one.example/item"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("input missing %q: %s", expected, content)
		}
	}
	for _, forbidden := range []string{"conversation", `"turn"`, `"call"`, `"snippet"`} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("input leaked authority/fallback field %q: %s", forbidden, content)
		}
	}
}

func TestParseKnowledgeIngestOutputAcceptsGroundedFactsAndEmptyBatch(t *testing.T) {
	batch := knowledgeIngestCodecBatch(t)
	documents := knowledgeIngestCodecDocuments(t, batch)
	raw := `{"facts":[{"subject":"作品主题","predicate":"公开状态","value":"已公布","statement":"这是一条由两个公开来源共同支持的完整稳定事实。","confidenceBasisPoints":8000,"evidenceChunkIDs":["` +
		documents[0].Chunks[0].ID + `","` + documents[1].Chunks[0].ID + `"]}]}`
	output, err := parseKnowledgeIngestOutput(raw, documents)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Facts) != 1 || len(output.Facts[0].EvidenceChunkIDs) != 2 {
		t.Fatalf("output = %#v", output)
	}
	empty, err := parseKnowledgeIngestOutput(`{"facts":[]}`, documents)
	if err != nil || len(empty.Facts) != 0 {
		t.Fatalf("empty output = (%#v, %v)", empty, err)
	}
}

func TestParseKnowledgeIngestOutputRejectsUnknownChunksAndNonStrictJSON(t *testing.T) {
	batch := knowledgeIngestCodecBatch(t)
	documents := knowledgeIngestCodecDocuments(t, batch)
	validFact := `{"subject":"作品主题","predicate":"状态","value":"已公布","statement":"这是一条长度足够并且完整的稳定事实。","confidenceBasisPoints":8000,"evidenceChunkIDs":["` + documents[0].Chunks[0].ID + `"]}`
	for name, raw := range map[string]string{
		"unknown chunk": `{"facts":[{"subject":"作品主题","predicate":"状态","value":"已公布","statement":"这是一条长度足够并且完整的稳定事实。","confidenceBasisPoints":8000,"evidenceChunkIDs":["other-batch-chunk"]}]}`,
		"unknown field": `{"facts":[` + validFact + `],"reason":"hidden"}`,
		"trailing text": `{"facts":[` + validFact + `]} explanation`,
		"markdown":      "```json\n{\"facts\":[]}\n```",
		"nil facts":     `{}`,
		"duplicate IDs": `{"facts":[{"subject":"作品主题","predicate":"状态","value":"已公布","statement":"这是一条长度足够并且完整的稳定事实。","confidenceBasisPoints":8000,"evidenceChunkIDs":["` + documents[0].Chunks[0].ID + `","` + documents[0].Chunks[0].ID + `"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseKnowledgeIngestOutput(raw, documents); err == nil {
				t.Fatalf("expected rejection for %s", raw)
			}
		})
	}
}
