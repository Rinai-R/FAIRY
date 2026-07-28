package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"fairy/coreclient"
	"fairy/session"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var installationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

const (
	desktopAudioMIME     = "audio/mpeg"
	desktopAudioFormat   = "mp3"
	maxDesktopAudioBytes = 2 * 1024 * 1024
)

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
	connections  connectionStore
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
	observation  *desktopObservationRuntime
	capture      *desktopCaptureRuntime
	privacy      session.DesktopPrivacyState
}

func NewCoreService() *CoreService {
	service := &CoreService{connections: newSystemConnectionStore(), newCache: newVisualCache, privacy: session.DesktopPrivacyProtected}
	service.capture, _ = newDesktopCaptureRuntime(newPlatformDesktopCapturer(), func() session.DesktopPrivacyState {
		service.mu.Lock()
		defer service.mu.Unlock()
		return service.privacy
	})
	return service
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
	socket, cache, observation := s.socket, s.visualCache, s.observation
	s.socket, s.visualCache = nil, nil
	s.observation = nil
	s.mu.Unlock()
	if observation != nil {
		observation.Stop()
	}
	if socket != nil {
		_ = socket.Close()
	}
	return cache.Close()
}

func (s *CoreService) SetDesktopObservationPrivacy(value string) error {
	privacy := session.DesktopPrivacyState(value)
	switch privacy {
	case session.DesktopPrivacyNormal, session.DesktopPrivacyLocked, session.DesktopPrivacyMeeting, session.DesktopPrivacyDoNotDisturb, session.DesktopPrivacyProtected:
	default:
		return errors.New("desktop observation privacy state is invalid")
	}
	s.mu.Lock()
	s.privacy = privacy
	s.mu.Unlock()
	return nil
}

func (s *CoreService) EnableDesktopObservation(intervalMinutes, idleMinutes int) error {
	if intervalMinutes < 1 || intervalMinutes > 60 || idleMinutes < 1 || idleMinutes > 240 {
		return errors.New("desktop observation interval or idle threshold is invalid")
	}
	s.mu.Lock()
	if s.observation != nil {
		s.mu.Unlock()
		return errors.New("desktop observation is already enabled")
	}
	if s.socket == nil || s.conversation == "" {
		s.mu.Unlock()
		return errors.New("Core session is not connected")
	}
	if s.privacy == session.DesktopPrivacyProtected {
		s.mu.Unlock()
		return errors.New("desktop observation privacy must be explicitly configured before enabling")
	}
	s.mu.Unlock()

	sampler, err := newMacOSIdleSampler(time.Duration(idleMinutes)*time.Minute, func() session.DesktopPrivacyState {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.privacy
	})
	if err != nil {
		return err
	}
	runtime, err := newDesktopObservationRuntime(sampler.Sample, func(ctx context.Context, observation session.DesktopObservation) error {
		s.mu.Lock()
		socket, conversation := s.socket, s.conversation
		s.mu.Unlock()
		if socket == nil || conversation == "" {
			return errors.New("Core session is not connected")
		}
		_, err := socket.ObserveDesktop(ctx, conversation, observation)
		return err
	}, observationSchedulerConfig{
		Interval: time.Duration(intervalMinutes) * time.Minute, DailyEvaluationLimit: 96, ConsecutiveFailureLimit: 3,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.observation != nil {
		s.mu.Unlock()
		return errors.New("desktop observation is already enabled")
	}
	s.observation = runtime
	s.mu.Unlock()
	if err := runtime.Start(context.Background()); err != nil {
		s.mu.Lock()
		if s.observation == runtime {
			s.observation = nil
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *CoreService) DisableDesktopObservation() {
	s.mu.Lock()
	runtime := s.observation
	s.observation = nil
	s.mu.Unlock()
	if runtime != nil {
		runtime.Stop()
	}
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
	endpoint, err := validateEndpoint(endpoint)
	if err != nil {
		return CoreSettings{}, err
	}
	if endpointKey == "" {
		endpointKey, err = generateInstallationKey()
		if err != nil {
			return CoreSettings{}, err
		}
	}
	if !installationKeyPattern.MatchString(endpointKey) {
		return CoreSettings{}, errors.New("installation key is invalid")
	}
	if token == "" {
		existing, loadErr := s.connections.Load()
		if errors.Is(loadErr, errConnectionNotFound) {
			return CoreSettings{}, errors.New("Core token is required when saving the first Desktop connection")
		}
		if loadErr != nil {
			return CoreSettings{}, fmt.Errorf("load existing Desktop Core connection: %w", loadErr)
		}
		token = existing.Token
	} else if token != strings.TrimSpace(token) {
		return CoreSettings{}, errors.New("Core token must contain no surrounding whitespace")
	}
	connection := desktopConnection{Endpoint: endpoint, EndpointKey: endpointKey, Token: token}
	if err := s.connections.Save(connection); err != nil {
		return CoreSettings{}, fmt.Errorf("save Desktop Core connection: %w", err)
	}
	return settingsForConnection(connection), nil
}

func (s *CoreService) ConnectionSettings() (CoreSettings, error) {
	connection, err := s.connections.Load()
	if errors.Is(err, errConnectionNotFound) {
		return CoreSettings{Endpoint: defaultCoreEndpoint}, nil
	}
	if err != nil {
		return CoreSettings{}, fmt.Errorf("load Desktop Core connection: %w", err)
	}
	return settingsForConnection(connection), nil
}

func (s *CoreService) Connect() (CoreSession, error) {
	connection, err := s.connections.Load()
	if errors.Is(err, errConnectionNotFound) {
		return CoreSession{}, errors.New("Core connection is not configured: open settings and save FAIRY_API_TOKEN")
	}
	if err != nil {
		return CoreSession{}, fmt.Errorf("load Desktop Core connection: %w", err)
	}
	settings := settingsForConnection(connection)
	client, err := coreclient.New(coreclient.Options{Endpoint: connection.Endpoint, Token: connection.Token})
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
	if err := socket.SetDesktopCaptureHandler(func(ctx context.Context, request coreclient.DesktopCaptureRequest) coreclient.DesktopCaptureResult {
		s.mu.Lock()
		capture := s.capture
		s.mu.Unlock()
		if capture == nil {
			return failedCaptureResult("capture_unavailable")
		}
		return capture.Handle(ctx, request)
	}); err != nil {
		return CoreSession{}, err
	}
	closeSocket := true
	defer func() {
		if closeSocket {
			_ = socket.Close()
		}
	}()
	opened, err := socket.OpenSession(ctx, coreclient.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: settings.EndpointKey,
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied,
		},
		OutputCapabilities: session.OutputCapabilities{Sticker: true},
	})
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

func isDesktopTurnActive(state string) bool {
	switch state {
	case "interpreting", "gathering", "planning", "responding":
		return true
	default:
		return false
	}
}

func isDesktopTurnTerminal(state string) bool {
	switch state {
	case "completed", "failed", "interrupted":
		return true
	default:
		return false
	}
}

type desktopTurnEvent struct {
	Type    string       `json:"type"`
	TurnID  string       `json:"turnId,omitempty"`
	State   string       `json:"state,omitempty"`
	Beat    *desktopBeat `json:"beat,omitempty"`
	Message string       `json:"message,omitempty"`
}

type desktopBeat struct {
	BeatID             string                  `json:"beatId"`
	Kind               string                  `json:"kind"`
	Index              uint8                   `json:"index"`
	ChainIndex         int                     `json:"chainIndex"`
	DisplayText        string                  `json:"displayText"`
	SpeechText         string                  `json:"speechText"`
	VisualState        string                  `json:"visualState"`
	SpeakerID          string                  `json:"speakerId,omitempty"`
	MIMEType           string                  `json:"mimeType,omitempty"`
	Format             string                  `json:"format,omitempty"`
	DataURL            string                  `json:"dataUrl,omitempty"`
	AudioUnavailable   bool                    `json:"audioUnavailable,omitempty"`
	AudioError         string                  `json:"audioError,omitempty"`
	Part               *session.ExpressionPart `json:"part,omitempty"`
	StickerURL         string                  `json:"stickerUrl,omitempty"`
	StickerUnavailable bool                    `json:"stickerUnavailable,omitempty"`
	StickerError       string                  `json:"stickerError,omitempty"`
}

func (s *CoreService) forwardTurnEvents(socket *coreclient.SessionSocket, conversation string, events <-chan coreclient.TurnEvent) {
	for event := range events {
		if event.ConversationID != conversation {
			continue
		}
		converted := decodeDesktopTurnEvent(event)
		if converted.Beat != nil && converted.Beat.Part != nil && converted.Beat.Part.Kind == session.ExpressionSticker {
			s.prepareDesktopSticker(socket, event, converted.Beat)
		}
		s.mu.Lock()
		current := s.socket == socket
		if current && isDesktopTurnActive(event.State) {
			s.active = true
			s.activeTurnID = event.TurnID
		}
		if current && isDesktopTurnTerminal(event.State) && s.activeTurnID == event.TurnID {
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

func (s *CoreService) prepareDesktopSticker(socket *coreclient.SessionSocket, event coreclient.TurnEvent, beat *desktopBeat) {
	fail := func(message string) {
		beat.StickerUnavailable = true
		beat.StickerError = message
		if strings.TrimSpace(beat.BeatID) == "" || strings.TrimSpace(event.TurnID) == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := socket.ReportExpressionDelivery(ctx, coreclient.ExpressionDeliveryResult{
			ConversationID: event.ConversationID,
			TurnID:         event.TurnID,
			BeatID:         beat.BeatID,
			Status:         session.ExpressionDeliveryFailed,
			ErrorMessage:   message,
		}); err != nil {
			beat.StickerError = message + "；失败状态回报 Core 失败"
		}
	}
	if beat.Part == nil || beat.Part.Sticker == nil || strings.TrimSpace(beat.Part.Sticker.ID) == "" ||
		!desktopStickerMIME(beat.Part.Sticker.MIMEType) || strings.TrimSpace(beat.BeatID) == "" {
		fail("表情包引用无效")
		return
	}
	s.mu.Lock()
	client, cache, current := s.client, s.visualCache, s.socket == socket
	s.mu.Unlock()
	if !current || client == nil || cache == nil {
		fail("Desktop 表情包读取链路不可用")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	content, err := client.ReadStickerContent(ctx, beat.Part.Sticker.ID)
	if err != nil {
		fail("无法从 Core 读取表情包")
		return
	}
	if content.MIMEType != beat.Part.Sticker.MIMEType {
		fail("Core 表情包 MIME 与回复快照不一致")
		return
	}
	route, err := cache.PutSticker(beat.BeatID, content)
	if err != nil {
		fail("无法准备 Desktop 表情包")
		return
	}
	beat.StickerURL = route
}

func (s *CoreService) ReportStickerDelivery(turnID, beatID string, succeeded bool) error {
	turnID, beatID = strings.TrimSpace(turnID), strings.TrimSpace(beatID)
	if turnID == "" || beatID == "" {
		return errors.New("sticker delivery identity is required")
	}
	s.mu.Lock()
	socket, conversation := s.socket, s.conversation
	s.mu.Unlock()
	if socket == nil || conversation == "" {
		return errors.New("Core session is not connected")
	}
	result := coreclient.ExpressionDeliveryResult{
		ConversationID: conversation,
		TurnID:         turnID,
		BeatID:         beatID,
		Status:         session.ExpressionDeliverySucceeded,
	}
	if !succeeded {
		result.Status = session.ExpressionDeliveryFailed
		result.ErrorMessage = "Desktop 表情包渲染失败"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return socket.ReportExpressionDelivery(ctx, result)
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
			validateDesktopAudio(&beat)
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

func validateDesktopAudio(beat *desktopBeat) {
	if beat == nil {
		return
	}
	hasMetadata := beat.MIMEType != "" || beat.Format != "" || beat.SpeakerID != ""
	if beat.DataURL == "" && !hasMetadata {
		return
	}
	fail := func() {
		beat.DataURL = ""
		beat.AudioUnavailable = true
		beat.AudioError = "语音数据不可用"
	}
	if beat.MIMEType != desktopAudioMIME || beat.Format != desktopAudioFormat {
		fail()
		return
	}
	prefix := "data:" + desktopAudioMIME + ";base64,"
	if !strings.HasPrefix(beat.DataURL, prefix) {
		fail()
		return
	}
	encoded := strings.TrimPrefix(beat.DataURL, prefix)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maxDesktopAudioBytes) {
		fail()
		return
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maxDesktopAudioBytes {
		fail()
	}
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

func generateInstallationKey() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "macos-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func settingsForConnection(connection desktopConnection) CoreSettings {
	return CoreSettings{
		Endpoint:    connection.Endpoint,
		EndpointKey: connection.EndpointKey,
		HasToken:    connection.Token != "",
	}
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
