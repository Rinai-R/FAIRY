package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultSearchLimit = 5
	// OpenSERP HTTP path segment is "duck"; response meta still says "duckduckgo".
	defaultEngine = "duck"
	healthTimeout      = 15 * time.Second
	healthPollInterval = 200 * time.Millisecond
)

var (
	ErrBinaryNotFound = errors.New("openserp binary not found")
	ErrDisabled       = errors.New("web search is disabled")
)

type Hit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type Service struct {
	root   string
	client *http.Client

	mu      sync.Mutex
	cmd     *exec.Cmd
	port    int
	baseURL string
}

func NewService(root string) *Service {
	return &Service{
		root: root,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "openserp.exe"
	}
	return "openserp"
}

func ResolveBinary(root string) (string, bool) {
	for _, path := range BinaryCandidates(root) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	candidates := BinaryCandidates(root)
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[0], false
}

func BinaryCandidates(root string) []string {
	name := BinaryName()
	out := make([]string, 0, 4)
	if env := os.Getenv("FAIRY_OPENSERP_PATH"); env != "" {
		out = append(out, env)
	}
	if root != "" {
		out = append(out, filepath.Join(root, "bin", name))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), name))
		out = append(out, filepath.Join(filepath.Dir(exe), "bin", name))
	}
	return out
}

func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *Service) EnsureRunning(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseURL != "" {
		if err := s.healthLocked(ctx, s.baseURL); err == nil {
			return s.baseURL, nil
		}
		_ = s.stopLocked()
	}
	binary, found := ResolveBinary(s.root)
	if !found {
		return "", fmt.Errorf("%w (expected under %s/bin/%s or FAIRY_OPENSERP_PATH)", ErrBinaryNotFound, s.root, BinaryName())
	}
	port, err := freePort()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(context.Background(), binary, "serve", "-a", "127.0.0.1", "-p", strconv.Itoa(port))
	cmd.Dir = filepath.Dir(binary)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", err
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	s.cmd = cmd
	s.port = port
	s.baseURL = baseURL
	if err := s.healthLocked(ctx, baseURL); err != nil {
		log.Printf("openserp sidecar health failed port=%d err=%v", port, err)
		_ = s.stopLocked()
		return "", err
	}
	log.Printf("openserp sidecar started port=%d binary=%s", port, binary)
	go s.reap()
	return baseURL, nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	query = trimQuery(query)
	if query == "" {
		return nil, errors.New("web search query is empty")
	}
	if limit <= 0 || limit > defaultSearchLimit {
		limit = defaultSearchLimit
	}
	baseURL, err := s.EnsureRunning(ctx)
	if err != nil {
		return nil, err
	}
	log.Printf("openserp search start engine=%s limit=%d queryRunes=%d", defaultEngine, limit, utf8.RuneCountInString(query))
	endpoint := fmt.Sprintf("%s/%s/search?text=%s&limit=%d", baseURL, defaultEngine, url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		log.Printf("openserp search failed err=%v", err)
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		err := fmt.Errorf("openserp search status %d: %s", res.StatusCode, truncate(string(body), 200))
		log.Printf("openserp search failed err=%v", err)
		return nil, err
	}
	hits, err := parseSearchHits(body, limit)
	if err != nil {
		log.Printf("openserp search parse failed err=%v", err)
		return nil, err
	}
	log.Printf("openserp search done hits=%d", len(hits))
	return hits, nil
}

func (s *Service) healthLocked(ctx context.Context, baseURL string) error {
	deadline := time.Now().Add(healthTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	var last error
	for time.Now().Before(deadline) {
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			cancel()
			return err
		}
		res, err := s.client.Do(req)
		if err == nil {
			_ = res.Body.Close()
			cancel()
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				return nil
			}
			last = fmt.Errorf("openserp health status %d", res.StatusCode)
		} else {
			last = err
			cancel()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPollInterval):
		}
	}
	if last == nil {
		last = errors.New("openserp health check timed out")
	}
	return last
}

func (s *Service) stopLocked() error {
	if s.cmd == nil || s.cmd.Process == nil {
		s.cmd = nil
		s.port = 0
		s.baseURL = ""
		return nil
	}
	err := s.cmd.Process.Kill()
	_, _ = s.cmd.Process.Wait()
	s.cmd = nil
	s.port = 0
	s.baseURL = ""
	return err
}

func (s *Service) reap() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	_, _ = cmd.Process.Wait()
	s.mu.Lock()
	if s.cmd == cmd {
		s.cmd = nil
		s.port = 0
		s.baseURL = ""
	}
	s.mu.Unlock()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func trimQuery(query string) string {
	return strings.TrimSpace(query)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

type searchResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

func parseSearchHits(body []byte, limit int) ([]Hit, error) {
	var parsed searchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		if item.Title == "" && item.URL == "" {
			continue
		}
		hits = append(hits, Hit{Title: item.Title, URL: item.URL, Snippet: item.Snippet})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}
