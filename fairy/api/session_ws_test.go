package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/session"

	"github.com/gorilla/websocket"
)

func TestSessionPlaneDocumentsWebSocketOnly(t *testing.T) {
	removed := []string{
		"POST /v1/sessions",
		"POST /v1/sessions/:id/turns",
		"POST /v1/sessions/:id/group-participation",
		"GET /v1/sessions/:id/events",
		"POST /v1/sessions/:id/turns/:turnId/cancel",
	}
	if len(removed) != 5 {
		t.Fatalf("removed session HTTP/SSE routes = %d", len(removed))
	}
}

func TestSessionConnOverflowSendsTryAgainLaterClose(t *testing.T) {
	connected := make(chan struct{})
	shutdown := make(chan struct{})
	var handler sync.WaitGroup
	handler.Add(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer handler.Done()
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		session := &sessionConn{conn: conn, watches: make(map[string]func())}
		close(connected)
		<-shutdown
		session.shutdown(ErrEventSubscriberOverflow)
	}))
	defer func() {
		server.Close()
		handler.Wait()
	}()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-connected
	close(shutdown)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("read error = %v, want websocket.CloseError", err)
	}
	if closeErr.Code != websocket.CloseTryAgainLater || closeErr.Text != ErrEventSubscriberOverflow.Error() {
		t.Fatalf("close = %#v", closeErr)
	}
}

func TestForwardTurnEventsPreservesOverflowReasonAfterEventStreamCloses(t *testing.T) {
	events := make(chan session.Event)
	failures := make(chan error, 1)
	close(events)
	failures <- ErrEventSubscriberOverflow
	close(failures)
	subscription := EventSubscription{Events: events, Failures: failures}

	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		session := &sessionConn{conn: conn, watches: map[string]func(){"c1": subscription.Unsubscribe}}
		session.forwardTurnEvents("c1", subscription)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseTryAgainLater {
		t.Fatalf("read error = %v, want 1013 close", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("forward turn event did not terminate")
	}
}
