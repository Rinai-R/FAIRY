package desktop

import (
	"errors"
	"sync"
)

// WindowBounds is a Wails-free geometry DTO for product windows.
type WindowBounds struct {
	X      int
	Y      int
	Width  int
	Height int
}

// CompanionWindow is implemented by the Wails window adapter in main.
// DesktopService stays free of Wails imports.
type CompanionWindow interface {
	Show()
	Hide()
	SetAlwaysOnTop(enabled bool)
	SetIgnoreMouseEvents(ignore bool)
	Bounds() WindowBounds
	SetBounds(bounds WindowBounds)
}

// ControlPanelWindow is the second product surface (settings).
type ControlPanelWindow interface {
	Show()
	Hide()
	Focus()
	SetBounds(bounds WindowBounds)
}

// SpeechBubbleWindow is the transparent, click-through window that floats the
// character's speech bubble above the companion window.
type SpeechBubbleWindow interface {
	Show()
	Hide()
	SetBounds(bounds WindowBounds)
}

const (
	// Idle hugs the full-size pet (+ chat trigger). Chat grows just enough for
	// the anchored card beside the pet — spare transparent cover stays minimal.
	idleWidth          = 220
	idleHeight         = 344
	chatWidth          = 552
	chatHeight         = 382
	controlPanelWidth  = 560
	controlPanelHeight = 620
	// The speech bubble lives in its own transparent, click-through window that
	// floats above the character. It is shown/positioned while the character is
	// talking and hidden once the bubble is gone, so the companion window itself
	// never resizes (which would flicker the character).
	speechBubbleWidth   = 250
	speechBubbleHeight  = 200
	speechBubbleOverlap = 30
	speechBubbleInsetX  = 14
)

type DesktopState struct {
	AlwaysOnTop         bool   `json:"alwaysOnTop"`
	ClickThrough        bool   `json:"clickThrough"`
	TrayReady           bool   `json:"trayReady"`
	Visible             bool   `json:"visible"`
	CompanionSurface    string `json:"companionSurface"`
	ControlPanelVisible bool   `json:"controlPanelVisible"`
	Phase               string `json:"phase"`
}

// StateEmitter broadcasts desktop-state-changed to the frontend (wired from main).
type StateEmitter func(DesktopState)

type DesktopService struct {
	mu            sync.Mutex
	state         DesktopState
	window        CompanionWindow
	controlPanel  ControlPanelWindow
	speechBubble  SpeechBubbleWindow
	emit          StateEmitter
	idleAnchor    *WindowBounds
	speechVisible bool
}

// AttachStateEmitter wires a Wails-free sink from main.
// It is intentionally not a service method so Wails does not bind it.
func AttachStateEmitter(service *DesktopService, emit StateEmitter) {
	if service == nil {
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.emit = emit
}

func NewDesktopService() *DesktopService {
	return &DesktopService{state: DesktopState{
		AlwaysOnTop:         true,
		ClickThrough:        false,
		TrayReady:           false,
		Visible:             true,
		CompanionSurface:    "idle",
		ControlPanelVisible: false,
		Phase:               "companion_idle",
	}}
}

// AttachCompanionWindow wires the native window adapter from main.
// It is intentionally not a service method so Wails does not bind it.
func AttachCompanionWindow(service *DesktopService, window CompanionWindow) {
	if service == nil {
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.window = window
}

// AttachControlPanelWindow wires the control-panel window adapter from main.
func AttachControlPanelWindow(service *DesktopService, window ControlPanelWindow) {
	if service == nil {
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.controlPanel = window
}

// AttachSpeechBubbleWindow wires the speech-bubble window adapter from main.
// It is intentionally not a service method so Wails does not bind it.
func AttachSpeechBubbleWindow(service *DesktopService, window SpeechBubbleWindow) {
	if service == nil {
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.speechBubble = window
}

// commit applies mutator under lock, then emits desktop-state-changed outside the lock
// (mirrors Tauri emit_desktop_state after each mutating command).
func (s *DesktopService) commit(mutator func()) DesktopState {
	s.mu.Lock()
	mutator()
	snapshot := s.state
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit(snapshot)
	}
	return snapshot
}

func (s *DesktopService) MarkTrayReady() (DesktopState, error) {
	return s.commit(func() {
		s.state.TrayReady = true
	}), nil
}

func (s *DesktopService) GetDesktopState() (DesktopState, error) {
	return s.snapshot(), nil
}

func (s *DesktopService) SetAlwaysOnTop(enabled bool) (DesktopState, error) {
	return s.commit(func() {
		s.state.AlwaysOnTop = enabled
		if s.window != nil {
			s.window.SetAlwaysOnTop(enabled)
		}
	}), nil
}

func (s *DesktopService) SetClickThrough(enabled bool) (DesktopState, error) {
	s.mu.Lock()
	if enabled && !s.state.TrayReady {
		s.mu.Unlock()
		return DesktopState{}, errors.New("TRAY_NOT_READY: click-through requires a tray restore path before it can be enabled")
	}
	s.state.ClickThrough = enabled
	if s.window != nil {
		s.window.SetIgnoreMouseEvents(enabled)
	}
	snapshot := s.state
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit(snapshot)
	}
	return snapshot, nil
}

func (s *DesktopService) ShowCompanion() (DesktopState, error) {
	return s.commit(func() {
		s.state.Visible = true
		s.state.ControlPanelVisible = false
		s.state.CompanionSurface = "idle"
		s.state.Phase = "companion_idle"
		if s.window != nil {
			s.window.Show()
		}
	}), nil
}

func (s *DesktopService) HideCompanion() (DesktopState, error) {
	return s.commit(func() {
		s.state.Visible = false
		s.state.CompanionSurface = "idle"
		s.state.Phase = "companion_idle"
		if s.window != nil {
			s.window.Hide()
		}
	}), nil
}

func (s *DesktopService) RestoreCompanionInteraction() (DesktopState, error) {
	return s.commit(func() {
		s.state.ClickThrough = false
		s.state.Visible = true
		if s.window != nil {
			s.window.SetIgnoreMouseEvents(false)
			s.window.Show()
		}
	}), nil
}

func (s *DesktopService) OpenCompanionChat() (DesktopState, error) {
	return s.commit(func() {
		s.state.Visible = true
		s.state.ControlPanelVisible = false
		s.state.CompanionSurface = "chat"
		s.state.Phase = "companion_chat_open"
		s.hideSpeechLocked()
		if s.window != nil {
			current := s.window.Bounds()
			anchor := current
			s.idleAnchor = &anchor
			target := chatWindowBounds(current)
			s.window.SetBounds(target)
			s.window.Show()
		}
	}), nil
}

func (s *DesktopService) CloseCompanionChat() (DesktopState, error) {
	return s.commit(func() {
		s.state.CompanionSurface = "idle"
		s.state.Phase = "companion_idle"
		if s.window != nil {
			if s.idleAnchor != nil {
				s.window.SetBounds(*s.idleAnchor)
				s.idleAnchor = nil
			} else {
				current := s.window.Bounds()
				s.window.SetBounds(WindowBounds{
					X:      current.X + current.Width - idleWidth,
					Y:      current.Y + current.Height - idleHeight,
					Width:  idleWidth,
					Height: idleHeight,
				})
			}
		}
	}), nil
}

// speechBubbleBounds places the transparent bubble window above the character,
// anchored so its bottom edge dips slightly into the top of the companion window
// (over the hair, above the face). The bubble content is bottom-anchored inside
// this window and grows upward.
func speechBubbleBounds(companion WindowBounds) WindowBounds {
	return WindowBounds{
		X:      companion.X - speechBubbleInsetX,
		Y:      companion.Y + speechBubbleOverlap - speechBubbleHeight,
		Width:  speechBubbleWidth,
		Height: speechBubbleHeight,
	}
}

// ExpandCompanionForSpeech shows and positions the standalone speech-bubble
// window above the character. It is a no-op unless the companion is idle so the
// bubble never appears over the chat or control panel.
func (s *DesktopService) ExpandCompanionForSpeech() (DesktopState, error) {
	return s.commit(func() {
		if s.window == nil || s.speechBubble == nil {
			return
		}
		if s.state.CompanionSurface != "idle" || s.state.ControlPanelVisible {
			return
		}
		s.speechVisible = true
		s.speechBubble.SetBounds(speechBubbleBounds(s.window.Bounds()))
		s.speechBubble.Show()
	}), nil
}

// RestoreCompanionAfterSpeech hides the standalone speech-bubble window. It is a
// no-op when the bubble is not currently shown.
func (s *DesktopService) RestoreCompanionAfterSpeech() (DesktopState, error) {
	return s.commit(func() {
		s.hideSpeechLocked()
	}), nil
}

// RepositionSpeechBubble keeps the bubble window glued to the character while it
// is dragged. It is invoked from the companion window's move hook and is a no-op
// unless the bubble is currently shown.
func (s *DesktopService) RepositionSpeechBubble() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.window == nil || s.speechBubble == nil || !s.speechVisible {
		return
	}
	s.speechBubble.SetBounds(speechBubbleBounds(s.window.Bounds()))
}

// hideSpeechLocked hides the bubble window; caller holds s.mu.
func (s *DesktopService) hideSpeechLocked() {
	if s.speechBubble == nil || !s.speechVisible {
		return
	}
	s.speechVisible = false
	s.speechBubble.Hide()
}

func (s *DesktopService) ShowControlPanel() (DesktopState, error) {
	return s.commit(func() {
		s.hideSpeechLocked()
		s.state.Visible = false
		s.state.ControlPanelVisible = true
		s.state.CompanionSurface = "idle"
		s.state.Phase = "control_panel_visible"
		if s.window != nil {
			if s.controlPanel != nil {
				companion := s.window.Bounds()
				s.controlPanel.SetBounds(replacementControlPanelBounds(companion))
			}
			s.window.Hide()
		}
		if s.controlPanel != nil {
			s.controlPanel.Show()
			s.controlPanel.Focus()
		}
	}), nil
}

// replacementControlPanelBounds mirrors the Tauri replacement placement:
// align the panel's bottom-right with the companion's bottom-right.
func replacementControlPanelBounds(companion WindowBounds) WindowBounds {
	return WindowBounds{
		X:      companion.X + companion.Width - controlPanelWidth,
		Y:      companion.Y + companion.Height - controlPanelHeight,
		Width:  controlPanelWidth,
		Height: controlPanelHeight,
	}
}

// chatWindowBounds grows toward the top-left (Tauri chat_window_position preference)
// when expanding from idle to chat size.
func chatWindowBounds(current WindowBounds) WindowBounds {
	widthGrowth := chatWidth - current.Width
	heightGrowth := chatHeight - current.Height
	if widthGrowth < 0 {
		widthGrowth = 0
	}
	if heightGrowth < 0 {
		heightGrowth = 0
	}
	return WindowBounds{
		X:      current.X - widthGrowth,
		Y:      current.Y - heightGrowth,
		Width:  chatWidth,
		Height: chatHeight,
	}
}

func (s *DesktopService) RestoreCompanionAfterControlPanel() (DesktopState, error) {
	s.mu.Lock()
	if s.state.Phase == "companion_idle" && !s.state.ControlPanelVisible {
		snap := s.state
		s.mu.Unlock()
		return snap, nil
	}
	// Emit transitioning_to_companion first so the companion frontend can latch
	// pet reveal via trackControlPanelReturn (Rust/Tauri parity). Jumping
	// straight to companion_idle leaves petVisualOpen=false forever.
	s.state.Phase = "transitioning_to_companion"
	transitioning := s.state
	emit := s.emit
	controlPanel := s.controlPanel
	window := s.window
	s.mu.Unlock()
	if emit != nil {
		emit(transitioning)
	}
	if controlPanel != nil {
		controlPanel.Hide()
	}
	if window != nil {
		window.Show()
	}
	return s.commit(func() {
		s.state.Visible = true
		s.state.ControlPanelVisible = false
		s.state.CompanionSurface = "idle"
		s.state.Phase = "companion_idle"
	}), nil
}

func (s *DesktopService) snapshot() DesktopState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}
