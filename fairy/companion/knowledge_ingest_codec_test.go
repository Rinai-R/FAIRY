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
	return memoryKnowledgeIngestBatches(batch)[0]
}

func knowledgeIngestCodecDocuments(t *testing.T, batch memory.KnowledgeIngestBatch) []memory.KnowledgeDocument {
	t.Helper()
	document, err := (testKnowledgeDocumentFetcher{}).FetchSource(t.Context(), batch.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	return []memory.KnowledgeDocument{document}
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
	for _, expected := range []string{batch.ID, documents[0].Chunks[0].ID, "https://one.example/item"} {
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
	raw := `{"facts":[{"subject":"作品主题","predicate":"公开状态","value":"已公布","statement":"这是一条由当前公开来源直接支持的完整稳定事实。","confidenceBasisPoints":8000,"evidenceChunkIDs":["` +
		documents[0].Chunks[0].ID + `"]}]}`
	output, err := parseKnowledgeIngestOutput(raw, documents)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Facts) != 1 || len(output.Facts[0].EvidenceChunkIDs) != 1 {
		t.Fatalf("output = %#v", output)
	}
	empty, err := parseKnowledgeIngestOutput(`{"facts":[]}`, documents)
	if err != nil || len(empty.Facts) != 0 {
		t.Fatalf("empty output = (%#v, %v)", empty, err)
	}
}

func TestKnowledgeReconcileCodecMapsOnlySuppliedPerFactAliases(t *testing.T) {
	facts := []memory.KnowledgeIngestFact{
		{Subject: "作品甲", Predicate: "状态", Value: "公测", Statement: "作品甲当前已经进入公开测试阶段。", ConfidenceBasisPoints: 8000, EvidenceChunkIDs: []string{"chunk-a"}},
		{Subject: "作品乙", Predicate: "状态", Value: "发布", Statement: "作品乙已经正式公开发布新版本。", ConfidenceBasisPoints: 8100, EvidenceChunkIDs: []string{"chunk-b"}},
	}
	recalls := []memory.KnowledgeIngestRecall{
		{FactIndex: 0, Candidates: []memory.RetrievedKnowledge{{ID: "knowledge-a", Statement: "作品甲此前处于内部测试阶段。", ConfidenceBasisPoints: 7000}}},
		{FactIndex: 1, Candidates: []memory.RetrievedKnowledge{{ID: "knowledge-b", Statement: "作品乙已经正式公开发布新版本。", ConfidenceBasisPoints: 8000}}},
	}
	items, aliases, err := buildKnowledgeReconcileInput("batch-a", facts, recalls)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || strings.Contains(items[0].Content, "knowledge-a") || !strings.Contains(items[0].Content, "f0m0") {
		t.Fatalf("reconcile input = %#v", items)
	}
	mutations, err := parseKnowledgeReconcileOutput(
		`{"mutations":[{"factIndex":0,"operation":"UPDATE","memoryId":"f0m0"},{"factIndex":1,"operation":"NONE","memoryId":"f1m0"}]}`,
		facts,
		aliases,
	)
	if err != nil {
		t.Fatal(err)
	}
	if mutations[0].MemoryID != "knowledge-a" || mutations[1].MemoryID != "knowledge-b" {
		t.Fatalf("mutations = %#v", mutations)
	}
}

func TestKnowledgeReconcileCodecRejectsInvalidMutationAuthority(t *testing.T) {
	facts := []memory.KnowledgeIngestFact{
		{Statement: "作品甲当前已经进入公开测试阶段。"},
		{Statement: "作品乙已经正式公开发布新版本。"},
	}
	aliases := []map[string]string{{"f0m0": "knowledge-a"}, {"f1m0": "knowledge-b"}}
	for name, raw := range map[string]string{
		"missing fact":   `{"mutations":[{"factIndex":0,"operation":"ADD"}]}`,
		"duplicate fact": `{"mutations":[{"factIndex":0,"operation":"ADD"},{"factIndex":0,"operation":"ADD"}]}`,
		"cross fact":     `{"mutations":[{"factIndex":0,"operation":"UPDATE","memoryId":"f1m0"},{"factIndex":1,"operation":"ADD"}]}`,
		"add with id":    `{"mutations":[{"factIndex":0,"operation":"ADD","memoryId":"f0m0"},{"factIndex":1,"operation":"ADD"}]}`,
		"unknown op":     `{"mutations":[{"factIndex":0,"operation":"UPSERT"},{"factIndex":1,"operation":"ADD"}]}`,
		"unknown field":  `{"mutations":[{"factIndex":0,"operation":"ADD","reason":"x"},{"factIndex":1,"operation":"ADD"}]}`,
		"trailing":       `{"mutations":[{"factIndex":0,"operation":"ADD"},{"factIndex":1,"operation":"ADD"}]} nope`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseKnowledgeReconcileOutput(raw, facts, aliases); err == nil {
				t.Fatalf("expected rejection for %s", raw)
			}
		})
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
