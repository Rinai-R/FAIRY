package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"fairy/coreclient"
	"fairy/interaction"
	keychain "github.com/keybase/go-keychain"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var installationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type CoreSettings struct {
	Endpoint    string `json:"endpoint"`
	EndpointKey string `json:"endpointKey"`
	HasToken    bool   `json:"hasToken"`
}

type CoreSession struct {
	Settings       CoreSettings               `json:"settings"`
	ConversationID string                     `json:"conversationId"`
	Character      coreclient.CharacterRecord `json:"character"`
	Messages       []coreclient.MessageRecord `json:"messages"`
}

type CoreService struct {
	tokens       tokenStore
	mu           sync.Mutex
	app          *application.App
	companion    application.Window
	controlPanel application.Window
	history      application.Window
	speechBubble application.Window
	controlOpen  bool
	historyOpen  bool
	client       *coreclient.Client
	socket       *coreclient.SessionSocket
	visualCache  *visualCache
	newCache     func() (*visualCache, error)
	conversation string
	active       bool
	activeTurnID string
	emit         func(string, any)
}

func NewCoreService() *CoreService {
	return &CoreService{tokens: systemTokenStore{}, newCache: newVisualCache}
}

// attachWindows is called only from the composition root after all product
// windows exist. Keeping the handles here lets the foot dock open the same
// dedicated settings Surface used by the historical desktop companion.
func (s *CoreService) attachWindows(companion, controlPanel, history, speechBubble application.Window) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.companion, s.controlPanel, s.history, s.speechBubble = companion, controlPanel, history, speechBubble
	s.controlOpen, s.historyOpen = false, false
}

func (s *CoreService) attachEmitter(emit func(string, any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit = emit
}

func (s *CoreService) attachApplication(app *application.App) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.app = app
}

func (s *CoreService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cache := s.visualCache
	s.mu.Unlock()
	if cache == nil {
		http.NotFound(w, r)
		return
	}
	cache.ServeHTTP(w, r)
}

func (s *CoreService) ServiceShutdown() error {
	s.mu.Lock()
	socket, cache := s.socket, s.visualCache
	s.socket, s.visualCache = nil, nil
	s.mu.Unlock()
	if socket != nil {
		_ = socket.Close()
	}
	return cache.Close()
}

func (s *CoreService) OpenControlPanel() error {
	s.mu.Lock()
	companion, panel, history := s.companion, s.controlPanel, s.history
	if panel == nil {
		s.mu.Unlock()
		return errors.New("Core settings window is unavailable")
	}
	controlOpen := s.controlOpen
	if controlOpen {
		s.controlOpen = false
	} else {
		s.controlOpen, s.historyOpen = true, false
	}
	s.mu.Unlock()
	if controlOpen {
		panel.Hide()
		s.emitControlPanelState(false)
		return nil
	}
	if history != nil {
		history.Hide()
		s.emitHistoryState(false)
	}
	if companion != nil {
		x, y := companion.Position()
		panel.SetPosition(x-348, y+47)
	}
	panel.Show()
	if companion != nil {
		companion.Focus()
	}
	s.emitControlPanelState(true)
	return nil
}

func (s *CoreService) CloseControlPanel() error {
	s.mu.Lock()
	panel := s.controlPanel
	s.controlOpen = false
	s.mu.Unlock()
	if panel != nil {
		panel.Hide()
	}
	s.emitControlPanelState(false)
	return nil
}

// OpenHistory keeps recent messages beside the pet rather than expanding or
// covering the companion window with the retired chat surface.
func (s *CoreService) OpenHistory() error {
	s.mu.Lock()
	companion, panel, history := s.companion, s.controlPanel, s.history
	if companion == nil || history == nil {
		s.mu.Unlock()
		return errors.New("history window is unavailable")
	}
	historyOpen := s.historyOpen
	if historyOpen {
		s.historyOpen = false
	} else {
		s.historyOpen, s.controlOpen = true, false
	}
	s.mu.Unlock()
	if historyOpen {
		history.Hide()
		s.emitHistoryState(false)
		return nil
	}
	if panel != nil {
		panel.Hide()
		s.emitControlPanelState(false)
	}
	s.RepositionHistory()
	history.Show()
	companion.Focus()
	s.emitHistoryState(true)
	return nil
}

func (s *CoreService) CloseHistory() {
	s.mu.Lock()
	history := s.history
	s.historyOpen = false
	s.mu.Unlock()
	if history != nil {
		history.Hide()
	}
	s.emitHistoryState(false)
}

func (s *CoreService) emitHistoryState(open bool) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit("desktop:history", map[string]bool{"open": open})
	}
}

func (s *CoreService) emitControlPanelState(open bool) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit("desktop:control-panel", map[string]bool{"open": open})
	}
}

func (s *CoreService) RepositionHistory() {
	s.mu.Lock()
	companion, history := s.companion, s.history
	s.mu.Unlock()
	if companion == nil || history == nil {
		return
	}
	x, y := companion.Position()
	history.SetPosition(x-340, y-24)
}

func (s *CoreService) RecentMessages() ([]coreclient.MessageRecord, error) {
	s.mu.Lock()
	client, conversation := s.client, s.conversation
	s.mu.Unlock()
	if client == nil || conversation == "" {
		return nil, errors.New("Core session is not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	page, err := client.ListMessages(ctx, conversation, 0, 20)
	if err != nil {
		return nil, err
	}
	return page.Messages, nil
}

func (s *CoreService) RepositionSpeechBubble() {
	s.mu.Lock()
	companion, bubble := s.companion, s.speechBubble
	s.mu.Unlock()
	if companion == nil || bubble == nil {
		return
	}
	x, y := companion.Position()
	bubble.SetPosition(x-14, y-170)
}

// HideSpeechBubble is called after the WebView finishes its local fade-out.
func (s *CoreService) HideSpeechBubble() {
	s.mu.Lock()
	bubble := s.speechBubble
	s.mu.Unlock()
	if bubble != nil {
		bubble.Hide()
	}
}

func (s *CoreService) SaveConnection(endpoint, token, endpointKey string) (CoreSettings, error) {
	settings, err := s.settings(endpoint, endpointKey)
	if err != nil {
		return CoreSettings{}, err
	}
	if token != "" && token != strings.TrimSpace(token) {
		return CoreSettings{}, errors.New("Core token must contain no surrounding whitespace")
	}
	if token != "" {
		if err := s.tokens.Set(token); err != nil {
			return CoreSettings{}, errors.New("saving Core token to macOS Keychain failed")
		}
		settings.HasToken = true
	}
	return settings, nil
}

func (s *CoreService) Connect(endpoint, endpointKey string) (CoreSession, error) {
	settings, err := s.settings(endpoint, endpointKey)
	if err != nil {
		return CoreSession{}, err
	}
	token, err := s.tokens.Get()
	if errors.Is(err, errTokenNotFound) {
		token = ""
	} else if err != nil {
		return CoreSession{}, errors.New("reading Core token from macOS Keychain failed")
	}
	client, err := coreclient.New(coreclient.Options{Endpoint: settings.Endpoint, Token: token})
	if err != nil {
		return CoreSession{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := client.Status(ctx); err != nil {
		return CoreSession{}, err
	}
	socket, err := client.DialSession(ctx)
	if err != nil {
		return CoreSession{}, err
	}
	closeSocket := true
	defer func() {
		if closeSocket {
			_ = socket.Close()
		}
	}()
	opened, err := socket.OpenSession(ctx, coreclient.OpenSessionRequest{Endpoint: interaction.EndpointDesktop, EndpointKey: settings.EndpointKey, Interaction: interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}})
	if err != nil {
		return CoreSession{}, err
	}
	events, err := socket.Watch(ctx, opened.ConversationID)
	if err != nil {
		return CoreSession{}, err
	}
	catalog, err := client.ListCharacters(ctx)
	if err != nil {
		return CoreSession{}, err
	}
	if catalog.Active == nil || catalog.Active.CharacterID != opened.CharacterID {
		return CoreSession{}, errors.New("active character is unavailable")
	}
	if catalog.Active.Appearance.Visual == nil {
		return CoreSession{}, errors.New("active character has no visual manifest")
	}
	cache, err := s.newCache()
	if err != nil {
		return CoreSession{}, err
	}
	localVisual, err := cache.Sync(ctx, client, *catalog.Active.Appearance.Visual)
	if err != nil {
		_ = cache.Close()
		return CoreSession{}, err
	}
	messages, err := client.ListMessages(ctx, opened.ConversationID, 0, 20)
	if err != nil {
		_ = cache.Close()
		return CoreSession{}, err
	}
	s.mu.Lock()
	previous, previousCache := s.socket, s.visualCache
	s.client, s.socket, s.conversation = client, socket, opened.ConversationID
	s.visualCache = cache
	s.active, s.activeTurnID = false, ""
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	if previousCache != nil {
		_ = previousCache.Close()
	}
	closeSocket = false
	go s.forwardTurnEvents(socket, opened.ConversationID, events)
	s.emitDesktopSession(messages.Messages)
	character := *catalog.Active
	character.Appearance.Visual = &localVisual
	return CoreSession{Settings: settings, ConversationID: opened.ConversationID, Character: character, Messages: messages.Messages}, nil
}

func (s *CoreService) Send(input string, speechEnabled bool) error {
	if input == "" || input != strings.TrimSpace(input) {
		return errors.New("message must be non-empty and contain no surrounding whitespace")
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return errors.New("a turn is already active")
	}
	socket, conversation := s.socket, s.conversation
	s.active, s.activeTurnID = true, ""
	s.mu.Unlock()
	if socket == nil || conversation == "" {
		s.clearActive()
		return errors.New("Core session is not connected")
	}
	// Emit waiting before SubmitTurn so the floating dots appear immediately and
	// before any harness events that the forwarder may deliver concurrently.
	s.emitTurnEvent(desktopTurnEvent{Type: "state_changed", State: "planning"})
	s.mu.Lock()
	bubble := s.speechBubble
	s.mu.Unlock()
	if bubble != nil {
		s.RepositionSpeechBubble()
		bubble.Show()
	}
	if _, err := socket.SubmitTurn(context.Background(), conversation, coreclient.SubmitTurnRequest{Input: input, SpeechEnabled: speechEnabled}); err != nil {
		s.clearActive()
		s.emitTurnEvent(desktopTurnEvent{Type: "failed", Message: "提交对话失败：" + err.Error()})
		return err
	}
	return nil
}

func (s *CoreService) Cancel() error {
	s.mu.Lock()
	socket, conversation, turnID := s.socket, s.conversation, s.activeTurnID
	s.mu.Unlock()
	if socket == nil || turnID == "" {
		return errors.New("no active turn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return socket.CancelTurn(ctx, conversation, turnID)
}

func (s *CoreService) clearActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active, s.activeTurnID = false, ""
}

type desktopTurnEvent struct {
	Type    string       `json:"type"`
	TurnID  string       `json:"turnId,omitempty"`
	State   string       `json:"state,omitempty"`
	Beat    *desktopBeat `json:"beat,omitempty"`
	Message string       `json:"message,omitempty"`
}

type desktopBeat struct {
	Kind        string `json:"kind"`
	DisplayText string `json:"displayText"`
	VisualState string `json:"visualState"`
}

func (s *CoreService) forwardTurnEvents(socket *coreclient.SessionSocket, conversation string, events <-chan coreclient.TurnEvent) {
	for event := range events {
		if event.ConversationID != conversation {
			continue
		}
		converted := decodeDesktopTurnEvent(event)
		s.mu.Lock()
		current := s.socket == socket
		if current && s.active && s.activeTurnID == "" {
			s.activeTurnID = event.TurnID
		}
		terminal := event.State == "completed" || event.State == "failed" || event.State == "interrupted"
		if current && terminal {
			s.active, s.activeTurnID = false, ""
		}
		s.mu.Unlock()
		if current {
			s.emitTurnEvent(converted)
		}
	}
	s.mu.Lock()
	current, active := s.socket == socket, s.active
	if current {
		s.socket = nil
		s.active, s.activeTurnID = false, ""
	}
	s.mu.Unlock()
	if current && active {
		s.emitTurnEvent(desktopTurnEvent{Type: "stream.closed", Message: "与 Core 的会话连接已断开"})
	}
}

func decodeDesktopTurnEvent(event coreclient.TurnEvent) desktopTurnEvent {
	converted := desktopTurnEvent{Type: "state_changed", TurnID: event.TurnID, State: event.State}
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(event.Payload, &envelope) == nil && envelope.Type != "" {
		converted.Type = envelope.Type
	}
	if converted.Type == "beat.ready" {
		var beat desktopBeat
		if json.Unmarshal(event.Payload, &beat) == nil {
			converted.Beat = &beat
		}
	}
	if converted.Type == "failed" {
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil {
			converted.Message = payload.Error.Message
		}
	}
	return converted
}

func (s *CoreService) emitTurnEvent(event desktopTurnEvent) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit("desktop:turn", event)
	}
}

func (s *CoreService) emitDesktopSession(messages []coreclient.MessageRecord) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit("desktop:session", map[string]any{"messages": messages})
	}
}

func (s *CoreService) settings(endpoint, endpointKey string) (CoreSettings, error) {
	endpoint, err := validateEndpoint(endpoint)
	if err != nil {
		return CoreSettings{}, err
	}
	if endpointKey == "" {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return CoreSettings{}, err
		}
		endpointKey = "macos-" + base64.RawURLEncoding.EncodeToString(raw)
	}
	if !installationKeyPattern.MatchString(endpointKey) {
		return CoreSettings{}, errors.New("installation key is invalid")
	}
	_, err = s.tokens.Get()
	if err != nil && !errors.Is(err, errTokenNotFound) {
		return CoreSettings{}, errors.New("reading Core token from macOS Keychain failed")
	}
	return CoreSettings{Endpoint: endpoint, EndpointKey: endpointKey, HasToken: err == nil}, nil
}

func validateEndpoint(raw string) (string, error) {
	if raw != strings.TrimSpace(raw) || raw == "" {
		return "", errors.New("Core endpoint must not be empty or contain surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("Core endpoint must be an absolute origin without credentials, path, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return strings.TrimSuffix(parsed.String(), "/"), nil
	}
	if parsed.Scheme != "http" || !isLoopback(parsed.Hostname()) {
		return "", errors.New("remote Core endpoints require HTTPS")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

var errTokenNotFound = errors.New("Core token is not present in macOS Keychain")

type tokenStore interface {
	Get() (string, error)
	Set(string) error
}
type systemTokenStore struct{}

func (systemTokenStore) Get() (string, error) {
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService("com.rinai.fairy.macos")
	item.SetAccount("core-api-token")
	item.SetMatchLimit(keychain.MatchLimitOne)
	item.SetReturnData(true)
	result, err := keychain.QueryItem(item)
	if err != nil {
		return "", err
	}
	if len(result) != 1 || len(result[0].Data) == 0 {
		return "", errTokenNotFound
	}
	return string(result[0].Data), nil
}
func (systemTokenStore) Set(token string) error {
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService("com.rinai.fairy.macos")
	item.SetAccount("core-api-token")
	item.SetData([]byte(token))
	item.SetAccessible(keychain.AccessibleAfterFirstUnlock)
	_ = keychain.DeleteItem(item)
	return keychain.AddItem(item)
}
