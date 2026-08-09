package main

import (
	"encoding/json"
	"errors"
	"fairy/transport/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeWindow struct {
	application.Window
	x, y          int
	shown, hidden bool
	visible       bool
	focused       bool
	showCount     int
}

type memoryConnectionStore struct {
	connection desktopConnection
	loadErr    error
	saveErr    error
	saveCount  int
}

func (s *memoryConnectionStore) Load() (desktopConnection, error) {
	if s.loadErr != nil {
		return desktopConnection{}, s.loadErr
	}
	return s.connection, nil
}

func (s *memoryConnectionStore) Save(connection desktopConnection) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.connection = connection
	s.loadErr = nil
	s.saveCount++
	return nil
}

func (w *fakeWindow) Position() (int, int) { return w.x, w.y }
func (w *fakeWindow) SetPosition(x, y int) { w.x, w.y = x, y }
func (w *fakeWindow) Show() application.Window {
	w.shown, w.visible = true, true
	w.showCount++
	return w
}
func (w *fakeWindow) Hide() application.Window {
	w.hidden, w.visible = true, false
	return w
}
func (w *fakeWindow) IsVisible() bool { return w.visible }
func (w *fakeWindow) Focus()          { w.focused = true }

func TestDecodeDesktopTurnEventKeepsTextOnlyBeat(t *testing.T) {
	payload := `{"type":"beat.ready","beatId":"b1","kind":"final","displayText":"只显示文字","visualState":"idle"}`
	event := decodeDesktopTurnEvent(session.TurnEvent{TurnID: "turn-1", Payload: json.RawMessage(payload)})
	if event.Beat == nil || event.Beat.DisplayText != "只显示文字" {
		t.Fatalf("decoded text-only beat = %#v", event.Beat)
	}
}

func TestOpenHistoryPlacesWindowToCompanionLeft(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	history := &fakeWindow{}
	service := NewCoreService()
	service.attachWindows(companion, nil, history, nil)
	var historyOpen bool
	service.attachEmitter(func(_ string, payload any) { historyOpen = payload.(map[string]bool)["open"] })

	if err := service.OpenHistory(); err != nil {
		t.Fatalf("OpenHistory() error = %v", err)
	}
	if !history.shown || !companion.focused {
		t.Fatalf("history shown=%t companion focused=%t, want both true", history.shown, companion.focused)
	}
	if history.x != 360 || history.y != 326 {
		t.Fatalf("history window position = (%d, %d), want (360, 326)", history.x, history.y)
	}
	if !historyOpen {
		t.Fatal("history open event was not emitted")
	}

	service.CloseHistory()
	if !history.hidden {
		t.Fatal("history window was not hidden")
	}
	if historyOpen {
		t.Fatal("history close event was not emitted")
	}
}

func TestOpenControlPanelPlacesWindowBesideCompanion(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{}
	history := &fakeWindow{}
	service := NewCoreService()
	service.attachWindows(companion, panel, history, nil)

	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() error = %v", err)
	}
	if companion.hidden {
		t.Fatal("companion window was hidden")
	}
	if !panel.shown || !companion.focused {
		t.Fatalf("settings shown=%t companion focused=%t, want both true", panel.shown, companion.focused)
	}
	if !history.hidden {
		t.Fatal("history window was not hidden before opening settings")
	}
	if panel.x != 352 || panel.y != 397 {
		t.Fatalf("settings window position = (%d, %d), want (352, 397)", panel.x, panel.y)
	}
}

func TestOpenControlPanelClosesVisiblePanel(t *testing.T) {
	panel := &fakeWindow{}
	service := NewCoreService()
	service.attachWindows(nil, panel, nil, nil)
	var controlPanelOpen bool
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:control-panel" {
			controlPanelOpen = payload.(map[string]bool)["open"]
		}
	})

	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() first call error = %v", err)
	}
	if !controlPanelOpen {
		t.Fatal("control panel open event was not emitted")
	}
	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() second call error = %v", err)
	}
	if !panel.hidden || panel.visible {
		t.Fatalf("settings window hidden=%t visible=%t, want hidden and not visible", panel.hidden, panel.visible)
	}
	if controlPanelOpen {
		t.Fatal("control panel close event was not emitted")
	}
}

func TestOpenHistoryHidesSettingsPanel(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{}
	history := &fakeWindow{}
	service := NewCoreService()
	service.attachWindows(companion, panel, history, nil)

	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() error = %v", err)
	}
	if err := service.OpenHistory(); err != nil {
		t.Fatalf("OpenHistory() error = %v", err)
	}
	if !panel.hidden {
		t.Fatal("settings window was not hidden before opening history")
	}
	if !history.visible {
		t.Fatal("history window was not shown after opening it from settings")
	}

	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() error = %v", err)
	}
	if !history.hidden {
		t.Fatal("history window was not hidden before reopening settings")
	}
	if !panel.visible {
		t.Fatal("settings window was not shown after opening it from history")
	}
}

func TestConnectionSettingsReturnsExplicitUnconfiguredState(t *testing.T) {
	service := NewCoreService()
	service.connections = &memoryConnectionStore{loadErr: errConnectionNotFound}

	settings, err := service.ConnectionSettings()
	if err != nil {
		t.Fatalf("ConnectionSettings() error = %v", err)
	}
	if settings.Endpoint != defaultCoreEndpoint || settings.EndpointKey != "" || settings.HasToken {
		t.Fatalf("ConnectionSettings() = %#v, want default unconfigured state", settings)
	}
	if _, err := service.Connect(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Connect() error = %v, want explicit unconfigured error", err)
	}
}

func TestConnectionSettingsRejectsDamagedStore(t *testing.T) {
	service := NewCoreService()
	service.connections = &memoryConnectionStore{loadErr: errors.New("mode must use 0600")}

	if _, err := service.ConnectionSettings(); err == nil || !strings.Contains(err.Error(), "mode must use 0600") {
		t.Fatalf("ConnectionSettings() error = %v, want store diagnostic", err)
	}
	if _, err := service.Connect(); err == nil || !strings.Contains(err.Error(), "mode must use 0600") {
		t.Fatalf("Connect() error = %v, want store diagnostic", err)
	}
}

func TestSaveConnectionRequiresInitialTokenAndRetainsExistingToken(t *testing.T) {
	store := &memoryConnectionStore{loadErr: errConnectionNotFound}
	service := NewCoreService()
	service.connections = store

	if _, err := service.SaveConnection(defaultCoreEndpoint, "", "desktop-test"); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("initial SaveConnection() error = %v, want token required", err)
	}
	settings, err := service.SaveConnection(defaultCoreEndpoint, "desktop-secret", "desktop-test")
	if err != nil {
		t.Fatalf("SaveConnection() error = %v", err)
	}
	if !settings.HasToken || settings.EndpointKey != "desktop-test" {
		t.Fatalf("SaveConnection() settings = %#v", settings)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "desktop-secret") || strings.Contains(string(encoded), `"token"`) {
		t.Fatalf("settings response exposed token: %s", encoded)
	}

	if _, err := service.SaveConnection("http://127.0.0.1:8788", "", "desktop-test-2"); err != nil {
		t.Fatalf("SaveConnection() retaining token error = %v", err)
	}
	if store.connection.Token != "desktop-secret" {
		t.Fatalf("retained token = %q, want original token", store.connection.Token)
	}
	if store.connection.Endpoint != "http://127.0.0.1:8788" || store.connection.EndpointKey != "desktop-test-2" {
		t.Fatalf("saved connection = %#v", store.connection)
	}
}

func TestSaveConnectionGeneratesInstallationKey(t *testing.T) {
	store := &memoryConnectionStore{loadErr: errConnectionNotFound}
	service := NewCoreService()
	service.connections = store

	settings, err := service.SaveConnection(defaultCoreEndpoint, "desktop-secret", "")
	if err != nil {
		t.Fatalf("SaveConnection() error = %v", err)
	}
	if !installationKeyPattern.MatchString(settings.EndpointKey) || !strings.HasPrefix(settings.EndpointKey, "macos-") {
		t.Fatalf("generated endpoint key = %q", settings.EndpointKey)
	}
}

func TestCoreServiceUsesOneSocketAndClearsCompletedTurn(t *testing.T) {
	var mu sync.Mutex
	var frameTypes []string
	connections := 0
	deliveryReceived := make(chan session.ExpressionDeliveryResult, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			writeServiceFixtureJSON(t, w, serviceStatusFixture())
		case "/v1/characters":
			writeServiceFixtureJSON(t, w, session.CharacterCatalog{Characters: []session.CharacterRecord{serviceCharacterFixture()}, Active: ptr(serviceCharacterFixture())})
		case "/v1/visual-assets/fairy.test/images/idle.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
		case "/v1/sessions/c1/messages":
			writeServiceFixtureJSON(t, w, session.MessagePage{Messages: []session.MessageRecord{}})
		case "/v1/session/ws":
			mu.Lock()
			connections++
			mu.Unlock()
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"type": "ready"})
			var submitRequestID string
			for {
				var frame map[string]json.RawMessage
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				var kind, requestID string
				_ = json.Unmarshal(frame["type"], &kind)
				_ = json.Unmarshal(frame["requestId"], &requestID)
				mu.Lock()
				frameTypes = append(frameTypes, kind)
				mu.Unlock()
				switch kind {
				case "session.open":
					var capabilities session.OutputCapabilities
					_ = json.Unmarshal(frame["outputCapabilities"], &capabilities)
					if !capabilities.Sticker {
						t.Error("Desktop session did not declare sticker capability")
						return
					}
					_ = conn.WriteJSON(map[string]any{"type": "session.opened", "requestId": requestID, "conversationId": "c1", "characterId": "character-1", "endpoint": "desktop"})
				case "session.watch":
					_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
				case "turn.submit":
					submitRequestID = requestID
					if _, present := frame["speechEnabled"]; present {
						t.Error("Desktop turn sent removed speech option")
						return
					}
					writeTurnEventFixture(conn, "t1", 1, "responding", `{"type":"beat.ready","beatId":"b1","kind":"final","displayText":"ok","visualState":"idle"}`)
				case "expression.delivery":
					var result session.ExpressionDeliveryResult
					_ = json.Unmarshal(frame["deliveryResult"], &result)
					deliveryReceived <- result
					_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
					writeTurnEventFixture(conn, "t1", 2, "completed", `{"type":"completed"}`)
					_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": submitRequestID, "payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"ok"}}`)})
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewCoreService()
	service.connections = &memoryConnectionStore{connection: desktopConnection{
		Endpoint:    server.URL,
		EndpointKey: "desktop-test",
		Token:       "desktop-test-token",
	}}
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	turns := make(chan desktopTurnEvent, 4)
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:turn" {
			turns <- payload.(desktopTurnEvent)
		}
	})
	if _, err := service.Connect(); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer service.ServiceShutdown()
	if err := service.Send("hello"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	assertTurnTypes(t, turns, "state_changed", "beat.ready", "completed")
	service.mu.Lock()
	active := service.active
	service.mu.Unlock()
	if active {
		t.Fatal("completed turn left service active")
	}
	select {
	case result := <-deliveryReceived:
		if result.Status != session.ExpressionDeliverySucceeded || result.ConversationID != "c1" || result.TurnID != "t1" || result.BeatID != "b1" || result.ExternalMessageID != "" {
			t.Fatalf("delivery result = %#v", result)
		}
	default:
		t.Fatal("Desktop did not report accepted final utterance")
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 1 {
		t.Fatalf("websocket connections = %d, want 1", connections)
	}
	if got, want := frameTypes, []string{"session.open", "session.watch", "turn.submit", "expression.delivery"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("socket frames = %v, want %v", got, want)
	}
}

func TestCoreServiceRejectsSendAndCancelsProactiveTurn(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	cancelled := make(chan struct {
		conversation string
		turnID       string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			writeServiceFixtureJSON(t, w, serviceStatusFixture())
		case "/v1/characters":
			writeServiceFixtureJSON(t, w, session.CharacterCatalog{Characters: []session.CharacterRecord{serviceCharacterFixture()}, Active: ptr(serviceCharacterFixture())})
		case "/v1/visual-assets/fairy.test/images/idle.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
		case "/v1/sessions/c1/messages":
			writeServiceFixtureJSON(t, w, session.MessagePage{Messages: []session.MessageRecord{}})
		case "/v1/session/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"type": "ready"})
			for {
				var frame map[string]json.RawMessage
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				var kind, requestID string
				_ = json.Unmarshal(frame["type"], &kind)
				_ = json.Unmarshal(frame["requestId"], &requestID)
				switch kind {
				case "session.open":
					_ = conn.WriteJSON(map[string]any{"type": "session.opened", "requestId": requestID, "conversationId": "c1", "characterId": "character-1", "endpoint": "desktop"})
				case "session.watch":
					_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
					writeTurnEventFixture(conn, "proactive-turn", 1, "planning", `{"type":"state_changed"}`)
				case "turn.submit":
					t.Error("Desktop submitted a second Turn while the proactive Turn was active")
					_ = conn.WriteJSON(map[string]any{
						"type": "result", "requestId": requestID,
						"payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"unexpected","responseText":""}}`),
					})
				case "turn.cancel":
					var conversation, turnID string
					_ = json.Unmarshal(frame["conversationId"], &conversation)
					_ = json.Unmarshal(frame["turnId"], &turnID)
					cancelled <- struct {
						conversation string
						turnID       string
					}{conversation: conversation, turnID: turnID}
					_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": requestID, "payload": json.RawMessage(`{"ok":true}`)})
					writeTurnEventFixture(conn, "proactive-turn", 2, "interrupted", `{"type":"state_changed"}`)
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewCoreService()
	service.connections = &memoryConnectionStore{connection: desktopConnection{
		Endpoint: server.URL, EndpointKey: "desktop-test", Token: "desktop-test-token",
	}}
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	turns := make(chan desktopTurnEvent, 4)
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:turn" {
			turns <- payload.(desktopTurnEvent)
		}
	})
	if _, err := service.Connect(); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer service.ServiceShutdown()

	assertTurnTypes(t, turns, "state_changed")
	if err := service.Send("must be rejected"); err == nil || err.Error() != "a turn is already active" {
		t.Fatalf("Send() error = %v, want active Turn error", err)
	}
	if err := service.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case got := <-cancelled:
		if got.conversation != "c1" || got.turnID != "proactive-turn" {
			t.Fatalf("cancel identity = (%q, %q), want (%q, %q)", got.conversation, got.turnID, "c1", "proactive-turn")
		}
	case <-time.After(time.Second):
		t.Fatal("proactive Turn cancel was not received")
	}
	assertTurnTypes(t, turns, "state_changed")
	service.mu.Lock()
	active, activeTurnID := service.active, service.activeTurnID
	service.mu.Unlock()
	if active || activeTurnID != "" {
		t.Fatalf("interrupted proactive Turn left active state = (%t, %q)", active, activeTurnID)
	}
}

func TestCoreServicePreparesControlledStickerAndReportsRenderSuccess(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	deliveryReceived := make(chan session.ExpressionDeliveryResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			writeServiceFixtureJSON(t, w, serviceStatusFixture())
		case "/v1/characters":
			writeServiceFixtureJSON(t, w, session.CharacterCatalog{Characters: []session.CharacterRecord{serviceCharacterFixture()}, Active: ptr(serviceCharacterFixture())})
		case "/v1/visual-assets/fairy.test/images/idle.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
		case "/v1/sessions/c1/messages":
			writeServiceFixtureJSON(t, w, session.MessagePage{Messages: []session.MessageRecord{}})
		case "/v1/stickers/sticker-1/content":
			if r.Header.Get("Authorization") != "Bearer desktop-test-token" {
				t.Errorf("sticker authorization = %q", r.Header.Get("Authorization"))
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write([]byte("GIF89a-content"))
		case "/v1/session/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"type": "ready"})
			var submitRequestID string
			for {
				var frame map[string]json.RawMessage
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				var kind, requestID string
				_ = json.Unmarshal(frame["type"], &kind)
				_ = json.Unmarshal(frame["requestId"], &requestID)
				switch kind {
				case "session.open":
					var capabilities session.OutputCapabilities
					_ = json.Unmarshal(frame["outputCapabilities"], &capabilities)
					if !capabilities.Sticker {
						t.Error("Desktop session did not declare sticker capability")
						return
					}
					_ = conn.WriteJSON(map[string]any{"type": "session.opened", "requestId": requestID, "conversationId": "c1", "characterId": "character-1", "endpoint": "desktop"})
				case "session.watch":
					_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
				case "turn.submit":
					submitRequestID = requestID
					writeTurnEventFixture(conn, "t1", 1, "responding", `{"type":"beat.ready","beatId":"b1","kind":"final","visualState":"idle","part":{"kind":"sticker","visualState":"idle","sticker":{"id":"sticker-1","description":"开心","mimeType":"image/gif"}}}`)
				case "expression.delivery":
					var result session.ExpressionDeliveryResult
					_ = json.Unmarshal(frame["deliveryResult"], &result)
					deliveryReceived <- result
					_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID, "conversationId": "c1"})
					writeTurnEventFixture(conn, "t1", 2, "completed", `{"type":"completed"}`)
					_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": submitRequestID, "payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"[表情包：开心]"}}`)})
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewCoreService()
	service.connections = &memoryConnectionStore{connection: desktopConnection{
		Endpoint: server.URL, EndpointKey: "desktop-test", Token: "desktop-test-token",
	}}
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	turns := make(chan desktopTurnEvent, 6)
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:turn" {
			turns <- payload.(desktopTurnEvent)
		}
	})
	if _, err := service.Connect(); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer service.ServiceShutdown()

	sendResult := make(chan error, 1)
	go func() { sendResult <- service.Send("发个表情") }()
	assertTurnTypes(t, turns, "state_changed")
	var beatEvent desktopTurnEvent
	select {
	case beatEvent = <-turns:
	case <-time.After(time.Second):
		t.Fatal("did not receive sticker beat")
	}
	if beatEvent.Type != "beat.ready" || beatEvent.Beat == nil || beatEvent.Beat.StickerURL == "" ||
		beatEvent.Beat.StickerUnavailable {
		t.Fatalf("sticker beat = %#v", beatEvent)
	}
	response := httptest.NewRecorder()
	service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, beatEvent.Beat.StickerURL, nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/gif" ||
		response.Body.String() != "GIF89a-content" {
		t.Fatalf("controlled sticker response = status %d, headers %#v, body %q", response.Code, response.Header(), response.Body.String())
	}
	if err := service.ReportStickerDelivery("t1", "b1", true); err != nil {
		t.Fatalf("ReportStickerDelivery() error = %v", err)
	}
	select {
	case result := <-deliveryReceived:
		if result.Status != session.ExpressionDeliverySucceeded || result.ConversationID != "c1" ||
			result.TurnID != "t1" || result.BeatID != "b1" {
			t.Fatalf("delivery result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive Desktop delivery result")
	}
	assertTurnTypes(t, turns, "completed")
	select {
	case err := <-sendResult:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send() did not finish after sticker delivery")
	}
}

func TestCoreServiceReportsStickerFetchFailure(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	deliveryReceived := make(chan session.ExpressionDeliveryResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/stickers/missing/content":
			http.NotFound(w, r)
		case "/v1/session/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"type": "ready"})
			for {
				var frame map[string]json.RawMessage
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				var kind, requestID string
				_ = json.Unmarshal(frame["type"], &kind)
				_ = json.Unmarshal(frame["requestId"], &requestID)
				if kind != "expression.delivery" {
					continue
				}
				var result session.ExpressionDeliveryResult
				_ = json.Unmarshal(frame["deliveryResult"], &result)
				deliveryReceived <- result
				_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := session.New(session.Options{Endpoint: server.URL, Token: "desktop-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewCoreService()
	service.client, service.socket, service.conversation, service.visualCache = client, socket, "c1", cache
	beat := desktopBeat{
		BeatID: "b1",
		Part: &session.ExpressionPart{
			Kind: session.ExpressionSticker,
			Sticker: &session.StickerReference{
				ID: "missing", Description: "不存在", MIMEType: "image/png",
			},
		},
	}
	service.prepareDesktopSticker(socket, session.TurnEvent{ConversationID: "c1", TurnID: "t1"}, &beat)
	if !beat.StickerUnavailable || beat.StickerURL != "" || beat.StickerError != "无法从 Core 读取表情包" {
		t.Fatalf("failed sticker beat = %#v", beat)
	}
	select {
	case result := <-deliveryReceived:
		if result.Status != session.ExpressionDeliveryFailed || result.ErrorMessage != "无法从 Core 读取表情包" {
			t.Fatalf("delivery result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive failed Desktop delivery result")
	}
}

func TestForwardTurnEventsClearsActiveWhenStreamCloses(t *testing.T) {
	service := NewCoreService()
	service.socket = nil
	turns := make(chan desktopTurnEvent, 2)
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:turn" {
			turns <- payload.(desktopTurnEvent)
		}
	})
	events := make(chan session.TurnEvent)
	done := make(chan struct{})
	go func() {
		service.forwardTurnEvents(nil, "c1", events)
		close(done)
	}()
	events <- session.TurnEvent{
		ConversationID: "c1",
		TurnID:         "proactive-turn",
		State:          "responding",
		Payload:        json.RawMessage(`{"type":"state_changed"}`),
	}
	assertTurnTypes(t, turns, "state_changed")
	close(events)
	select {
	case event := <-turns:
		if event.Type != "stream.closed" {
			t.Fatalf("event type = %q, want stream.closed", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("stream closure did not emit terminal event")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("turn event forwarder did not stop")
	}
	if service.active || service.activeTurnID != "" {
		t.Fatalf("closed stream left active state = (%t, %q)", service.active, service.activeTurnID)
	}
}

func TestForwardTurnEventsTracksProactiveTurnUntilMatchingTerminal(t *testing.T) {
	service := NewCoreService()
	companion := &fakeWindow{x: 700, y: 350}
	bubble := &fakeWindow{}
	service.attachWindows(companion, nil, nil, bubble)
	turns := make(chan desktopTurnEvent, 4)
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:turn" {
			turns <- payload.(desktopTurnEvent)
		}
	})
	events := make(chan session.TurnEvent)
	done := make(chan struct{})
	go func() {
		service.forwardTurnEvents(nil, "c1", events)
		close(done)
	}()

	events <- session.TurnEvent{
		ConversationID: "c1",
		TurnID:         "proactive-turn",
		State:          "interpreting",
		Payload:        json.RawMessage(`{"type":"state_changed"}`),
	}
	assertTurnTypes(t, turns, "state_changed")
	service.mu.Lock()
	active, activeTurnID := service.active, service.activeTurnID
	service.mu.Unlock()
	if !active || activeTurnID != "proactive-turn" {
		t.Fatalf("proactive active state = (%t, %q), want (true, %q)", active, activeTurnID, "proactive-turn")
	}
	if !bubble.shown || bubble.x != 686 || bubble.y != 180 {
		t.Fatalf("proactive speech bubble shown=%t position=(%d, %d), want true at (686, 180)", bubble.shown, bubble.x, bubble.y)
	}

	events <- session.TurnEvent{
		ConversationID: "c1",
		TurnID:         "proactive-turn",
		State:          "gathering",
		Payload:        json.RawMessage(`{"type":"state_changed"}`),
	}
	assertTurnTypes(t, turns, "state_changed")
	if bubble.showCount != 1 {
		t.Fatalf("same proactive turn showed speech bubble %d times, want 1", bubble.showCount)
	}

	events <- session.TurnEvent{
		ConversationID: "c1",
		TurnID:         "older-turn",
		State:          "completed",
		Payload:        json.RawMessage(`{"type":"completed"}`),
	}
	assertTurnTypes(t, turns, "completed")
	service.mu.Lock()
	active, activeTurnID = service.active, service.activeTurnID
	service.mu.Unlock()
	if !active || activeTurnID != "proactive-turn" {
		t.Fatalf("unrelated terminal changed active state to (%t, %q)", active, activeTurnID)
	}

	events <- session.TurnEvent{
		ConversationID: "c1",
		TurnID:         "proactive-turn",
		State:          "completed",
		Payload:        json.RawMessage(`{"type":"completed"}`),
	}
	assertTurnTypes(t, turns, "completed")
	service.mu.Lock()
	active, activeTurnID = service.active, service.activeTurnID
	service.mu.Unlock()
	if active || activeTurnID != "" {
		t.Fatalf("matching terminal left active state = (%t, %q)", active, activeTurnID)
	}

	close(events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("turn event forwarder did not stop")
	}
}

func TestForwardTurnEventsDropsStaleSocketBeforeStickerDelivery(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	staleDelivery := make(chan struct{}, 1)
	staleContentRead := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stickers/sticker-1/content" {
			staleContentRead <- struct{}{}
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write([]byte("GIF89a"))
			return
		}
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
		_ = conn.WriteJSON(map[string]any{"type": "ready"})
		for {
			var frame map[string]json.RawMessage
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			var kind, requestID string
			_ = json.Unmarshal(frame["type"], &kind)
			_ = json.Unmarshal(frame["requestId"], &requestID)
			if kind != "expression.delivery" {
				continue
			}
			staleDelivery <- struct{}{}
			_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
		}
	}))
	defer server.Close()

	client, err := session.New(session.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	staleSocket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer staleSocket.Close()

	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewCoreService()
	service.client = client
	service.socket = &session.SessionSocket{}
	service.visualCache = cache
	staleEmission := make(chan struct{}, 1)
	service.attachEmitter(func(name string, _ any) {
		if name == "desktop:turn" {
			staleEmission <- struct{}{}
		}
	})
	events := make(chan session.TurnEvent, 1)
	done := make(chan struct{})
	go func() {
		service.forwardTurnEvents(staleSocket, "c1", events)
		close(done)
	}()
	events <- session.TurnEvent{
		ConversationID: "c1",
		TurnID:         "stale-turn",
		State:          "responding",
		Payload:        json.RawMessage(`{"type":"beat.ready","beatId":"b1","kind":"final","visualState":"idle","part":{"kind":"sticker","visualState":"idle","sticker":{"id":"sticker-1","description":"开心","mimeType":"image/gif"}}}`),
	}
	close(events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale turn event forwarder did not stop")
	}
	select {
	case <-staleDelivery:
		t.Fatal("stale socket reported a Desktop sticker delivery result")
	default:
	}
	select {
	case <-staleContentRead:
		t.Fatal("stale socket requested Desktop sticker content")
	default:
	}
	cache.mu.RLock()
	stickerCount := len(cache.stickers)
	cache.mu.RUnlock()
	if stickerCount != 0 {
		t.Fatalf("stale socket cached %d Desktop stickers, want 0", stickerCount)
	}
	select {
	case <-staleEmission:
		t.Fatal("stale socket emitted a Desktop turn event")
	default:
	}
}

func TestDesktopTurnStateClassification(t *testing.T) {
	for _, state := range []string{"interpreting", "gathering", "planning", "responding"} {
		if !isDesktopTurnActive(state) || isDesktopTurnTerminal(state) {
			t.Fatalf("state %q was not classified as active only", state)
		}
	}
	for _, state := range []string{"completed", "failed", "interrupted"} {
		if isDesktopTurnActive(state) || !isDesktopTurnTerminal(state) {
			t.Fatalf("state %q was not classified as terminal only", state)
		}
	}
	for _, state := range []string{"", "unknown"} {
		if isDesktopTurnActive(state) || isDesktopTurnTerminal(state) {
			t.Fatalf("state %q was unexpectedly classified", state)
		}
	}
}

func serviceStatusFixture() session.Status {
	return session.Status{
		Bootstrap: json.RawMessage(`{}`), ConfigRoot: "test", WebSearch: json.RawMessage(`{}`), SemanticEmbedding: json.RawMessage(`{}`),
		Database: session.DependencyStatus{Ready: true, Mode: "test"}, SecretKey: session.DependencyStatus{Ready: true, Mode: "test"},
	}
}

func serviceCharacterFixture() session.CharacterRecord {
	return session.CharacterRecord{CharacterID: "character-1", Name: "Test", Appearance: session.CharacterAppearance{Status: "assigned", Visual: &session.VisualManifest{SchemaVersion: 2, PackID: "fairy.test", Renderer: "state_images", Frame: session.VisualFrame{Width: 16, Height: 16}, Scale: 1, Anchor: session.VisualAnchor{X: 8, Y: 16}, States: []session.VisualState{{ID: "idle", ImagePath: "images/idle.png"}}}}}
}

func writeServiceFixtureJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fixture: %v", err)
	}
}

func writeTurnEventFixture(conn *websocket.Conn, turnID string, sequence uint64, state, payload string) {
	event := session.TurnEvent{ConversationID: "c1", TurnID: turnID, Sequence: sequence, State: state, Payload: json.RawMessage(payload)}
	_ = conn.WriteJSON(map[string]any{"type": "turn.event", "conversationId": "c1", "event": event})
}

func assertTurnTypes(t *testing.T, turns <-chan desktopTurnEvent, want ...string) {
	t.Helper()
	for _, expected := range want {
		select {
		case event := <-turns:
			if event.Type != expected {
				t.Fatalf("turn event = %q, want %q", event.Type, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("did not receive %q turn event", expected)
		}
	}
}

func ptr[T any](value T) *T { return &value }
