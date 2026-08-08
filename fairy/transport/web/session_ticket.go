package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

const (
	browserSessionTicketCapacity = 128
	browserSessionTicketTTL      = 30 * time.Second
)

var errBrowserSessionTicketCapacity = errors.New("browser session ticket capacity exhausted")

type browserSessionTicket struct {
	origin    string
	expiresAt time.Time
}

type browserSessionTicketRegistry struct {
	mu       sync.Mutex
	entries  map[[sha256.Size]byte]browserSessionTicket
	capacity int
	ttl      time.Duration
	now      func() time.Time
}

func newBrowserSessionTicketRegistry(capacity int, ttl time.Duration) *browserSessionTicketRegistry {
	if capacity < 1 {
		capacity = 1
	}
	if ttl <= 0 {
		ttl = browserSessionTicketTTL
	}
	return &browserSessionTicketRegistry{
		entries:  make(map[[sha256.Size]byte]browserSessionTicket, capacity),
		capacity: capacity,
		ttl:      ttl,
		now:      time.Now,
	}
}

func normalizeTicketOrigin(origin string) (string, error) {
	origin = strings.ToLower(strings.TrimSpace(origin))
	if origin == "" || !isLocalConsoleOrigin(origin) {
		return "", errors.New("origin not allowed")
	}
	return strings.TrimSuffix(origin, "/"), nil
}

func (registry *browserSessionTicketRegistry) issue(origin string) (string, time.Time, error) {
	if registry == nil {
		return "", time.Time{}, errors.New("browser session tickets are unavailable")
	}
	origin, err := normalizeTicketOrigin(origin)
	if err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, errors.New("generating browser session ticket")
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(value))

	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	registry.removeExpired(now)
	if len(registry.entries) >= registry.capacity {
		return "", time.Time{}, errBrowserSessionTicketCapacity
	}
	expiresAt := now.Add(registry.ttl)
	registry.entries[digest] = browserSessionTicket{origin: origin, expiresAt: expiresAt}
	return value, expiresAt, nil
}

func (registry *browserSessionTicketRegistry) consume(origin string, value string) bool {
	if registry == nil || strings.TrimSpace(value) == "" {
		return false
	}
	origin, err := normalizeTicketOrigin(origin)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(value))
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	registry.removeExpired(now)
	ticket, ok := registry.entries[digest]
	if !ok || ticket.origin != origin || !now.Before(ticket.expiresAt) {
		return false
	}
	delete(registry.entries, digest)
	return true
}

func (registry *browserSessionTicketRegistry) removeExpired(now time.Time) {
	for digest, ticket := range registry.entries {
		if !now.Before(ticket.expiresAt) {
			delete(registry.entries, digest)
		}
	}
}

func (s *Server) handleBrowserSessionTicket(_ context.Context, c *app.RequestContext) {
	origin := string(c.GetHeader("Origin"))
	value, expiresAt, err := s.sessionTickets.issue(origin)
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, errBrowserSessionTicketCapacity) {
			status = http.StatusTooManyRequests
		}
		writeErr(c, status, err)
		return
	}
	c.JSON(http.StatusCreated, map[string]any{
		"ticket":          value,
		"protocol":        fairySessionProtocol,
		"expiresAtUnixMs": expiresAt.UnixMilli(),
	})
}
