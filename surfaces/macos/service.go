package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"fairy-surfaces/turnclient"
	"fairy/coreclient"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var installationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type ConnectionState struct {
	Endpoint   string `json:"endpoint"`
	SurfaceKey string `json:"surfaceKey"`
	HasToken   bool   `json:"hasToken"`
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
	conversation string
	activeCancel context.CancelFunc
}

func NewAppService(tokens TokenStore) *AppService {
	return &AppService{tokens: tokens}
}

func (s *AppService) Startup(ctx context.Context) { s.ctx = ctx }

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

func (s *AppService) Connection(endpoint, surfaceKey string) (ConnectionState, error) {
	state, err := validatedConnectionState(endpoint, surfaceKey)
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

func (s *AppService) SaveConnection(endpoint, token, surfaceKey string) (ConnectionState, error) {
	state, err := validatedConnectionState(endpoint, surfaceKey)
	if err != nil {
		return ConnectionState{}, err
	}
	if token == "" || token != strings.TrimSpace(token) {
		return ConnectionState{}, errors.New("Core token must be non-empty and contain no surrounding whitespace")
	}
	if s == nil || s.tokens == nil {
		return ConnectionState{}, errors.New("macOS Keychain is unavailable")
	}
	if err := s.tokens.Set(token); err != nil {
		return ConnectionState{}, errors.New("saving Core token to macOS Keychain failed")
	}
	state.HasToken = true
	return state, nil
}

func validatedConnectionState(endpoint, surfaceKey string) (ConnectionState, error) {
	endpoint, err := ValidateEndpoint(endpoint)
	if err != nil {
		return ConnectionState{}, err
	}
	surfaceKey, err = EnsureInstallationKey(surfaceKey)
	if err != nil {
		return ConnectionState{}, err
	}
	return ConnectionState{Endpoint: endpoint, SurfaceKey: surfaceKey}, nil
}

func (s *AppService) Connect(endpoint, surfaceKey string) (SessionState, error) {
	state, err := s.Connection(endpoint, surfaceKey)
	if err != nil {
		return SessionState{}, err
	}
	token, err := s.tokens.Get()
	if err != nil {
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
	session, err := client.OpenSession(ctx, coreclient.OpenSessionRequest{Surface: "desktop", SurfaceKey: state.SurfaceKey})
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
	s.client = client
	s.conversation = session.ConversationID
	s.mu.Unlock()
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
	if s.activeCancel != nil {
		s.mu.Unlock()
		return errors.New("a turn is already active")
	}
	client, conversation := s.client, s.conversation
	turnCtx, cancel := context.WithCancel(context.Background())
	s.activeCancel = cancel
	s.mu.Unlock()
	if client == nil || conversation == "" {
		cancel()
		s.clearActive(cancel)
		return errors.New("Core session is not connected")
	}
	defer func() { cancel(); s.clearActive(cancel) }()
	runner, _ := turnclient.New(client, 15*time.Second)
	_, err := runner.Run(turnCtx, turnclient.Request{ConversationID: conversation, Input: input, SpeechEnabled: speechEnabled, Surface: "desktop"}, func(event turnclient.Event) error {
		if s.ctx != nil {
			runtime.EventsEmit(s.ctx, "turn:event", event)
		}
		return nil
	})
	return err
}

func (s *AppService) Cancel() error {
	s.mu.Lock()
	cancel := s.activeCancel
	s.mu.Unlock()
	if cancel == nil {
		return errors.New("no active turn")
	}
	cancel()
	return nil
}

func (s *AppService) clearActive(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeCancel != nil {
		s.activeCancel = nil
	}
}
