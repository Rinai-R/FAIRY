package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeTokenStore struct {
	token  string
	getErr error
	setErr error
	sets   int
}

func (f *fakeTokenStore) Get() (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	if f.token == "" {
		return "", ErrTokenNotFound
	}
	return f.token, nil
}

func (f *fakeTokenStore) Set(token string) error {
	f.sets++
	if f.setErr != nil {
		return f.setErr
	}
	f.token = token
	return nil
}

func (f *fakeTokenStore) Delete() error { f.token = ""; return nil }

func TestValidateEndpointAllowsOnlyHTTPSOrLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:8787", "http://[::1]:8787", "http://localhost:8787", "https://core.example.com"} {
		if _, err := ValidateEndpoint(endpoint); err != nil {
			t.Fatalf("ValidateEndpoint(%q): %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"http://core.example.com", "https://user@core.example.com", "https://core.example.com/path", " https://core.example.com"} {
		if _, err := ValidateEndpoint(endpoint); err == nil {
			t.Fatalf("ValidateEndpoint(%q) accepted unsafe endpoint", endpoint)
		}
	}
}

func TestInstallationKeyGenerationAndValidation(t *testing.T) {
	key, err := EnsureInstallationKey("")
	if err != nil || !installationKeyPattern.MatchString(key) || !strings.HasPrefix(key, "macos-") {
		t.Fatalf("generated key = %q, %v", key, err)
	}
	if got, err := EnsureInstallationKey(key); err != nil || got != key {
		t.Fatalf("existing key = %q, %v", got, err)
	}
	if _, err := EnsureInstallationKey("bad key"); err == nil {
		t.Fatal("invalid installation key accepted")
	}
}

func TestConnectionNeverReturnsTokenAndKeychainFailureIsExplicit(t *testing.T) {
	tokens := &fakeTokenStore{token: "secret-token"}
	service := NewAppService(tokens)
	state, err := service.Connection("http://127.0.0.1:8787", "macos-installation")
	if err != nil || !state.HasToken {
		t.Fatalf("Connection() = %#v, %v", state, err)
	}
	if fmt.Sprintf("%#v", state) == "secret-token" || strings.Contains(fmt.Sprintf("%#v", state), tokens.token) {
		t.Fatal("connection state exposed token")
	}
	tokens.getErr = errors.New("keychain locked")
	if _, err := service.Connection("http://127.0.0.1:8787", "macos-installation"); err == nil || !strings.Contains(err.Error(), "Keychain failed") {
		t.Fatalf("Keychain read error = %v", err)
	}
}

func TestSaveConnectionDoesNotFallbackWhenKeychainWriteFails(t *testing.T) {
	tokens := &fakeTokenStore{setErr: errors.New("denied")}
	service := NewAppService(tokens)
	if _, err := service.SaveConnection("https://core.example.com", "secret-token", "macos-installation"); err == nil {
		t.Fatal("SaveConnection succeeded after Keychain failure")
	}
	if tokens.token != "" || tokens.sets != 1 {
		t.Fatalf("fake token store = %#v", tokens)
	}
}

func TestSaveConnectionAllowsEmptyTokenWithoutOverwritingKeychain(t *testing.T) {
	tokens := &fakeTokenStore{token: "existing-token"}
	service := NewAppService(tokens)
	state, err := service.SaveConnection("http://127.0.0.1:8787", "", "macos-installation")
	if err != nil || !state.HasToken {
		t.Fatalf("SaveConnection() = %#v, %v", state, err)
	}
	if tokens.token != "existing-token" || tokens.sets != 0 {
		t.Fatalf("empty token changed Keychain: %#v", tokens)
	}
}

func TestConnectUsesBearerAndDesktopInstallationSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	expectedAuthorization := "Bearer exact-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expectedAuthorization {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/v1/session/ws" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"type": "ready"})
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			if frame["type"] != "session.open" || frame["endpoint"] != "desktop" || frame["endpointKey"] != "macos-installation" {
				t.Fatalf("session frame = %#v", frame)
			}
			_ = conn.WriteJSON(map[string]any{
				"type": "session.opened", "requestId": frame["requestId"],
				"conversationId": "c1", "characterId": "ch1", "messageCount": 1, "endpoint": "desktop",
			})
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			if frame["type"] != "session.watch" || frame["conversationId"] != "c1" {
				t.Fatalf("watch frame = %#v", frame)
			}
			_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": frame["requestId"]})
			for {
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				switch frame["type"] {
				case "turn.submit":
					input, _ := frame["input"].(string)
					submitRequestID := frame["requestId"]
					if input == "cancel" {
						_ = conn.WriteJSON(map[string]any{"type": "turn.event", "conversationId": "c1", "event": map[string]any{"conversationId": "c1", "turnId": "t2", "sequence": 1, "state": "running", "payload": json.RawMessage(`{"type":"state_changed"}`)}})
						if err := conn.ReadJSON(&frame); err != nil {
							return
						}
						if frame["type"] != "turn.cancel" || frame["turnId"] != "t2" {
							t.Fatalf("cancel frame = %#v", frame)
						}
						_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": frame["requestId"], "payload": json.RawMessage(`{}`)})
						_ = conn.WriteJSON(map[string]any{"type": "turn.event", "conversationId": "c1", "event": map[string]any{"conversationId": "c1", "turnId": "t2", "sequence": 2, "state": "interrupted", "payload": json.RawMessage(`{"type":"state_changed"}`)}})
					} else {
						_ = conn.WriteJSON(map[string]any{"type": "turn.event", "conversationId": "c1", "event": map[string]any{"conversationId": "c1", "turnId": "t1", "sequence": 1, "state": "completed", "payload": json.RawMessage(`{"type":"completed"}`)}})
					}
					_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": submitRequestID, "payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t` + map[bool]string{true: "2", false: "1"}[input == "cancel"] + `"}}`)})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			fmt.Fprint(w, `{"bootstrap":{},"configRoot":"/tmp","webSearch":{},"semanticEmbedding":{},"database":{"ready":true,"mode":"production"},"qdrant":{"ready":true,"mode":"production"},"secretKey":{"ready":true,"mode":"production"}}`)
		case "/v1/sessions/c1/messages":
			fmt.Fprint(w, `{"messages":[{"id":"m1","conversationId":"c1","turnId":"t1","sequence":1,"role":"assistant","content":"你好","createdAtUnixMs":1}]}`)
		case "/v1/characters":
			fmt.Fprint(w, `{"characters":[{"characterId":"ch1","revision":1,"name":"Fairy","appearance":{"status":"assigned","visual":{"packId":"fairy.test","states":[{"id":"idle","description":"待机","imagePath":"fairy-character://localhost/fairy.test/images/idle.png"}]}}}],"active":{"characterId":"ch1","revision":1,"name":"Fairy","appearance":{"status":"assigned","visual":{"packId":"fairy.test","states":[{"id":"idle","description":"待机","imagePath":"fairy-character://localhost/fairy.test/images/idle.png"}]}}}}`)
		case "/v1/visual-assets/fairy.test/images/idle.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("png"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := NewAppService(&fakeTokenStore{token: "exact-token"})
	defer service.Shutdown(context.Background())
	state, err := service.Connect(server.URL, "macos-installation")
	if err != nil || state.Session.ConversationID != "c1" || len(state.Messages) != 1 || state.Character.Name != "Fairy" || len(state.Visuals) != 1 {
		t.Fatalf("Connect() = %#v, %v", state, err)
	}
	turnStarted := make(chan desktopTurnEvent, 1)
	turnCompleted := make(chan desktopTurnEvent, 1)
	service.emit = func(event any) {
		if converted, ok := event.(desktopTurnEvent); ok {
			switch converted.TurnEvent.State {
			case "running":
				turnStarted <- converted
			case "completed":
				turnCompleted <- converted
			}
		}
	}
	if err := service.Send("complete", false); err != nil {
		t.Fatalf("Send(completed): %v", err)
	}
	select {
	case <-turnCompleted:
	case <-time.After(time.Second):
		t.Fatal("completed event was not forwarded")
	}
	sendDone := make(chan error, 1)
	go func() { sendDone <- service.Send("cancel", false) }()
	select {
	case <-turnStarted:
	case <-time.After(time.Second):
		t.Fatal("turn did not reach running state")
	}
	if err := service.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("Send(cancel): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled submit did not return")
	}

	expectedAuthorization = ""
	unauthenticated := NewAppService(&fakeTokenStore{})
	defer unauthenticated.Shutdown(context.Background())
	state, err = unauthenticated.Connect(server.URL, "macos-installation")
	if err != nil || state.Connection.HasToken {
		t.Fatalf("unauthenticated Connect() = %#v, %v", state, err)
	}
}

func TestCancelRejectsWhenNoTurnIsActive(t *testing.T) {
	service := NewAppService(&fakeTokenStore{})
	if err := service.Cancel(); err == nil {
		t.Fatal("Cancel() succeeded without an active turn")
	}
}
