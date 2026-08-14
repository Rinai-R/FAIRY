package knowledge

import (
	"errors"
	"fairy/runtime/embedding"
	"net/url"
	"strings"
)

const (
	maxKnowledgeDocumentActions            = 8
	maxKnowledgeDocumentActionContentRunes = 2400
)

type preparedKnowledgeDocumentAction struct {
	action    DocumentAction
	topic     string
	embedding embedding.EmbeddingValue
}

func validateKnowledgeDocument(task IngestTask, document Document) error {
	source := task.Source
	if document.SourceID != source.ID || document.CanonicalURL != source.URL {
		return errors.New("knowledge document does not match the task source")
	}
	parsed, err := url.Parse(document.CanonicalURL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" ||
		parsed.String() != document.CanonicalURL ||
		parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("knowledge document URL is invalid")
	}
	if !validContentHash(document.ContentHash) ||
		document.ReconcilerRevision != "" && !validContentHash(document.ReconcilerRevision) ||
		document.ContentType != "text/html" && document.ContentType != "text/plain" ||
		document.FetchedAtUnixMS < 0 {
		return errors.New("knowledge document metadata is invalid")
	}
	if strings.TrimSpace(document.Content) != document.Content ||
		document.Content == "" ||
		ContainsDisallowedControl(document.Content) ||
		embedding.ContentHash(document.Content) != document.ContentHash {
		return errors.New("knowledge document complete content is invalid")
	}
	return ValidateID("knowledge_evidence_id", document.EvidenceID)
}
