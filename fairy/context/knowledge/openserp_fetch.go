package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fairy/transport/openserp"
)

// FetchSource delegates public-page network access to OpenSERP. It never dials
// the source URL from the FAIRY process.
func (s *WebSearchService) FetchSource(ctx context.Context, source IngestSource) (Document, error) {
	if s == nil || s.authority == nil || s.authorityErr != nil {
		return Document{}, ErrPluginCapabilityUnavailable
	}
	if err := validateOpenSERPIngestSource(source); err != nil {
		return Document{}, err
	}
	fetchedAt := time.Now().UnixMilli()
	response, err := s.authority.Extract(ctx, source.URL)
	if err != nil {
		if errors.Is(err, openserp.ErrCapabilityDenied) {
			return Document{}, fmt.Errorf("%w: extraction target rejected", ErrFetchRejected)
		}
		return Document{}, fmt.Errorf("%w: %v", ErrFetchTransient, err)
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		return Document{}, fmt.Errorf("%w: OpenSERP status %d", ErrFetchTransient, response.StatusCode)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Document{}, fmt.Errorf("%w: OpenSERP status %d", ErrFetchRejected, response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.ContentType)
	if err != nil || mediaType != "text/plain" {
		return Document{}, fmt.Errorf("%w: OpenSERP extraction content type is unsupported", ErrFetchRejected)
	}
	content := strings.TrimSpace(string(response.Body))
	if content == "" || ContainsDisallowedControl(content) {
		return Document{}, fmt.Errorf("%w: OpenSERP extraction content is invalid", ErrFetchRejected)
	}
	contentSum := sha256.Sum256([]byte(content))
	contentHash := fmt.Sprintf("%x", contentSum[:])
	evidenceSum := sha256.Sum256([]byte(source.URL + "\x00" + contentHash))
	return Document{
		SourceID: source.ID, CanonicalURL: source.URL, Title: source.Title,
		Content: content, ContentHash: contentHash,
		EvidenceID:      fmt.Sprintf("web-evidence-%x", evidenceSum[:12]),
		ContentType:     "text/plain",
		FetchedAtUnixMS: fetchedAt,
	}, nil
}

func validateOpenSERPIngestSource(source IngestSource) error {
	if err := ValidateID("knowledge_source_id", source.ID); err != nil {
		return fmt.Errorf("%w: invalid source identity", ErrFetchRejected)
	}
	target := strings.TrimSpace(source.URL)
	parsed, err := url.ParseRequestURI(target)
	if err != nil || parsed == nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" ||
		parsed.String() != target || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: invalid URL", ErrFetchRejected)
	}
	if source.Title == "" || strings.TrimSpace(source.Title) != source.Title || ContainsDisallowedControl(source.Title) {
		return fmt.Errorf("%w: invalid source title", ErrFetchRejected)
	}
	return nil
}
