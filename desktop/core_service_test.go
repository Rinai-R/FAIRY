package main

import (
	"context"
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
	width, height int
	shown, hidden bool
	visible       bool
	focused       bool
	showCount     int
	positionCalls int
	sizeCalls     int
	moveCalls     int
}

type fakeWindowRelation struct {
	attachCount int
	detachCount int
	attached    bool
	attachX     int
	attachY     int
	attachErr   error
	detachErr   error
}

func (r *fakeWindowRelation) Attach(_ application.Window, child application.Window) error {
	r.attachCount++
	if r.attachErr != nil {
		return r.attachErr
	}
	r.attached = true
	r.attachX, r.attachY = child.Position()
	return nil
}

func (r *fakeWindowRelation) Detach(application.Window, application.Window) error {
	r.detachCount++
	if r.detachErr != nil {
		return r.detachErr
	}
	r.attached = false
	return nil
}

func useFakeWindowRelation(service *CoreService) *fakeWindowRelation {
	relation := &fakeWindowRelation{}
	service.windowLink = relation
	return relation
}

func (w *fakeWindow) Position() (int, int) {
	w.positionCalls++
	return w.x, w.y
}
func (w *fakeWindow) Size() (int, int) {
	w.sizeCalls++
	return w.width, w.height
}
func (w *fakeWindow) SetPosition(x, y int) {
	w.moveCalls++
	w.x, w.y = x, y
}
func (w *fakeWindow) SetSize(width, height int) application.Window {
	w.sizeCalls++
	w.width, w.height = width, height
	return w
}
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
	panel := &fakeWindow{width: controlPanelWidth, height: controlPanelHeight}
	history := &fakeWindow{}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
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
	if panel.x != 272 || panel.y != 397 {
		t.Fatalf("settings window position = (%d, %d), want (272, 397)", panel.x, panel.y)
	}
	if !relation.attached || relation.attachCount != 1 || relation.attachX != 272 || relation.attachY != 397 {
		t.Fatalf("settings relation = %#v, want one attach after initial positioning", relation)
	}
}

func TestRepositionControlPanelUsesLatestCompanionPositionOnlyWhenRequested(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{width: 460, height: controlPanelHeight}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
	service.attachWindows(companion, panel, nil, nil)

	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() error = %v", err)
	}
	if panel.x != 232 || panel.y != 397 {
		t.Fatalf("initial settings position = (%d, %d), want (232, 397)", panel.x, panel.y)
	}

	companion.x, companion.y = 920, 480
	service.repositionControlPanel()
	if panel.x != 452 || panel.y != 527 {
		t.Fatalf("followed settings position = (%d, %d), want (452, 527)", panel.x, panel.y)
	}

	if err := service.CloseControlPanel(); err != nil {
		t.Fatalf("CloseControlPanel() error = %v", err)
	}
	companion.x, companion.y = 1100, 600
	service.repositionControlPanel()
	if panel.x != 452 || panel.y != 527 {
		t.Fatalf("closed settings moved to (%d, %d), want unchanged (452, 527)", panel.x, panel.y)
	}
	if relation.attached || relation.attachCount != 1 || relation.detachCount != 1 {
		t.Fatalf("settings relation = %#v, want one attach and one detach", relation)
	}
}

func TestOriginalCompanionMovePathRepositionsOnlyLightweightWindows(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{width: 460, height: controlPanelHeight}
	history := &fakeWindow{}
	bubble := &fakeWindow{}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
	service.attachWindows(companion, panel, history, bubble)
	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() error = %v", err)
	}
	companion.positionCalls = 0
	panel.moveCalls, history.moveCalls, bubble.moveCalls = 0, 0, 0
	companion.x, companion.y = 920, 480

	// The heavyweight settings WebView follows through its native parent-child
	// relationship and is intentionally absent from the WindowDidMove hot path.
	service.RepositionSpeechBubble()
	service.RepositionHistory()

	if companion.positionCalls != 2 {
		t.Fatalf("original move path read companion position %d times, want 2", companion.positionCalls)
	}
	if panel.moveCalls != 0 || history.moveCalls != 1 || bubble.moveCalls != 1 {
		t.Fatalf("move calls panel=%d history=%d bubble=%d, want 0, 1, 1", panel.moveCalls, history.moveCalls, bubble.moveCalls)
	}
	if panel.x != 232 || panel.y != 397 {
		t.Fatalf("settings moved with companion to (%d, %d), want original (232, 397)", panel.x, panel.y)
	}
	if history.x != 580 || history.y != 456 {
		t.Fatalf("history position = (%d, %d), want (580, 456)", history.x, history.y)
	}
	if bubble.x != 906 || bubble.y != 310 {
		t.Fatalf("bubble position = (%d, %d), want (906, 310)", bubble.x, bubble.y)
	}
	if !relation.attached || relation.attachCount != 1 || relation.detachCount != 0 {
		t.Fatalf("movement changed native settings relation: %#v", relation)
	}
}

func TestControlPanelResizeRefreshesCachedWidth(t *testing.T) {
	companion := &fakeWindow{x: 900, y: 420}
	panel := &fakeWindow{width: controlPanelWidth, height: controlPanelHeight}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
	service.attachWindows(companion, panel, nil, nil)
	if err := service.OpenControlPanel(); err != nil {
		t.Fatalf("OpenControlPanel() error = %v", err)
	}

	panel.width = 520
	service.refreshControlPanelWidth()
	companion.positionCalls, panel.sizeCalls, panel.moveCalls = 0, 0, 0
	companion.x, companion.y = 1040, 510
	service.repositionControlPanel()

	if companion.positionCalls != 1 || panel.sizeCalls != 0 || panel.moveCalls != 1 {
		t.Fatalf("resize reposition calls position=%d size=%d move=%d, want 1, 0, 1", companion.positionCalls, panel.sizeCalls, panel.moveCalls)
	}
	if panel.x != 512 || panel.y != 557 {
		t.Fatalf("resized settings position = (%d, %d), want (512, 557)", panel.x, panel.y)
	}
	if relation.attachCount != 1 || relation.detachCount != 0 || !relation.attached {
		t.Fatalf("resize changed native settings relation: %#v", relation)
	}
}

func TestOpenControlPanelClosesVisiblePanel(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
	service.attachWindows(companion, panel, nil, nil)
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
	if relation.attached || relation.attachCount != 1 || relation.detachCount != 1 {
		t.Fatalf("settings relation = %#v, want attach then detach", relation)
	}
}

func TestOpenHistoryHidesSettingsPanel(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{}
	history := &fakeWindow{}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
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
	if !relation.attached || relation.attachCount != 2 || relation.detachCount != 1 {
		t.Fatalf("settings relation = %#v, want attach, detach, reattach", relation)
	}
}

func TestOpenControlPanelFailsClosedWhenNativeAttachFails(t *testing.T) {
	companion := &fakeWindow{x: 700, y: 350}
	panel := &fakeWindow{width: controlPanelWidth, height: controlPanelHeight}
	service := NewCoreService()
	relation := useFakeWindowRelation(service)
	relation.attachErr = errors.New("native relation unavailable")
	service.attachWindows(companion, panel, nil, nil)

	err := service.OpenControlPanel()
	if err == nil || !strings.Contains(err.Error(), "native relation unavailable") {
		t.Fatalf("OpenControlPanel() error = %v, want native relation diagnostic", err)
	}
	if service.controlOpen || panel.shown || relation.attached {
		t.Fatalf("failed attach left settings active: controlOpen=%t shown=%t relation=%#v", service.controlOpen, panel.shown, relation)
	}
}

func useLoopbackSessionTransport(t *testing.T, service *CoreService, endpoint, token string) {
	t.Helper()
	if service.profileDir == nil {
		useTempProfile(t, service)
	}
	service.openTransport = func(ctx context.Context) (sessionPlane, sessionAssets, CoreSettings, error) {
		client, err := session.New(session.Options{Endpoint: endpoint, Token: token})
		if err != nil {
			return nil, nil, CoreSettings{}, err
		}
		socket, err := client.DialSession(ctx)
		if err != nil {
			return nil, nil, CoreSettings{}, err
		}
		return socket, client, CoreSettings{}, nil
	}
}

type scriptedSessionPlane struct {
	opened     session.OpenSessionResponse
	events     chan session.TurnEvent
	submits    []string
	deliveries []session.ExpressionDeliveryResult
}

func (s *scriptedSessionPlane) OpenSession(context.Context, session.OpenSessionRequest) (session.OpenSessionResponse, error) {
	return s.opened, nil
}
func (s *scriptedSessionPlane) Watch(context.Context, string) (<-chan session.TurnEvent, error) {
	if s.events == nil {
		s.events = make(chan session.TurnEvent)
	}
	return s.events, nil
}
func (s *scriptedSessionPlane) SubmitTurn(_ context.Context, _ string, request session.SubmitTurnRequest) (session.SubmitTurnResponse, error) {
	s.submits = append(s.submits, request.Input)
	return session.SubmitTurnResponse{}, nil
}
func (s *scriptedSessionPlane) CancelTurn(context.Context, string, string) error {
	return nil
}
func (s *scriptedSessionPlane) ReportExpressionDelivery(_ context.Context, result session.ExpressionDeliveryResult) error {
	s.deliveries = append(s.deliveries, result)
	return nil
}
func (s *scriptedSessionPlane) ObserveDesktop(context.Context, string, session.DesktopObservation) (session.DesktopObservationResponse, error) {
	return session.DesktopObservationResponse{}, nil
}
func (s *scriptedSessionPlane) SetDesktopCaptureHandler(func(context.Context, session.DesktopCaptureRequest) session.DesktopCaptureResult) error {
	return nil
}
func (s *scriptedSessionPlane) Close() error { return nil }

type scriptedSessionAssets struct {
	catalog  session.CharacterCatalog
	page     session.MessagePage
	stickers map[string]session.StickerContent
	visuals  map[string][]byte
}

func (s scriptedSessionAssets) ListCharacters(context.Context) (session.CharacterCatalog, error) {
	return s.catalog, nil
}
func (s scriptedSessionAssets) ListMessages(context.Context, string, uint64, int) (session.MessagePage, error) {
	return s.page, nil
}
func (s scriptedSessionAssets) ReadStickerContent(_ context.Context, id string) (session.StickerContent, error) {
	content, ok := s.stickers[id]
	if !ok {
		return session.StickerContent{}, errors.New("sticker was not found")
	}
	return content, nil
}
func (s scriptedSessionAssets) VisualAsset(_ context.Context, packID, assetPath string) ([]byte, error) {
	image, ok := s.visuals[packID+"/"+assetPath]
	if !ok {
		return nil, errors.New("visual asset was not found")
	}
	return image, nil
}

type scriptedOwnedRuntime struct {
	fakeOwnedRuntime
	plane  sessionPlane
	assets sessionAssets
}

func (s *scriptedOwnedRuntime) OpenSessionTransport() (sessionPlane, sessionAssets, error) {
	return s.plane, s.assets, nil
}

func TestConnectUsesInProcessFacadeWithoutHTTP(t *testing.T) {
	character := serviceCharacterFixture()
	plane := &scriptedSessionPlane{opened: session.OpenSessionResponse{ConversationID: "c1", CharacterID: character.CharacterID}}
	assets := scriptedSessionAssets{
		catalog: session.CharacterCatalog{Characters: []session.CharacterRecord{character}, Active: ptr(character)},
		page:    session.MessagePage{Messages: []session.MessageRecord{{ID: "m1"}}},
		visuals: map[string][]byte{"fairy.test/images/idle.png": testPNG},
	}
	runtime := &scriptedOwnedRuntime{plane: plane, assets: assets}
	service := NewCoreService()
	useTempProfile(t, service)
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	service.openEdge = func(context.Context) (ownedRuntime, error) { return runtime, nil }
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	defer service.ServiceShutdown()

	sessionState, err := service.Connect()
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if sessionState.ConversationID != "c1" || sessionState.Character.CharacterID != character.CharacterID {
		t.Fatalf("Connect() session = %#v", sessionState)
	}
	if !sessionState.Settings.Ready || sessionState.Settings.ProfileDir == "" || sessionState.Settings.CharacterName != character.Name {
		t.Fatalf("Connect() settings = %#v", sessionState.Settings)
	}
	encoded, err := json.Marshal(sessionState.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"endpoint"`, "endpointKey", "hasToken", "http://", "token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("in-process Connect leaked %q: %s", forbidden, encoded)
		}
	}
	if err := service.Send("hello"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(plane.submits) != 1 || plane.submits[0] != "hello" {
		t.Fatalf("SubmitTurn inputs = %v", plane.submits)
	}
	messages, err := service.RecentMessages()
	if err != nil {
		t.Fatalf("RecentMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "m1" {
		t.Fatalf("RecentMessages() = %#v", messages)
	}
}

func TestConnectFailsClosedWithoutEdgeAndDoesNotMentionLegacyHTTP(t *testing.T) {
	service := NewCoreService()
	_, err := service.Connect()
	if err == nil || !strings.Contains(err.Error(), "edge runtime is not started") {
		t.Fatalf("Connect() error = %v, want edge runtime missing", err)
	}
	message := strings.ToLower(err.Error())
	for _, forbidden := range []string{"bearer", "127.0.0.1:8787", "websocket", "http://"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("Connect() error mentioned %q: %v", forbidden, err)
		}
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
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	useLoopbackSessionTransport(t, service, server.URL, "desktop-test-token")
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
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	useLoopbackSessionTransport(t, service, server.URL, "desktop-test-token")
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
	service.newCache = func() (*visualCache, error) { return newVisualCacheAt(t.TempDir()) }
	useLoopbackSessionTransport(t, service, server.URL, "desktop-test-token")
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
	service.assets, service.socket, service.conversation, service.visualCache = client, socket, "c1", cache
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
	service.assets = client
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
