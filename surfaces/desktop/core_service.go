package main

import (
	"context"
	"encoding/json"
	"errors"
	"fairy/transport/session"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type CoreSettings struct {
	ProfileDir    string `json:"profileDir"`
	Ready         bool   `json:"ready"`
	CharacterName string `json:"characterName"`
}

type CoreSession struct {
	Settings       CoreSettings            `json:"settings"`
	ConversationID string                  `json:"conversationId"`
	Character      session.CharacterRecord `json:"character"`
	Messages       []session.MessageRecord `json:"messages"`
}

type CoreService struct {
	mu                   sync.Mutex
	app                  *application.App
	companion            application.Window
	controlPanel         application.Window
	history              application.Window
	speechBubble         application.Window
	management           application.Window
	windowLink           windowRelation
	controlOpen          bool
	historyOpen          bool
	managementOpen       bool
	controlWidth         int
	assets               sessionAssets
	socket               sessionPlane
	visualCache          *visualCache
	newCache             func() (*visualCache, error)
	conversation         string
	active               bool
	activeTurnID         string
	emit                 func(string, any)
	observation          *desktopObservationRuntime
	capture              *desktopCaptureRuntime
	privacy              session.DesktopPrivacyState
	edge                 ownedRuntime
	openEdge             func(context.Context) (ownedRuntime, error)
	openTransport        func(context.Context) (sessionPlane, sessionAssets, CoreSettings, error)
	localEndpointKey     string
	legacyConnectionFile func() (string, error)
	instance             instanceGuard
	acquireLock          func(string, func()) (instanceGuard, error)
	profileDir           func() (string, error)
	requestFocus         func(string) error
	shutdownBudget       shutdownBudget
	characterName        string
}

func NewCoreService() *CoreService {
	service := &CoreService{
		newCache:     newVisualCache,
		privacy:      session.DesktopPrivacyProtected,
		controlWidth: controlPanelWidth,
		windowLink:   newPlatformWindowRelation(),
	}
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
	s.controlWidth = controlPanelWidth
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
	companion, panel, history, windowLink := s.companion, s.controlPanel, s.history, s.windowLink
	if companion == nil || panel == nil {
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
		if err := windowLink.Detach(companion, panel); err != nil {
			return fmt.Errorf("detach Core settings window: %w", err)
		}
		s.emitControlPanelState(false)
		return nil
	}
	if history != nil {
		history.Hide()
		s.emitHistoryState(false)
	}
	s.refreshControlPanelWidth()
	s.repositionControlPanel()
	if err := windowLink.Attach(companion, panel); err != nil {
		s.mu.Lock()
		if panel == s.controlPanel {
			s.controlOpen = false
		}
		s.mu.Unlock()
		return fmt.Errorf("attach Core settings window: %w", err)
	}
	panel.Show()
	companion.Focus()
	s.emitControlPanelState(true)
	return nil
}

// refreshControlPanelWidth keeps the resizable settings window out of the
// companion move hot path. It is called only when the panel opens or resizes.
func (s *CoreService) refreshControlPanelWidth() {
	s.mu.Lock()
	panel := s.controlPanel
	s.mu.Unlock()
	if panel == nil {
		return
	}
	width, _ := panel.Size()
	if width <= 0 {
		return
	}
	s.mu.Lock()
	if panel == s.controlPanel {
		s.controlWidth = width
	}
	s.mu.Unlock()
}

// repositionControlPanel places the settings window beside the companion at
// settings lifecycle boundaries such as open and resize, not during pet drag.
func (s *CoreService) repositionControlPanel() {
	s.mu.Lock()
	companion, panel, open, width := s.companion, s.controlPanel, s.controlOpen, s.controlWidth
	s.mu.Unlock()
	if !open || companion == nil || panel == nil {
		return
	}
	x, y := companion.Position()
	panel.SetPosition(x-width-8, y+47)
}

func (s *CoreService) CloseControlPanel() error {
	s.mu.Lock()
	companion, panel, windowLink := s.companion, s.controlPanel, s.windowLink
	s.controlOpen = false
	s.mu.Unlock()
	if panel != nil {
		panel.Hide()
	}
	s.emitControlPanelState(false)
	if err := windowLink.Detach(companion, panel); err != nil {
		return fmt.Errorf("detach Core settings window: %w", err)
	}
	return nil
}

// OpenHistory keeps recent messages beside the pet rather than expanding or
// covering the companion window with the retired chat surface.
func (s *CoreService) OpenHistory() error {
	s.mu.Lock()
	companion, panel, history, windowLink := s.companion, s.controlPanel, s.history, s.windowLink
	if companion == nil || history == nil {
		s.mu.Unlock()
		return errors.New("history window is unavailable")
	}
	historyOpen := s.historyOpen
	controlOpen := s.controlOpen
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
		if controlOpen {
			if err := windowLink.Detach(companion, panel); err != nil {
				return fmt.Errorf("detach Core settings window: %w", err)
			}
		}
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

func (s *CoreService) RecentMessages() ([]session.MessageRecord, error) {
	s.mu.Lock()
	assets, conversation := s.assets, s.conversation
	s.mu.Unlock()
	if assets == nil || conversation == "" {
		return nil, errors.New("Core session is not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	page, err := assets.ListMessages(ctx, conversation, 0, 20)
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

func (s *CoreService) showSpeechBubble() {
	s.RepositionSpeechBubble()
	s.mu.Lock()
	bubble := s.speechBubble
	s.mu.Unlock()
	if bubble == nil {
		return
	}
	bubble.Show()
}

func (s *CoreService) RuntimeInfo() (CoreSettings, error) {
	if s == nil {
		return CoreSettings{}, errors.New("desktop core service is unavailable")
	}
	dir, err := s.resolveProfileDir()
	if err != nil {
		return CoreSettings{}, err
	}
	s.mu.Lock()
	ready := s.edge != nil
	name := s.characterName
	s.mu.Unlock()
	return CoreSettings{ProfileDir: dir, Ready: ready, CharacterName: name}, nil
}

func (s *CoreService) resolveProfileDir() (string, error) {
	resolve := s.profileDir
	if resolve == nil {
		resolve = desktopProfileDir
	}
	return resolve()
}

func (s *CoreService) Connect() (CoreSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	socket, assets, _, err := s.openSessionTransport(ctx)
	if err != nil {
		return CoreSession{}, err
	}
	if socket == nil || assets == nil {
		if socket != nil {
			_ = socket.Close()
		}
		return CoreSession{}, errors.New("session transport is unavailable")
	}
	endpointKey, err := s.desktopEndpointKey()
	if err != nil {
		_ = socket.Close()
		return CoreSession{}, err
	}
	info, err := s.RuntimeInfo()
	if err != nil {
		_ = socket.Close()
		return CoreSession{}, err
	}
	if err := socket.SetDesktopCaptureHandler(func(ctx context.Context, request session.DesktopCaptureRequest) session.DesktopCaptureResult {
		s.mu.Lock()
		capture := s.capture
		s.mu.Unlock()
		if capture == nil {
			return failedCaptureResult("capture_unavailable")
		}
		return capture.Handle(ctx, request)
	}); err != nil {
		_ = socket.Close()
		return CoreSession{}, err
	}
	closeSocket := true
	defer func() {
		if closeSocket {
			_ = socket.Close()
		}
	}()
	opened, err := socket.OpenSession(ctx, session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: endpointKey,
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
	catalog, err := assets.ListCharacters(ctx)
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
	localVisual, err := cache.Sync(ctx, assets, *catalog.Active.Appearance.Visual)
	if err != nil {
		_ = cache.Close()
		return CoreSession{}, err
	}
	messages, err := assets.ListMessages(ctx, opened.ConversationID, 0, 20)
	if err != nil {
		_ = cache.Close()
		return CoreSession{}, err
	}
	s.mu.Lock()
	previous, previousCache := s.socket, s.visualCache
	s.assets, s.socket, s.conversation = assets, socket, opened.ConversationID
	s.visualCache = cache
	s.active, s.activeTurnID = false, ""
	s.characterName = catalog.Active.Name
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
	info.CharacterName = character.Name
	return CoreSession{Settings: info, ConversationID: opened.ConversationID, Character: character, Messages: messages.Messages}, nil
}

func (s *CoreService) openSessionTransport(ctx context.Context) (sessionPlane, sessionAssets, CoreSettings, error) {
	s.mu.Lock()
	open := s.openTransport
	runtime := s.edge
	s.mu.Unlock()
	if open != nil {
		return open(ctx)
	}
	if runtime == nil {
		return nil, nil, CoreSettings{}, errors.New("edge runtime is not started")
	}
	plane, assets, err := runtime.OpenSessionTransport()
	if err != nil {
		return nil, nil, CoreSettings{}, err
	}
	if plane == nil || assets == nil {
		if plane != nil {
			_ = plane.Close()
		}
		return nil, nil, CoreSettings{}, errors.New("edge session transport is unavailable")
	}
	info, err := s.RuntimeInfo()
	if err != nil {
		_ = plane.Close()
		return nil, nil, CoreSettings{}, err
	}
	return plane, assets, info, nil
}

func (s *CoreService) desktopEndpointKey() (string, error) {
	s.mu.Lock()
	if s.localEndpointKey != "" {
		key := s.localEndpointKey
		s.mu.Unlock()
		return key, nil
	}
	s.mu.Unlock()
	dir, err := s.resolveProfileDir()
	if err != nil {
		return "", err
	}
	legacyPath, err := s.resolveLegacyConnectionFile()
	if err != nil {
		return "", err
	}
	key, err := loadOrCreateEndpointKey(dir, legacyPath)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.localEndpointKey == "" {
		s.localEndpointKey = key
	} else {
		key = s.localEndpointKey
	}
	s.mu.Unlock()
	return key, nil
}

func (s *CoreService) resolveLegacyConnectionFile() (string, error) {
	if s != nil && s.legacyConnectionFile != nil {
		return s.legacyConnectionFile()
	}
	return defaultLegacyConnectionFile()
}

func (s *CoreService) Send(input string) error {
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
	s.showSpeechBubble()
	if _, err := socket.SubmitTurn(context.Background(), conversation, session.SubmitTurnRequest{Input: input}); err != nil {
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
	VisualState        string                  `json:"visualState"`
	Part               *session.ExpressionPart `json:"part,omitempty"`
	StickerURL         string                  `json:"stickerUrl,omitempty"`
	StickerUnavailable bool                    `json:"stickerUnavailable,omitempty"`
	StickerError       string                  `json:"stickerError,omitempty"`
}

func (s *CoreService) forwardTurnEvents(socket sessionPlane, conversation string, events <-chan session.TurnEvent) {
	for event := range events {
		if event.ConversationID != conversation {
			continue
		}
		s.mu.Lock()
		current := s.socket == socket
		s.mu.Unlock()
		if !current {
			continue
		}
		converted := decodeDesktopTurnEvent(event)
		if converted.Beat != nil && converted.Beat.Part != nil && converted.Beat.Part.Kind == session.ExpressionSticker {
			s.prepareDesktopSticker(socket, event, converted.Beat)
		}
		s.mu.Lock()
		current = s.socket == socket
		showBubble := false
		if current && isDesktopTurnActive(event.State) {
			showBubble = s.activeTurnID != event.TurnID
			s.active = true
			s.activeTurnID = event.TurnID
		}
		if current && isDesktopTurnTerminal(event.State) && s.activeTurnID == event.TurnID {
			s.active, s.activeTurnID = false, ""
		}
		s.mu.Unlock()
		if current {
			if showBubble {
				s.showSpeechBubble()
			}
			accepted := s.emitTurnEvent(converted)
			if accepted && desktopFinalUtterance(converted) {
				s.reportDesktopUtterance(socket, event, converted.Beat)
			}
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

func desktopFinalUtterance(event desktopTurnEvent) bool {
	if event.Type != "beat.ready" || event.Beat == nil || event.Beat.Kind != "final" || strings.TrimSpace(event.Beat.BeatID) == "" {
		return false
	}
	if event.Beat.Part == nil {
		return strings.TrimSpace(event.Beat.DisplayText) != ""
	}
	return event.Beat.Part.Kind == session.ExpressionUtterance && strings.TrimSpace(event.Beat.Part.Text) != ""
}

func (s *CoreService) reportDesktopUtterance(socket sessionPlane, event session.TurnEvent, beat *desktopBeat) {
	if socket == nil || beat == nil || strings.TrimSpace(event.ConversationID) == "" || strings.TrimSpace(event.TurnID) == "" {
		return
	}
	s.mu.Lock()
	current := s.socket == socket
	s.mu.Unlock()
	if !current {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = socket.ReportExpressionDelivery(ctx, session.ExpressionDeliveryResult{
		ConversationID: event.ConversationID,
		TurnID:         event.TurnID,
		BeatID:         beat.BeatID,
		Status:         session.ExpressionDeliverySucceeded,
	})
}

func (s *CoreService) prepareDesktopSticker(socket sessionPlane, event session.TurnEvent, beat *desktopBeat) {
	fail := func(message string) {
		beat.StickerUnavailable = true
		beat.StickerError = message
		if strings.TrimSpace(beat.BeatID) == "" || strings.TrimSpace(event.TurnID) == "" {
			return
		}
		s.mu.Lock()
		current := s.socket == socket
		s.mu.Unlock()
		if !current {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := socket.ReportExpressionDelivery(ctx, session.ExpressionDeliveryResult{
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
	assets, cache, current := s.assets, s.visualCache, s.socket == socket
	s.mu.Unlock()
	if !current {
		return
	}
	if assets == nil || cache == nil {
		fail("Desktop 表情包读取链路不可用")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	content, err := assets.ReadStickerContent(ctx, beat.Part.Sticker.ID)
	if err != nil {
		fail("无法从 Core 读取表情包")
		return
	}
	if content.MIMEType != beat.Part.Sticker.MIMEType {
		fail("Core 表情包 MIME 与回复快照不一致")
		return
	}
	s.mu.Lock()
	if s.socket != socket || s.visualCache != cache {
		s.mu.Unlock()
		return
	}
	route, err := cache.PutSticker(beat.BeatID, content)
	s.mu.Unlock()
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
	result := session.ExpressionDeliveryResult{
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

func decodeDesktopTurnEvent(event session.TurnEvent) desktopTurnEvent {
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

func (s *CoreService) emitTurnEvent(event desktopTurnEvent) bool {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit == nil {
		return false
	}
	emit("desktop:turn", event)
	return true
}

func (s *CoreService) emitDesktopSession(messages []session.MessageRecord) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit("desktop:session", map[string]any{"messages": messages})
	}
}
