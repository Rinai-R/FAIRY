package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	defaultSearchLimit = 5
	// OpenSERP HTTP path segment is "duck"; response meta still says "duckduckgo".
	defaultEngine        = "duck"
	defaultBaseURL       = "http://127.0.0.1:7000"
	healthTimeout        = 5 * time.Second
	envOpenSERPBaseURL   = "FAIRY_OPENSERP_URL"
)

var (
	ErrEndpointNotConfigured = errors.New("openserp endpoint is not reachable")
	ErrDisabled              = errors.New("web search is disabled")
)

type Hit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Service is an HTTP client for an externally managed OpenSERP instance
// (typically started via docker compose). It never spawns a local binary.
type Service struct {
	baseURL string
	client  *http.Client
	logger  *zap.Logger
	mu      sync.Mutex
}

func NewService(baseURL string) *Service {
	return &Service{
		baseURL: normalizeBaseURL(baseURL),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: zap.NewNop(),
	}
}

// NewServiceFromEnv builds a client using explicit baseURL, else FAIRY_OPENSERP_URL, else localhost:7000.
func NewServiceFromEnv(baseURL string) *Service {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = os.Getenv(envOpenSERPBaseURL)
	}
	return NewService(baseURL)
}

func AttachLogger(s *Service, logger *zap.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}

func (s *Service) BaseURL() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

func (s *Service) Close() error {
	return nil
}

func (s *Service) EnsureReady(ctx context.Context) error {
	if s == nil {
		return ErrEndpointNotConfigured
	}
	s.mu.Lock()
	base := s.baseURL
	s.mu.Unlock()
	if base == "" {
		return ErrEndpointNotConfigured
	}
	healthCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, strings.TrimRight(base, "/")+"/health", nil)
	if err != nil {
		return err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEndpointNotConfigured, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: health status %d", ErrEndpointNotConfigured, res.StatusCode)
	}
	return nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
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

func parseSearchHits(body []byte, limit int) ([]Hit, error) {
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
	hits := make([]Hit, 0, limit)
	for _, item := range payload.Results[:limit] {
		hits = append(hits, Hit{Title: item.Title, URL: item.URL, Snippet: item.Snippet})
	}
	return hits, nil
}
