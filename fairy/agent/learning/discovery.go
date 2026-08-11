package learning

import (
	"fairy/context/knowledge"
	discoveryctx "fairy/context/learning/discovery"
	"fairy/context/memory/extraction"
	"fairy/runtime/model"
)

type discoverySpace = discoveryctx.Space
type discoveryEvidence = discoveryctx.Evidence
type discoveryEnvelope = discoveryctx.Envelope
type discoveryCandidate = discoveryctx.Candidate
type discoveryOutput = discoveryctx.Output

const (
	discoveryPersonal  = discoveryctx.Personal
	discoveryKnowledge = discoveryctx.Knowledge
	discoverySocial    = discoveryctx.Social
	discoveryIgnore    = discoveryctx.Ignore
)

func privateDiscoveryEnvelope(batch extraction.BatchInput) discoveryEnvelope {
	evidence := make([]discoveryEvidence, 0, len(batch.Turns)*2)
	for _, turn := range batch.Turns {
		evidence = append(evidence,
			discoveryEvidence{Ref: turn.TurnID, Role: "user", Content: turn.UserMessage},
			discoveryEvidence{Ref: turn.TurnID + ":assistant", Role: "assistant", Content: turn.AssistantMessage},
		)
	}
	return discoveryEnvelope{
		Type: "private_conversation", ConversationID: batch.ConversationID, CharacterID: batch.CharacterID,
		AllowedSpaces: []discoverySpace{discoveryPersonal, discoveryIgnore}, Evidence: evidence,
	}
}

func knowledgeDiscoveryEnvelope(task knowledge.IngestTask, document knowledge.Document) discoveryEnvelope {
	return discoveryEnvelope{
		Type: "public_document", ConversationID: task.ConversationID,
		AllowedSpaces: []discoverySpace{discoveryKnowledge, discoveryIgnore},
		Evidence:      []discoveryEvidence{{Ref: document.SourceID, Role: "source", Content: document.Content}},
	}
}

func buildDiscoveryInput(envelope discoveryEnvelope) ([]model.PromptItem, error) {
	return discoveryctx.BuildInput(envelope)
}

func parseDiscoveryOutput(raw string, envelope discoveryEnvelope) (discoveryOutput, error) {
	return discoveryctx.ParseOutput(raw, envelope)
}

func personalDiscoveryCandidates(output discoveryOutput) []discoveryCandidate {
	result := make([]discoveryCandidate, 0, len(output.Candidates))
	for _, candidate := range output.Candidates {
		if candidate.Space == discoveryPersonal {
			result = append(result, candidate)
		}
	}
	return result
}

func knowledgeDiscoveryCandidates(output discoveryOutput) []knowledge.LearningCandidate {
	result := make([]knowledge.LearningCandidate, 0, len(output.Candidates))
	for _, candidate := range output.Candidates {
		if candidate.Space == discoveryKnowledge {
			result = append(result, knowledge.LearningCandidate{Statement: candidate.Statement, Query: candidate.Query})
		}
	}
	return result
}
