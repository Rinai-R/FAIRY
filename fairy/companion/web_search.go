package companion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	defaultSearchLimit    = 5
	maxSearchTitleRunes   = 300
	maxSearchSnippetRunes = 1200
	maxSearchURLBytes     = 2048
	// OpenSERP HTTP path segment is "duck"; response meta still says "duckduckgo".
	defaultEngine  = "duck"
	defaultBaseURL = "http://127.0.0.1:7000"
	healthTimeout  = 5 * time.Second
)

var (
	ErrWebSearchEndpointNotConfigured = errors.New("openserp endpoint is not reachable")
)

type WebSearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type webSearchSource struct {
	ID              string
	Title           string
	URL             string
	Snippet         string
	Rank            uint8
	FetchedAtUnixMS int64
}

type webSearchBatch struct {
	ID             string
	ConversationID string
	TurnID         string
	ToolCallID     string
	Sources        []webSearchSource
}

// Service is an HTTP client for an externally managed OpenSERP instance
// (typically started via docker compose). It never spawns a local binary.
type WebSearchService struct {
	baseURL string
	client  *http.Client
	logger  *zap.Logger
	mu      sync.Mutex
}

func NewWebSearchService(baseURL string) *WebSearchService {
	return &WebSearchService{
		baseURL: normalizeBaseURL(baseURL),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: zap.NewNop(),
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

func (s *WebSearchService) Close() error {
	return nil
}

func (s *WebSearchService) EnsureReady(ctx context.Context) error {
	if s == nil {
		return ErrWebSearchEndpointNotConfigured
	}
	s.mu.Lock()
	base := s.baseURL
	s.mu.Unlock()
	if base == "" {
		return ErrWebSearchEndpointNotConfigured
	}
	healthCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, strings.TrimRight(base, "/")+"/health", nil)
	if err != nil {
		return err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebSearchEndpointNotConfigured, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: health status %d", ErrWebSearchEndpointNotConfigured, res.StatusCode)
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
	s.mu.Lock()
	base := s.baseURL
	s.mu.Unlock()
	s.logger.Info("openserp search start", zap.String("engine", defaultEngine), zap.Int("limit", limit), zap.Int("queryRunes", utf8.RuneCountInString(query)), zap.String("baseURL", base))
	endpoint := fmt.Sprintf("%s/%s/search?text=%s&limit=%d", strings.TrimRight(base, "/"), defaultEngine, url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		s.logger.Error("openserp search failed", zap.Error(err))
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		err := fmt.Errorf("openserp search status %d: %s", res.StatusCode, truncate(string(body), 200))
		s.logger.Error("openserp search failed", zap.Error(err))
		return nil, err
	}
	hits, err := parseSearchHits(body, limit)
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

func newWebSearchBatch(conversationID, turnID, toolCallID string, hits []WebSearchHit, fetchedAtUnixMS int64) (webSearchBatch, error) {
	conversationID = strings.TrimSpace(conversationID)
	turnID = strings.TrimSpace(turnID)
	toolCallID = strings.TrimSpace(toolCallID)
	if conversationID == "" || turnID == "" || toolCallID == "" {
		return webSearchBatch{}, errors.New("web search batch identity is required")
	}
	batch := webSearchBatch{
		ID:             webSearchBatchID(conversationID, turnID, toolCallID),
		ConversationID: conversationID,
		TurnID:         turnID,
		ToolCallID:     toolCallID,
		Sources:        make([]webSearchSource, 0, min(len(hits), defaultSearchLimit)),
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
		source := webSearchSource{
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
