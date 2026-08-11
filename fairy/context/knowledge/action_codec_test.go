package knowledge

import (
	"strings"
	"testing"

	"fairy/runtime/model"
)

func knowledgeAgentCodecTask(t *testing.T) IngestTask {
	t.Helper()
	searchBatch, err := NewSearchBatch(
		"conversation", "turn", "call",
		[]WebSearchHit{{Title: "来源一", URL: "https://one.example/item", Snippet: "这是足够完整的公开来源摘要。"}},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return IngestTasks(searchBatch)[0]
}

func knowledgeAgentCodecDocument(task IngestTask) Document {
	return Document{
		SourceID: task.Source.ID, CanonicalURL: task.Source.URL,
		Title:       "来源一",
		Content:     "项目过去处于内部测试阶段。\n项目现在已经进入公开测试阶段。\n官方同时明确撤回了旧发布日期。",
		ContentHash: strings.Repeat("a", 64), EvidenceID: "web-evidence-a",
		ContentType: "text/html", FetchedAtUnixMS: 1,
	}
}

func TestBuildKnowledgeAgentInputContainsCompleteCurrentDocument(t *testing.T) {
	task := knowledgeAgentCodecTask(t)
	document := knowledgeAgentCodecDocument(task)
	items, err := BuildAgentInput(task, document, []LearningCandidate{{Statement: "项目发布了稳定版本", Query: "项目当前发布状态"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	for _, expected := range []string{`"taskId":"` + task.ID + `"`, document.CanonicalURL, "官方同时明确撤回了旧发布日期"} {
		if !strings.Contains(items[0].Content, expected) {
			t.Fatalf("input missing %q: %s", expected, items[0].Content)
		}
	}
	for _, forbidden := range []string{`"batchId"`, `"conversationId"`, `"turnId"`, `"snippet"`, `"chunks"`, `"documents"`} {
		if strings.Contains(items[0].Content, forbidden) {
			t.Fatalf("input leaked forbidden field %q: %s", forbidden, items[0].Content)
		}
	}
}

func TestKnowledgeReconcilerRevisionTracksOnlyFixedContract(t *testing.T) {
	const legacyContractRevision = "whole-document-actions-v1"
	if AgentContractRevision != "whole-document-task-actions-v3" {
		t.Fatalf("current knowledge agent contract revision = %q, want v3", AgentContractRevision)
	}
	first := ReconcilerRevision("fixed instructions", AgentContractRevision)
	repeated := ReconcilerRevision("fixed instructions", AgentContractRevision)
	changedInstructions := ReconcilerRevision("changed instructions", AgentContractRevision)
	legacy := ReconcilerRevision("fixed instructions", legacyContractRevision)
	if len(first) != 64 || first != repeated ||
		first == changedInstructions || first == legacy {
		t.Fatalf(
			"revisions first=%q repeated=%q instructions=%q legacy=%q",
			first,
			repeated,
			changedInstructions,
			legacy,
		)
	}
}

func TestKnowledgeSearchCodecUsesStableRequestAliases(t *testing.T) {
	query, err := ParseSearchArguments(`{"query":"项目当前公开测试阶段"}`)
	if err != nil || query != "项目当前公开测试阶段" {
		t.Fatalf("query = (%q, %v)", query, err)
	}
	aliases := NewAliasSet()
	call := model.FunctionCall{
		CallID: "call-1", Name: SearchToolName,
		Arguments: `{"query":"项目当前公开测试阶段"}`,
	}
	candidate := Retrieved{
		ID: "knowledge-real-id", Topic: "项目状态",
		Statement: "项目过去处于内部测试阶段。", ConfidenceBasisPoints: 8000,
	}
	items, err := BuildSearchToolItems(call, []Retrieved{candidate}, aliases)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].Parts == nil ||
		strings.Contains((*items[1].Parts)[0].Text, candidate.ID) ||
		!strings.Contains((*items[1].Parts)[0].Text, `"id":"k0"`) {
		t.Fatalf("tool items = %#v", items)
	}
	repeated, err := BuildSearchToolItems(
		model.FunctionCall{CallID: "call-2", Name: SearchToolName, Arguments: call.Arguments},
		[]Retrieved{candidate},
		aliases,
	)
	if err != nil || repeated[1].Parts == nil || !strings.Contains((*repeated[1].Parts)[0].Text, `"id":"k0"`) {
		t.Fatalf("repeated alias = (%#v, %v)", repeated, err)
	}
	if realID, ok := aliases.realID("k0"); !ok || realID != candidate.ID {
		t.Fatalf("alias resolution = (%q, %v)", realID, ok)
	}
}

func TestParseKnowledgeAgentOutputAcceptsGroundedActions(t *testing.T) {
	task := knowledgeAgentCodecTask(t)
	document := knowledgeAgentCodecDocument(task)
	aliases := NewAliasSet()
	if _, err := aliases.aliasFor("knowledge-update"); err != nil {
		t.Fatal(err)
	}
	if _, err := aliases.aliasFor("knowledge-delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := aliases.aliasFor("knowledge-none"); err != nil {
		t.Fatal(err)
	}
	raw := `{"actions":[` +
		`{"operation":"ADD","content":"项目新增了一条由完整来源支持的稳定公开知识。","confidenceBasisPoints":8200,"evidence":"项目现在已经进入公开测试阶段。"},` +
		`{"operation":"REPLACE","memoryId":"k0","content":"项目现在已经进入公开测试阶段，过去的内部测试状态已经失效。","confidenceBasisPoints":9000,"evidence":"项目现在已经进入公开测试阶段。"},` +
		`{"operation":"DELETE","memoryId":"k1","evidence":"官方同时明确撤回了旧发布日期。"},` +
		`{"operation":"NONE","memoryId":"k2","evidence":"项目过去处于内部测试阶段。"}` +
		`]}`
	actions, err := ParseAgentOutput(raw, document, aliases)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 4 || actions[1].MemoryID != "knowledge-update" ||
		actions[2].MemoryID != "knowledge-delete" || actions[3].MemoryID != "knowledge-none" {
		t.Fatalf("actions = %#v", actions)
	}
	empty, err := ParseAgentOutput(`{"actions":[]}`, document, aliases)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty actions = (%#v, %v)", empty, err)
	}
}

func TestKnowledgeAgentCodecRejectsInvalidAuthorityAndEvidence(t *testing.T) {
	document := knowledgeAgentCodecDocument(knowledgeAgentCodecTask(t))
	aliases := NewAliasSet()
	if _, err := aliases.aliasFor("knowledge-a"); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"unknown alias":        `{"actions":[{"operation":"REPLACE","memoryId":"k9","content":"项目现在已经进入公开测试阶段。","confidenceBasisPoints":8000,"evidence":"项目现在已经进入公开测试阶段。"}]}`,
		"fake evidence":        `{"actions":[{"operation":"ADD","content":"项目现在已经进入公开测试阶段。","confidenceBasisPoints":8000,"evidence":"正文里不存在的证据"}]}`,
		"add with id":          `{"actions":[{"operation":"ADD","memoryId":"k0","content":"项目现在已经进入公开测试阶段。","confidenceBasisPoints":8000,"evidence":"项目现在已经进入公开测试阶段。"}]}`,
		"add with empty id":    `{"actions":[{"operation":"ADD","memoryId":"","content":"项目现在已经进入公开测试阶段。","confidenceBasisPoints":8000,"evidence":"项目现在已经进入公开测试阶段。"}]}`,
		"delete content":       `{"actions":[{"operation":"DELETE","memoryId":"k0","content":"不允许","evidence":"官方同时明确撤回了旧发布日期。"}]}`,
		"delete empty content": `{"actions":[{"operation":"DELETE","memoryId":"k0","content":"","evidence":"官方同时明确撤回了旧发布日期。"}]}`,
		"duplicate target":     `{"actions":[{"operation":"NONE","memoryId":"k0","evidence":"项目过去处于内部测试阶段。"},{"operation":"DELETE","memoryId":"k0","evidence":"官方同时明确撤回了旧发布日期。"}]}`,
		"duplicate field":      `{"actions":[],"actions":[]}`,
		"unknown field":        `{"actions":[{"operation":"NONE","memoryId":"k0","evidence":"项目过去处于内部测试阶段。","reason":"x"}]}`,
		"trailing":             `{"actions":[]} nope`,
		"missing actions":      `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAgentOutput(raw, document, aliases); err == nil {
				t.Fatalf("expected rejection for %s", raw)
			}
		})
	}
	for name, raw := range map[string]string{
		"short":     `{"query":"短"}`,
		"space":     `{"query":" 项目当前公开测试阶段"}`,
		"unknown":   `{"query":"项目当前公开测试阶段","scope":"personal"}`,
		"duplicate": `{"query":"项目当前公开测试阶段","query":"项目当前公开测试阶段"}`,
		"trailing":  `{"query":"项目当前公开测试阶段"} nope`,
	} {
		t.Run("query_"+name, func(t *testing.T) {
			if _, err := ParseSearchArguments(raw); err == nil {
				t.Fatalf("expected rejection for %s", raw)
			}
		})
	}
	controlDocument := document
	controlDocument.Content += "\n项目证据包含\u0000禁止控制符。"
	if _, err := ParseAgentOutput(
		`{"actions":[{"operation":"ADD","content":"项目现在已经进入公开测试阶段。","confidenceBasisPoints":8000,"evidence":"项目证据包含\u0000禁止控制符。"}]}`,
		controlDocument,
		aliases,
	); err == nil {
		t.Fatal("control-character evidence was accepted")
	}
}

func TestKnowledgeAgentBudgetRejectsCompleteDocumentWithoutTruncating(t *testing.T) {
	document := knowledgeAgentCodecDocument(knowledgeAgentCodecTask(t))
	document.Content = strings.Repeat("完整正文", 5000)
	if err := ValidateInitialAgentBudget(document, 4096, 1600); err == nil {
		t.Fatal("validateInitialKnowledgeAgentBudget() error = nil")
	}
	if len(document.Content) != len(strings.Repeat("完整正文", 5000)) {
		t.Fatal("budget validation mutated the complete document")
	}
}
