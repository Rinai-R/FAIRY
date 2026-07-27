package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/coreclient"

	"github.com/gorilla/websocket"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeWindow struct {
	application.Window
	x, y          int
	shown, hidden bool
	visible       bool
	focused       bool
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
	return w
}
func (w *fakeWindow) Hide() application.Window {
	w.hidden, w.visible = true, false
	return w
}
func (w *fakeWindow) IsVisible() bool { return w.visible }
func (w *fakeWindow) Focus()          { w.focused = true }

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
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status":
			writeServiceFixtureJSON(t, w, serviceStatusFixture())
		case "/v1/characters":
			writeServiceFixtureJSON(t, w, coreclient.CharacterCatalog{Characters: []coreclient.CharacterRecord{serviceCharacterFixture()}, Active: ptr(serviceCharacterFixture())})
		case "/v1/visual-assets/fairy.test/images/idle.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
		case "/v1/sessions/c1/messages":
			writeServiceFixtureJSON(t, w, coreclient.MessagePage{Messages: []coreclient.MessageRecord{}})
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
					_ = conn.WriteJSON(map[string]any{"type": "session.opened", "requestId": requestID, "conversationId": "c1", "characterId": "character-1", "endpoint": "desktop"})
				case "session.watch":
					_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID})
				case "turn.submit":
					writeTurnEventFixture(conn, "t1", 1, "responding", `{"type":"beat.ready","kind":"reply","displayText":"ok","visualState":"idle"}`)
					writeTurnEventFixture(conn, "t1", 2, "completed", `{"type":"completed"}`)
					_ = conn.WriteJSON(map[string]any{"type": "result", "requestId": requestID, "payload": json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"ok"}}`)})
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
	if err := service.Send("hello", false); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	assertTurnTypes(t, turns, "state_changed", "beat.ready", "completed")
	service.mu.Lock()
	active := service.active
	service.mu.Unlock()
	if active {
		t.Fatal("completed turn left service active")
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 1 {
		t.Fatalf("websocket connections = %d, want 1", connections)
	}
	if got, want := frameTypes, []string{"session.open", "session.watch", "turn.submit"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("socket frames = %v, want %v", got, want)
	}
}

func TestForwardTurnEventsClearsActiveWhenStreamCloses(t *testing.T) {
	service := NewCoreService()
	service.active = true
	service.socket = nil
	turns := make(chan desktopTurnEvent, 1)
	service.attachEmitter(func(name string, payload any) {
		if name == "desktop:turn" {
			turns <- payload.(desktopTurnEvent)
		}
	})
	events := make(chan coreclient.TurnEvent)
	close(events)
	service.forwardTurnEvents(nil, "c1", events)
	select {
	case event := <-turns:
		if event.Type != "stream.closed" {
			t.Fatalf("event type = %q, want stream.closed", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("stream closure did not emit terminal event")
	}
	if service.active {
		t.Fatal("closed stream left service active")
	}
}

func serviceStatusFixture() coreclient.Status {
	return coreclient.Status{
		Bootstrap: json.RawMessage(`{}`), ConfigRoot: "test", WebSearch: json.RawMessage(`{}`), SemanticEmbedding: json.RawMessage(`{}`),
		Database: coreclient.DependencyStatus{Ready: true, Mode: "test"}, Qdrant: coreclient.DependencyStatus{Ready: true, Mode: "test"}, SecretKey: coreclient.DependencyStatus{Ready: true, Mode: "test"},
	}
}

func serviceCharacterFixture() coreclient.CharacterRecord {
	return coreclient.CharacterRecord{CharacterID: "character-1", Name: "Test", Appearance: coreclient.CharacterAppearance{Status: "assigned", Visual: &coreclient.VisualManifest{SchemaVersion: 2, PackID: "fairy.test", Renderer: "state_images", Frame: coreclient.VisualFrame{Width: 16, Height: 16}, Scale: 1, Anchor: coreclient.VisualAnchor{X: 8, Y: 16}, States: []coreclient.VisualState{{ID: "idle", ImagePath: "images/idle.png"}}}}}
}

func writeServiceFixtureJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fixture: %v", err)
	}
}

func writeTurnEventFixture(conn *websocket.Conn, turnID string, sequence uint64, state, payload string) {
	event := coreclient.TurnEvent{ConversationID: "c1", TurnID: turnID, Sequence: sequence, State: state, Payload: json.RawMessage(payload)}
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
