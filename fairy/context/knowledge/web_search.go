package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"fairy/transport/openserp"
)

const (
	defaultSearchLimit    = 5
	maxSearchTitleRunes   = 300
	maxSearchSnippetRunes = 1200
	maxSearchURLBytes     = 2048
	// The aggregate route uses whichever engines the declared OpenSERP instance
	// has initialized instead of requiring a particular optional engine.
	defaultEngine  = "mega"
	defaultBaseURL = "http://127.0.0.1:7000"
	healthTimeout  = 5 * time.Second
)

var (
	ErrSearchEndpointNotConfigured = errors.New("openserp endpoint is not reachable")
)

type WebSearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type SearchSource struct {
	ID              string
	Title           string
	URL             string
	Snippet         string
	Rank            uint8
	FetchedAtUnixMS int64
}

type SearchBatch struct {
	ID             string
	ConversationID string
	TurnID         string
	ToolCallID     string
	Sources        []SearchSource
}

// Service is an HTTP client for an externally managed OpenSERP instance
// (typically started via docker compose). It never spawns a local binary.
type WebSearchService struct {
	baseURL      string
	authority    *openserp.Authority
	authorityErr error
	logger       *zap.Logger
}

func NewWebSearchService(baseURL string) *WebSearchService {
	baseURL = normalizeBaseURL(baseURL)
	authority, err := openserp.NewAuthority(baseURL)
	return &WebSearchService{
		baseURL:      baseURL,
		authority:    authority,
		authorityErr: err,
		logger:       zap.NewNop(),
	}
}

func NewWebSearchServiceWithAuthority(authority *openserp.Authority) *WebSearchService {
	service := &WebSearchService{authority: authority, logger: zap.NewNop()}
	if authority == nil {
		service.authorityErr = openserp.ErrCapabilityDenied
		return service
	}
	service.baseURL = authority.Origin()
	return service
}

func AttachWebSearchLogger(s *WebSearchService, logger *zap.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}

func (s *WebSearchService) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

func (s *WebSearchService) Available() bool {
	return s != nil && s.authority != nil && s.authorityErr == nil
}

func (s *WebSearchService) Close() error {
	if s != nil && s.authority != nil {
		s.authority.Close()
	}
	return nil
}

func (s *WebSearchService) EnsureReady(ctx context.Context) error {
	if s == nil {
		return ErrSearchEndpointNotConfigured
	}
	if s.authorityErr != nil || s.authority == nil {
		return ErrSearchEndpointNotConfigured
	}
	healthCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	response, err := s.authority.Health(healthCtx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSearchEndpointNotConfigured, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: health status %d", ErrSearchEndpointNotConfigured, response.StatusCode)
	}
	return nil
}

func (s *WebSearchService) Search(ctx context.Context, query string, limit int) ([]WebSearchHit, error) {
	query = trimQuery(query)
	if query == "" {
		return nil, errors.New("web search query is empty")
	}
	if limit <= 0 || limit > defaultSearchLimit {
		limit = defaultSearchLimit
	}
	if err := s.EnsureReady(ctx); err != nil {
		return nil, err
	}
	base := s.baseURL
	s.logger.Info("openserp search start", zap.String("engine", defaultEngine), zap.Int("limit", limit), zap.Int("queryRunes", utf8.RuneCountInString(query)), zap.String("baseURL", base))
	response, err := s.authority.Search(ctx, query, limit)
	if err != nil {
		s.logger.Error("openserp search failed", zap.Error(err))
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err := fmt.Errorf("openserp search status %d: %s", response.StatusCode, truncate(string(response.Body), 200))
		s.logger.Error("openserp search failed", zap.Error(err))
		return nil, err
	}
	hits, err := parseSearchHits(response.Body, limit)
	if err != nil {
		s.logger.Error("openserp search parse failed", zap.Error(err))
		return nil, err
	}
	s.logger.Info("openserp search done", zap.Int("hits", len(hits)))
	return hits, nil
}

func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(raw, "/")
}

func trimQuery(query string) string {
	return strings.TrimSpace(query)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func parseSearchHits(body []byte, limit int) ([]WebSearchHit, error) {
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > len(payload.Results) {
		limit = len(payload.Results)
	}
	hits := make([]WebSearchHit, 0, limit)
	for _, item := range payload.Results[:limit] {
		hits = append(hits, WebSearchHit{Title: item.Title, URL: item.URL, Snippet: item.Snippet})
	}
	return hits, nil
}

var searchHTMLTagPattern = regexp.MustCompile(`<[^>]*>`)

var searchTrackingParameters = map[string]struct{}{
	"dclid":   {},
	"fbclid":  {},
	"gclid":   {},
	"gbraid":  {},
	"mc_cid":  {},
	"mc_eid":  {},
	"msclkid": {},
	"wbraid":  {},
	"yclid":   {},
}

func NewSearchBatch(conversationID, turnID, toolCallID string, hits []WebSearchHit, fetchedAtUnixMS int64) (SearchBatch, error) {
	conversationID = strings.TrimSpace(conversationID)
	turnID = strings.TrimSpace(turnID)
	toolCallID = strings.TrimSpace(toolCallID)
	if conversationID == "" || turnID == "" || toolCallID == "" {
		return SearchBatch{}, errors.New("web search batch identity is required")
	}
	batch := SearchBatch{
		ID:             webSearchBatchID(conversationID, turnID, toolCallID),
		ConversationID: conversationID,
		TurnID:         turnID,
		ToolCallID:     toolCallID,
		Sources:        make([]SearchSource, 0, min(len(hits), defaultSearchLimit)),
	}
	seenURLs := make(map[string]struct{}, defaultSearchLimit)
	seenContents := make(map[string]struct{}, defaultSearchLimit)
	for index, hit := range hits {
		if len(batch.Sources) >= defaultSearchLimit {
			break
		}
		title := cleanWebSearchText(hit.Title)
		snippet := cleanWebSearchText(hit.Snippet)
		canonicalURL, ok := canonicalWebSearchURL(hit.URL)
		if !ok || title == "" && snippet == "" {
			continue
		}
		if utf8.RuneCountInString(title) > maxSearchTitleRunes || utf8.RuneCountInString(snippet) > maxSearchSnippetRunes {
			continue
		}
		if _, exists := seenURLs[canonicalURL]; exists {
			continue
		}
		contentKey := strings.ToLower(title + "\x00" + snippet)
		if _, exists := seenContents[contentKey]; exists {
			continue
		}
		source := SearchSource{
			ID:              webSearchSourceID(canonicalURL, title, snippet),
			Title:           title,
			URL:             canonicalURL,
			Snippet:         snippet,
			Rank:            uint8(index + 1),
			FetchedAtUnixMS: fetchedAtUnixMS,
		}
		seenURLs[canonicalURL] = struct{}{}
		seenContents[contentKey] = struct{}{}
		batch.Sources = append(batch.Sources, source)
	}
	return batch, nil
}

func cleanWebSearchText(value string) string {
	value = html.UnescapeString(strings.TrimSpace(value))
	value = searchHTMLTagPattern.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func canonicalWebSearchURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxSearchURLBytes {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") {
			query.Del(key)
			continue
		}
		if _, drop := searchTrackingParameters[lower]; drop {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), true
}

func webSearchBatchID(conversationID, turnID, toolCallID string) string {
	sum := sha256.Sum256([]byte(conversationID + "\x00" + turnID + "\x00" + toolCallID))
	return fmt.Sprintf("web-batch-%x", sum[:12])
}

func webSearchSourceID(canonicalURL, title, snippet string) string {
	sum := sha256.Sum256([]byte(canonicalURL + "\x00" + title + "\x00" + snippet))
	return fmt.Sprintf("web-source-%x", sum[:12])
}

func webSearchSourceJobID(batchID, sourceID string) string {
	sum := sha256.Sum256([]byte(batchID + "\x00" + sourceID))
	return fmt.Sprintf("web-job-%x", sum[:12])
}
