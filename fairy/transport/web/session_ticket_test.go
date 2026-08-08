package web

import (
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserSessionTicketIsSingleUseAndOriginBound(t *testing.T) {
	now := time.Unix(100, 0)
	registry := newBrowserSessionTicketRegistry(2, 30*time.Second)
	registry.now = func() time.Time { return now }
	value, expiresAt, err := registry.issue("http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if value == "" || !expiresAt.Equal(now.Add(30*time.Second)) || len(registry.entries) != 1 {
		t.Fatalf("issued ticket = %q, expires=%v, entries=%d", value, expiresAt, len(registry.entries))
	}
	if registry.consume("http://localhost:8787", value) {
		t.Fatal("ticket accepted for a different origin")
	}
	if !registry.consume("http://127.0.0.1:8787", value) {
		t.Fatal("valid ticket was rejected")
	}
	if registry.consume("http://127.0.0.1:8787", value) {
		t.Fatal("ticket replay was accepted")
	}
}

func TestBrowserSessionTicketExpiryCapacityAndConcurrentConsume(t *testing.T) {
	now := time.Unix(200, 0)
	registry := newBrowserSessionTicketRegistry(1, time.Second)
	registry.now = func() time.Time { return now }
	first, _, err := registry.issue("http://localhost:8787")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.issue("http://localhost:8787"); err != errBrowserSessionTicketCapacity {
		t.Fatalf("capacity error = %v", err)
	}
	now = now.Add(2 * time.Second)
	if registry.consume("http://localhost:8787", first) {
		t.Fatal("expired ticket was accepted")
	}
	second, _, err := registry.issue("http://localhost:8787")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if registry.consume("http://localhost:8787", second) {
				accepted.Add(1)
			}
		}()
	}
	wait.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("concurrent accepts = %d", accepted.Load())
	}
}

func TestServerConsumesTicketFromWebSocketProtocols(t *testing.T) {
	registry := newBrowserSessionTicketRegistry(1, time.Minute)
	value, _, err := registry.issue("http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{sessionTickets: registry}
	request := httptest.NewRequest("GET", "http://127.0.0.1:8787/v1/session/ws", nil)
	request.Header.Set("Origin", "http://127.0.0.1:8787")
	request.Header.Set("Sec-WebSocket-Protocol", fairySessionProtocol+", "+fairySessionTicketProtocolPrefix+value)
	if !server.consumeBrowserSessionTicket(request) {
		t.Fatal("valid websocket ticket protocols were rejected")
	}
	if server.consumeBrowserSessionTicket(request) {
		t.Fatal("websocket ticket replay was accepted")
	}
}
