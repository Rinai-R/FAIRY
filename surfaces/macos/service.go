package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"fairy/coreclient"
	"fairy/interaction"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var installationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type ConnectionState struct {
	Endpoint    string `json:"endpoint"`
	EndpointKey string `json:"endpointKey"`
	HasToken    bool   `json:"hasToken"`
}

type SessionState struct {
	Connection ConnectionState                `json:"connection"`
	Session    coreclient.OpenSessionResponse `json:"session"`
	Messages   []coreclient.MessageRecord     `json:"messages"`
	Character  coreclient.CharacterRecord     `json:"character"`
	Visuals    []VisualAsset                  `json:"visuals"`
}

type VisualAsset struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	DataURL     string `json:"dataUrl"`
}

type AppService struct {
	mu           sync.Mutex
	ctx          context.Context
	tokens       TokenStore
	client       *coreclient.Client
	socket       *coreclient.SessionSocket
	conversation string
	active       bool
	activeTurnID string
	emit         func(any)
}

func NewAppService(tokens TokenStore) *AppService {
	return &AppService{tokens: tokens}
}

func (s *AppService) Startup(ctx context.Context) {
	s.ctx = ctx
	s.emit = func(event any) { runtime.EventsEmit(ctx, "turn:event", event) }
}

func (s *AppService) Shutdown(context.Context) {
	s.mu.Lock()
	socket := s.socket
	s.socket = nil
	s.active = false
	s.activeTurnID = ""
	s.mu.Unlock()
	if socket != nil {
		_ = socket.Close()
	}
}

func ValidateEndpoint(raw string) (string, error) {
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
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("remote Core endpoints require HTTPS; HTTP is allowed only for loopback")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func EnsureInstallationKey(existing string) (string, error) {
	if existing != "" {
		if !installationKeyPattern.MatchString(existing) {
			return "", errors.New("installation key is invalid")
		}
		return existing, nil
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("generating installation key failed")
	}
	return "macos-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *AppService) Connection(endpoint, endpointKey string) (ConnectionState, error) {
	state, err := validatedConnectionState(endpoint, endpointKey)
	if err != nil {
		return ConnectionState{}, err
	}
	if s == nil || s.tokens == nil {
		return ConnectionState{}, errors.New("macOS Keychain is unavailable")
	}
	_, err = s.tokens.Get()
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return ConnectionState{}, errors.New("reading Core token from macOS Keychain failed")
	}
	state.HasToken = err == nil
	return state, nil
}

func (s *AppService) SaveConnection(endpoint, token, endpointKey string) (ConnectionState, error) {
	state, err := validatedConnectionState(endpoint, endpointKey)
	if err != nil {
		return ConnectionState{}, err
	}
	if token != "" && token != strings.TrimSpace(token) {
		return ConnectionState{}, errors.New("Core token must contain no surrounding whitespace")
	}
	if s == nil || s.tokens == nil {
		return ConnectionState{}, errors.New("macOS Keychain is unavailable")
	}
	if token == "" {
		return s.Connection(state.Endpoint, state.EndpointKey)
	}
	if err := s.tokens.Set(token); err != nil {
		return ConnectionState{}, errors.New("saving Core token to macOS Keychain failed")
	}
	state.HasToken = true
	return state, nil
}

func validatedConnectionState(endpoint, endpointKey string) (ConnectionState, error) {
	endpoint, err := ValidateEndpoint(endpoint)
	if err != nil {
		return ConnectionState{}, err
	}
	endpointKey, err = EnsureInstallationKey(endpointKey)
	if err != nil {
		return ConnectionState{}, err
	}
	return ConnectionState{Endpoint: endpoint, EndpointKey: endpointKey}, nil
}

func (s *AppService) Connect(endpoint, endpointKey string) (SessionState, error) {
	state, err := s.Connection(endpoint, endpointKey)
	if err != nil {
		return SessionState{}, err
	}
	token, err := s.tokens.Get()
	if errors.Is(err, ErrTokenNotFound) {
		token = ""
	} else if err != nil {
		return SessionState{}, errors.New("reading Core token from macOS Keychain failed")
	}
	client, err := coreclient.New(coreclient.Options{Endpoint: state.Endpoint, Token: token})
	if err != nil {
		return SessionState{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := client.Status(ctx); err != nil {
		return SessionState{}, err
	}
	socket, err := client.DialSession(ctx)
	if err != nil {
		return SessionState{}, err
	}
	closeSocket := true
	defer func() {
		if closeSocket {
			_ = socket.Close()
		}
	}()
	session, err := socket.OpenSession(ctx, coreclient.OpenSessionRequest{
		Endpoint: interaction.EndpointDesktop, EndpointKey: state.EndpointKey,
		Interaction: interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied},
	})
	if err != nil {
		return SessionState{}, err
	}
	events, err := socket.Watch(ctx, session.ConversationID)
	if err != nil {
		return SessionState{}, err
	}
	page, err := client.ListMessages(ctx, session.ConversationID, 0, 200)
	if err != nil {
		return SessionState{}, err
	}
	catalog, err := client.ListCharacters(ctx)
	if err != nil {
		return SessionState{}, err
	}
	if catalog.Active == nil || catalog.Active.CharacterID != session.CharacterID || catalog.Active.Appearance.Status != "assigned" || catalog.Active.Appearance.Visual == nil {
		return SessionState{}, errors.New("active character has no assigned visual pack")
	}
	visuals, err := loadVisualAssets(ctx, client, *catalog.Active)
	if err != nil {
		return SessionState{}, err
	}
	s.mu.Lock()
	previous := s.socket
	s.client = client
	s.socket = socket
	s.conversation = session.ConversationID
	s.active = false
	s.activeTurnID = ""
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	closeSocket = false
	go s.forwardTurnEvents(socket, session.ConversationID, events)
	return SessionState{Connection: state, Session: session, Messages: page.Messages, Character: *catalog.Active, Visuals: visuals}, nil
}

func loadVisualAssets(ctx context.Context, client *coreclient.Client, record coreclient.CharacterRecord) ([]VisualAsset, error) {
	manifest := record.Appearance.Visual
	if manifest == nil || manifest.PackID == "" || len(manifest.States) == 0 {
		return nil, errors.New("active character visual manifest is empty")
	}
	assets := make([]VisualAsset, 0, len(manifest.States))
	for _, state := range manifest.States {
		assetPath, err := visualAssetPath(manifest.PackID, state.ImagePath)
		if err != nil {
			return nil, fmt.Errorf("visual state %q: %w", state.ID, err)
		}
		png, err := client.VisualAsset(ctx, manifest.PackID, assetPath)
		if err != nil {
			return nil, fmt.Errorf("loading visual state %q: %w", state.ID, err)
		}
		assets = append(assets, VisualAsset{ID: state.ID, Description: state.Description, DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
	}
	return assets, nil
}

func visualAssetPath(packID, imagePath string) (string, error) {
	if parsed, err := url.Parse(imagePath); err == nil && parsed.Scheme == "fairy-character" {
		prefix := "/" + packID + "/"
		if !strings.HasPrefix(parsed.Path, prefix) {
			return "", errors.New("visual image path does not match pack")
		}
		return strings.TrimPrefix(parsed.Path, prefix), nil
	}
	if strings.TrimSpace(imagePath) == "" {
		return "", errors.New("visual image path is empty")
	}
	return imagePath, nil
}

func (s *AppService) Send(input string, speechEnabled bool) error {
	if input == "" || input != strings.TrimSpace(input) {
		return errors.New("message must be non-empty and contain no surrounding whitespace")
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return errors.New("a turn is already active")
	}
	socket, conversation := s.socket, s.conversation
	s.active = true
	s.activeTurnID = ""
	s.mu.Unlock()
	if socket == nil || conversation == "" {
		s.clearActive()
		return errors.New("Core session is not connected")
	}
	_, err := socket.SubmitTurn(context.Background(), conversation, coreclient.SubmitTurnRequest{Input: input, SpeechEnabled: speechEnabled})
	if err != nil {
		s.clearActive()
		s.emitTurnEvent(desktopTurnEvent{Type: "failed", Message: "提交对话失败：" + err.Error()})
	}
	return err
}

func (s *AppService) Cancel() error {
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

func (s *AppService) clearActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	s.activeTurnID = ""
}

type desktopTurnEvent struct {
	Type      string `json:"type"`
	TurnEvent struct {
		State string `json:"state"`
	} `json:"turnEvent"`
	Beat    *desktopBeat `json:"beat,omitempty"`
	Failure *struct {
		Message string `json:"message"`
	} `json:"failure,omitempty"`
	Message string `json:"message,omitempty"`
}

type desktopBeat struct {
	Kind        string `json:"kind"`
	DisplayText string `json:"displayText"`
	VisualState string `json:"visualState"`
}

func (s *AppService) forwardTurnEvents(socket *coreclient.SessionSocket, conversation string, events <-chan coreclient.TurnEvent) {
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
			s.active = false
			s.activeTurnID = ""
		}
		s.mu.Unlock()
		if current {
			s.emitTurnEvent(converted)
		}
	}
	s.mu.Lock()
	current := s.socket == socket
	active := s.active
	if current {
		s.socket = nil
		s.active = false
		s.activeTurnID = ""
	}
	s.mu.Unlock()
	if current && active {
		s.emitTurnEvent(desktopTurnEvent{Type: "stream.closed", Message: "与 Core 的会话连接已断开"})
	}
}

func decodeDesktopTurnEvent(event coreclient.TurnEvent) desktopTurnEvent {
	converted := desktopTurnEvent{Type: "state_changed"}
	converted.TurnEvent.State = event.State
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
			converted.Failure = &payload.Error
		}
	}
	return converted
}

func (s *AppService) emitTurnEvent(event desktopTurnEvent) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit(event)
	}
}
