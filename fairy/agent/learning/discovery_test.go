package learning

import (
	"encoding/json"
	"testing"

	"fairy/context/character"
	"fairy/context/knowledge"
	"fairy/context/memory/extraction"
	"fairy/runtime/model"
)

func TestPrivateDiscoveryEnvelopePreservesEvidenceAndCapabilities(t *testing.T) {
	envelope := privateDiscoveryEnvelope(extraction.BatchInput{
		ConversationID: "conversation-1", CharacterID: "character-1",
		Turns: []extraction.Turn{{TurnID: "turn-1", UserMessage: "我长期喜欢爵士乐", AssistantMessage: "我记住了"}},
	})
	if len(envelope.AllowedSpaces) != 2 || envelope.AllowedSpaces[0] != discoveryPersonal || envelope.AllowedSpaces[1] != discoveryIgnore {
		t.Fatalf("unexpected capabilities: %#v", envelope.AllowedSpaces)
	}
	if len(envelope.Evidence) != 2 || envelope.Evidence[0].Content != "我长期喜欢爵士乐" || envelope.Evidence[1].Role != "assistant" {
		t.Fatalf("unexpected evidence: %#v", envelope.Evidence)
	}
	items, err := buildDiscoveryInput(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(items[0].Content), &payload); err != nil || len(payload) != 1 {
		t.Fatalf("unexpected payload: %v %#v", err, payload)
	}
}

func TestKnowledgeDiscoveryEnvelopeUsesCompleteDocumentAndStableCacheIdentity(t *testing.T) {
	task := knowledge.IngestTask{ConversationID: "conversation-1", Source: knowledge.IngestSource{ID: "source-1"}}
	first := knowledgeDiscoveryEnvelope(task, knowledge.Document{SourceID: "source-1", Content: "完整文档第一版"})
	second := knowledgeDiscoveryEnvelope(task, knowledge.Document{SourceID: "source-1", Content: "完全不同的动态文档第二版"})
	if len(first.Evidence) != 1 || first.Evidence[0].Content != "完整文档第一版" || first.AllowedSpaces[0] != discoveryKnowledge {
		t.Fatalf("unexpected knowledge discovery envelope: %#v", first)
	}
	firstCache := model.NewCacheKeyInput(model.PromptLaneLearningDiscovery, "model-1", task.ConversationID, character.LearningDiscoveryInstructions)
	secondCache := model.NewCacheKeyInput(model.PromptLaneLearningDiscovery, "model-1", task.ConversationID, character.LearningDiscoveryInstructions)
	if firstCache.StablePromptHash != secondCache.StablePromptHash {
		t.Fatal("dynamic evidence changed stable discovery cache identity")
	}
	firstItems, err := buildDiscoveryInput(first)
	if err != nil {
		t.Fatal(err)
	}
	secondItems, err := buildDiscoveryInput(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstItems[0].Content == secondItems[0].Content {
		t.Fatal("dynamic documents were not preserved in request input")
	}
}

func TestParseDiscoveryOutputRejectsCrossSpaceAndUntrustedEvidence(t *testing.T) {
	envelope := privateDiscoveryEnvelope(extraction.BatchInput{Turns: []extraction.Turn{{TurnID: "turn-1", UserMessage: "喜欢爵士乐", AssistantMessage: "好的"}}})
	for _, raw := range []string{
		`{"candidates":[{"space":"knowledge","statement":"爵士乐很好","query":"爵士乐","evidenceRefs":["turn-1"]}]}`,
		`{"candidates":[{"space":"personal","statement":"用户喜欢爵士乐","query":"用户音乐偏好","evidenceRefs":["missing"]}]}`,
		`{"candidates":[{"space":"personal","statement":"用户喜欢爵士乐","query":"用户音乐偏好","evidenceRefs":["turn-1:assistant"]}]}`,
		`{"candidates":[{"space":"personal","statement":"用户喜欢爵士乐","query":"用户音乐偏好","evidenceRefs":["turn-1"]}],"extra":true}`,
	} {
		if _, err := parseDiscoveryOutput(raw, envelope); err == nil {
			t.Fatalf("accepted invalid discovery output: %s", raw)
		}
	}
}
