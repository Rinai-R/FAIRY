package desktop

import "testing"

func TestDesktopServiceInitialState(t *testing.T) {
	state, err := NewDesktopService().GetDesktopState()
	if err != nil {
		t.Fatalf("GetDesktopState() error = %v", err)
	}
	if !state.AlwaysOnTop || !state.Visible || state.CompanionSurface != "idle" || state.Phase != "companion_idle" {
		t.Fatalf("state = %#v", state)
	}
	if state.TrayReady || state.ClickThrough || state.ControlPanelVisible {
		t.Fatalf("unexpected enabled flags: %#v", state)
	}
}

func TestDesktopServiceChatLifecycle(t *testing.T) {
	service := NewDesktopService()
	companion := &stubCompanionWindow{bounds: WindowBounds{X: 400, Y: 300, Width: idleWidth, Height: idleHeight}}
	AttachCompanionWindow(service, companion)
	state, err := service.OpenCompanionChat()
	if err != nil {
		t.Fatalf("OpenCompanionChat() error = %v", err)
	}
	if state.CompanionSurface != "chat" || state.Phase != "companion_chat_open" {
		t.Fatalf("state = %#v", state)
	}
	if companion.bounds.Width != chatWidth || companion.bounds.Height != chatHeight {
		t.Fatalf("chat bounds = %#v", companion.bounds)
	}
	state, err = service.CloseCompanionChat()
	if err != nil {
		t.Fatalf("CloseCompanionChat() error = %v", err)
	}
	if state.CompanionSurface != "idle" || state.Phase != "companion_idle" {
		t.Fatalf("state = %#v", state)
	}
	if companion.bounds.Width != idleWidth || companion.bounds.Height != idleHeight {
		t.Fatalf("idle bounds = %#v", companion.bounds)
	}
}

func TestCompanionWindowSizesStayMinimalPerSurface(t *testing.T) {
	if idleWidth >= chatWidth || idleHeight >= chatHeight {
		t.Fatalf("idle (%dx%d) must be smaller than chat (%dx%d)", idleWidth, idleHeight, chatWidth, chatHeight)
	}
	// Idle hugs the full-size pet; chat fits 326px card + pet without spare desktop cover.
	if idleWidth != 220 || idleHeight != 344 {
		t.Fatalf("idle window = %dx%d, want 220x344", idleWidth, idleHeight)
	}
	if chatWidth != 552 || chatHeight != 382 {
		t.Fatalf("chat window = %dx%d, want 552x382", chatWidth, chatHeight)
	}
}

func TestDesktopServiceEmitsStateChanges(t *testing.T) {
	service := NewDesktopService()
	var emitted []DesktopState
	AttachStateEmitter(service, func(state DesktopState) {
		emitted = append(emitted, state)
	})
	if _, err := service.OpenCompanionChat(); err != nil {
		t.Fatalf("OpenCompanionChat() error = %v", err)
	}
	if _, err := service.CloseCompanionChat(); err != nil {
		t.Fatalf("CloseCompanionChat() error = %v", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("emitted count = %d, want 2", len(emitted))
	}
	if emitted[0].CompanionSurface != "chat" || emitted[1].CompanionSurface != "idle" {
		t.Fatalf("emitted = %#v", emitted)
	}
}

type stubCompanionWindow struct {
	hidden int
	shown  int
	bounds WindowBounds
}

func (s *stubCompanionWindow) Show()                     { s.shown++ }
func (s *stubCompanionWindow) Hide()                     { s.hidden++ }
func (s *stubCompanionWindow) SetAlwaysOnTop(bool)       {}
func (s *stubCompanionWindow) SetIgnoreMouseEvents(bool) {}
func (s *stubCompanionWindow) Bounds() WindowBounds      { return s.bounds }
func (s *stubCompanionWindow) SetBounds(b WindowBounds)  { s.bounds = b }

type stubControlPanelWindow struct {
	shown  int
	hidden int
	focus  int
	bounds WindowBounds
}

func (s *stubControlPanelWindow) Show()                    { s.shown++ }
func (s *stubControlPanelWindow) Hide()                    { s.hidden++ }
func (s *stubControlPanelWindow) Focus()                   { s.focus++ }
func (s *stubControlPanelWindow) SetBounds(b WindowBounds) { s.bounds = b }

func TestDesktopServiceControlPanelLifecycle(t *testing.T) {
	service := NewDesktopService()
	companion := &stubCompanionWindow{bounds: WindowBounds{X: 100, Y: 200, Width: 520, Height: 382}}
	panel := &stubControlPanelWindow{}
	AttachCompanionWindow(service, companion)
	AttachControlPanelWindow(service, panel)
	var emitted []DesktopState
	AttachStateEmitter(service, func(state DesktopState) {
		emitted = append(emitted, state)
	})
	state, err := service.ShowControlPanel()
	if err != nil {
		t.Fatalf("ShowControlPanel() error = %v", err)
	}
	if state.Visible || !state.ControlPanelVisible || state.Phase != "control_panel_visible" {
		t.Fatalf("state = %#v", state)
	}
	if panel.shown != 1 || panel.focus != 1 || companion.hidden != 1 {
		t.Fatalf("window show/hide/focus counts companion=%#v panel=%#v", companion, panel)
	}
	wantBounds := WindowBounds{X: 100 + 520 - 560, Y: 200 + 382 - 620, Width: 560, Height: 620}
	if panel.bounds != wantBounds {
		t.Fatalf("panel bounds = %#v, want %#v", panel.bounds, wantBounds)
	}
	emitted = emitted[:0]
	state, err = service.RestoreCompanionAfterControlPanel()
	if err != nil {
		t.Fatalf("RestoreCompanionAfterControlPanel() error = %v", err)
	}
	if !state.Visible || state.ControlPanelVisible || state.Phase != "companion_idle" {
		t.Fatalf("state = %#v", state)
	}
	if panel.hidden != 1 || companion.shown != 1 {
		t.Fatalf("restore counts companion=%#v panel=%#v", companion, panel)
	}
	if len(emitted) != 2 {
		t.Fatalf("restore emits = %#v, want transitioning then companion_idle", emitted)
	}
	if emitted[0].Phase != "transitioning_to_companion" {
		t.Fatalf("first restore emit phase = %q, want transitioning_to_companion", emitted[0].Phase)
	}
	if emitted[1].Phase != "companion_idle" || !emitted[1].Visible {
		t.Fatalf("second restore emit = %#v", emitted[1])
	}
	// Idempotent when already restored.
	emitted = emitted[:0]
	state, err = service.RestoreCompanionAfterControlPanel()
	if err != nil {
		t.Fatalf("second RestoreCompanionAfterControlPanel() error = %v", err)
	}
	if state.Phase != "companion_idle" || len(emitted) != 0 {
		t.Fatalf("idempotent restore state=%#v emitted=%#v", state, emitted)
	}
}

func TestReplacementControlPanelBoundsAlignsBottomRight(t *testing.T) {
	got := replacementControlPanelBounds(WindowBounds{X: 40, Y: 80, Width: 520, Height: 382})
	want := WindowBounds{X: 0, Y: -158, Width: 560, Height: 620}
	if got != want {
		t.Fatalf("replacementControlPanelBounds() = %#v, want %#v", got, want)
	}
}

func TestDesktopServiceRejectsClickThroughWithoutTray(t *testing.T) {
	_, err := NewDesktopService().SetClickThrough(true)
	if err == nil {
		t.Fatal("SetClickThrough(true) error = nil, want explicit tray restore error")
	}
}

func TestDesktopServiceAllowsClickThroughAfterTrayReady(t *testing.T) {
	service := NewDesktopService()
	if _, err := service.MarkTrayReady(); err != nil {
		t.Fatalf("MarkTrayReady() error = %v", err)
	}
	state, err := service.SetClickThrough(true)
	if err != nil {
		t.Fatalf("SetClickThrough(true) error = %v", err)
	}
	if !state.TrayReady || !state.ClickThrough {
		t.Fatalf("state = %#v", state)
	}
	state, err = service.RestoreCompanionInteraction()
	if err != nil {
		t.Fatalf("RestoreCompanionInteraction() error = %v", err)
	}
	if state.ClickThrough || !state.Visible {
		t.Fatalf("restored state = %#v", state)
	}
}
