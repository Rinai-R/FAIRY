package turnclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/coreclient"
	"github.com/gorilla/websocket"
)

func TestRunnerCompletedDeliversTypedBeatsAndWaitsForSubmit(t *testing.T) {
	allow := make(chan struct{})
	server := newTurnWSServer(t, func(conn *websocket.Conn, frame map[string]any) {
		requestID, _ := frame["requestId"].(string)
		switch frame["type"] {
		case "session.watch":
			_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
			go func() {
				<-allow
				_ = conn.WriteJSON(map[string]any{
					"type": "turn.event", "conversationId": "c1",
					"event": map[string]any{
						"conversationId": "c1", "turnId": "t1", "sequence": 1, "state": "responding",
						"payload": json.RawMessage(`{"type":"beat.ready","beatId":"b1","kind":"final","displayText":"你好","visualState":"idle"}`),
					},
				})
				_ = conn.WriteJSON(map[string]any{
					"type": "turn.event", "conversationId": "c1",
					"event": map[string]any{
						"conversationId": "c1", "turnId": "t1", "sequence": 2, "state": "completed",
						"payload": json.RawMessage(`{"type":"completed","text":"你好"}`),
					},
				})
			}()
		case "turn.submit":
			_ = conn.WriteJSON(map[string]any{
				"type": "result", "requestId": requestID,
				"payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"你好"}}`),
			})
			close(allow)
		}
	})
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	result, err := runner.Run(context.Background(), Request{ConversationID: "c1", Input: "你好"}, func(event Event) error {
		types = append(types, event.Type)
		if event.Type == "beat.ready" && (event.Beat == nil || event.Beat.DisplayText != "你好") {
			t.Fatal("beat was not decoded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Terminal.Type != "completed" || strings.Join(types, ",") != "beat.ready,completed" {
		t.Fatalf("result=%#v types=%v", result, types)
	}
}

func TestRunnerDisconnectReturnsErrorAndCancelsKnownTurn(t *testing.T) {
	var cancelCalls atomic.Int32
	server := newTurnWSServer(t, func(conn *websocket.Conn, frame map[string]any) {
		requestID, _ := frame["requestId"].(string)
		switch frame["type"] {
		case "session.watch":
			_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
			_ = conn.WriteJSON(map[string]any{
				"type": "turn.event", "conversationId": "c1",
				"event": map[string]any{
					"conversationId": "c1", "turnId": "t1", "sequence": 1, "state": "responding",
					"payload": json.RawMessage(`{"type":"state_changed"}`),
				},
			})
			_ = conn.Close()
		case "turn.submit":
			_ = conn.WriteJSON(map[string]any{
				"type": "result", "requestId": requestID,
				"payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"断开"}}`),
			})
		case "turn.cancel":
			cancelCalls.Add(1)
			_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": requestID, "payload": json.RawMessage(`{"ok":true}`)})
		}
	})
	defer server.Close()
	client, _ := coreclient.New(coreclient.Options{Endpoint: server.URL})
	runner, _ := New(client, time.Second)
	_, err := runner.Run(context.Background(), Request{ConversationID: "c1", Input: "断开"}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "disconnected before terminal") {
		t.Fatalf("Run() error = %v", err)
	}
	if cancelCalls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancelCalls.Load())
	}
}

func TestDecodeInterruptedAndFailedTerminals(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   string
		payload string
		want    string
	}{
		{name: "interrupted", state: "interrupted", payload: `{"type":"state_changed"}`, want: "state_changed"},
		{name: "failed", state: "failed", payload: `{"type":"failed","error":{"code":"x","message":"y","retryable":false}}`, want: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, err := DecodeEvent(coreclient.SSEEvent{
				Event: "turn.event",
				Data:  []byte(`{"conversationId":"c1","turnId":"t1","sequence":1,"state":"` + test.state + `","payload":` + test.payload + `}`),
			})
			if err != nil || event.Type != test.want || !isTerminal(event) {
				t.Fatalf("event=%#v err=%v", event, err)
			}
		})
	}
}

func newTurnWSServer(t *testing.T, handle func(*websocket.Conn, map[string]any)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/session/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{"type": "ready"}); err != nil {
			return
		}
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			mu.Lock()
			handle(conn, frame)
			mu.Unlock()
		}
	}))
}
