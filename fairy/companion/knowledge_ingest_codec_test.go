package companion

import (
	"strings"
	"testing"

	"fairy/memory"
)

func knowledgeIngestCodecBatch(t *testing.T) memory.KnowledgeIngestBatch {
	t.Helper()
	batch, err := newWebSearchBatch(
		"conversation", "turn", "call", "anime",
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

func TestBuildKnowledgeIngestInputContainsOnlyCurrentBatch(t *testing.T) {
	batch := knowledgeIngestCodecBatch(t)
	items, err := buildKnowledgeIngestInput(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	content := items[0].Content
	for _, expected := range []string{batch.ID, batch.Category, batch.Sources[0].ID, batch.Sources[1].ID, "https://one.example/item"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("input missing %q: %s", expected, content)
		}
	}
	for _, forbidden := range []string{"conversation", `"turn"`, `"call"`} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("input leaked authority field %q: %s", forbidden, content)
		}
	}
}

func TestParseKnowledgeIngestOutputAcceptsGroundedFactsAndEmptyBatch(t *testing.T) {
	batch := knowledgeIngestCodecBatch(t)
	raw := `{"facts":[{"topic":"作品主题","statement":"这是一条由两个公开来源共同支持的完整稳定事实。","confidenceBasisPoints":8000,"sourceHitIDs":["` +
		batch.Sources[0].ID + `","` + batch.Sources[1].ID + `"]}]}`
	output, err := parseKnowledgeIngestOutput(raw, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Facts) != 1 || len(output.Facts[0].SourceHitIDs) != 2 {
		t.Fatalf("output = %#v", output)
	}
	empty, err := parseKnowledgeIngestOutput(`{"facts":[]}`, batch)
	if err != nil || len(empty.Facts) != 0 {
		t.Fatalf("empty output = (%#v, %v)", empty, err)
	}
}

func TestParseKnowledgeIngestOutputRejectsUnknownSourcesAndNonStrictJSON(t *testing.T) {
	batch := knowledgeIngestCodecBatch(t)
	validFact := `{"topic":"作品主题","statement":"这是一条长度足够并且完整的稳定事实。","confidenceBasisPoints":8000,"sourceHitIDs":["` + batch.Sources[0].ID + `"]}`
	for name, raw := range map[string]string{
		"unknown source": `{"facts":[{"topic":"作品主题","statement":"这是一条长度足够并且完整的稳定事实。","confidenceBasisPoints":8000,"sourceHitIDs":["other-batch-source"]}]}`,
		"unknown field":  `{"facts":[` + validFact + `],"reason":"hidden"}`,
		"trailing text":  `{"facts":[` + validFact + `]} explanation`,
		"markdown":       "```json\n{\"facts\":[]}\n```",
		"nil facts":      `{}`,
		"duplicate IDs":  `{"facts":[{"topic":"作品主题","statement":"这是一条长度足够并且完整的稳定事实。","confidenceBasisPoints":8000,"sourceHitIDs":["` + batch.Sources[0].ID + `","` + batch.Sources[0].ID + `"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseKnowledgeIngestOutput(raw, batch); err == nil {
				t.Fatalf("expected rejection for %s", raw)
			}
		})
	}
}
