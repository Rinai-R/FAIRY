package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSessionSocketDispatchesCaptureCallbackAndReturnsCorrelatedResult(t *testing.T) {
	upgrader := websocket.Upgrader{}
	start := make(chan struct{})
	received := make(chan sessionClientFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(sessionServerFrame{Type: "ready"}); err != nil {
			t.Error(err)
			return
		}
		<-start
		var open sessionClientFrame
		if err := conn.ReadJSON(&open); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteJSON(sessionServerFrame{Type: "session.opened", RequestID: open.RequestID, ConversationID: "conversation-1", CharacterID: "character-1", Endpoint: EndpointDesktop}); err != nil {
			t.Error(err)
			return
		}
		request := DesktopCaptureRequest{
			ExecutionID: "execution-1", ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
			DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), MaxDecodedBytes: 1024,
			MaxDimension: 100, AllowedMIMETypes: []string{"image/png"},
		}
		if err := conn.WriteJSON(sessionServerFrame{Type: "desktop.capture.request", ConversationID: request.ConversationID, CaptureRequest: &request}); err != nil {
			t.Error(err)
			return
		}
		var result sessionClientFrame
		if err := conn.ReadJSON(&result); err != nil {
			t.Error(err)
			return
		}
		received <- result
		_ = conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: result.RequestID, ConversationID: request.ConversationID})
	}))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	// The test server accepts the session path regardless of URL path.
	client.baseURL.Path = strings.TrimSuffix(client.baseURL.Path, "/")
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if got := cap(socket.captureDispatchSlots); got != sessionSocketCaptureCapacity {
		t.Fatalf("default capture dispatch capacity = %d, want %d", got, sessionSocketCaptureCapacity)
	}
	called := make(chan DesktopCaptureRequest, 1)
	if err := socket.SetDesktopCaptureHandler(func(_ context.Context, request DesktopCaptureRequest) DesktopCaptureResult {
		called <- request
		return DesktopCaptureResult{Status: "failed", ErrorCode: "permission_denied", ErrorMessage: "not persisted by Core"}
	}); err != nil {
		t.Fatal(err)
	}
	close(start)
	if _, err := socket.OpenSession(t.Context(), OpenSessionRequest{
		Endpoint: EndpointDesktop, EndpointKey: "desktop-1",
		Interaction: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationEmbodied},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-called:
		if request.ExecutionID != "execution-1" {
			t.Fatalf("callback request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("capture callback was not invoked")
	}
	select {
	case frame := <-received:
		if frame.Type != "desktop.capture.result" || frame.CaptureResult == nil || frame.CaptureResult.ExecutionID != "execution-1" || frame.CaptureResult.ConversationID != "conversation-1" {
			t.Fatalf("capture result frame = %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("capture result was not returned")
	}
}

func TestSessionSocketBoundsCaptureDispatchWorkersAndRecovers(t *testing.T) {
	socket := &SessionSocket{captureDispatchSlots: make(chan struct{}, 2)}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	finished := make(chan struct{}, 2)
	work := func() {
		started <- struct{}{}
		<-release
		finished <- struct{}{}
	}
	if err := socket.dispatchCapture(work); err != nil {
		t.Fatal(err)
	}
	if err := socket.dispatchCapture(work); err != nil {
		t.Fatal(err)
	}
	<-started
	<-started
	if err := socket.dispatchCapture(func() { t.Error("over-capacity capture dispatch ran") }); !errors.Is(err, ErrSessionCaptureDispatchCapacity) {
		t.Fatalf("over-capacity error = %v, want ErrSessionCaptureDispatchCapacity", err)
	}
	if got := len(socket.captureDispatchSlots); got != 2 {
		t.Fatalf("active capture dispatch workers = %d, want 2", got)
	}
	close(release)
	<-finished
	<-finished

	recovered := make(chan struct{})
	if err := socket.dispatchCapture(func() { close(recovered) }); err != nil {
		t.Fatalf("dispatch after release error = %v", err)
	}
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("capture dispatch did not recover after release")
	}
}

func TestSessionSocketRollsBackCaptureSlotWhenDispatchCapacityIsFull(t *testing.T) {
	socket := &SessionSocket{
		captureSlot:          make(chan struct{}, 1),
		captureDispatchSlots: make(chan struct{}, 1),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := socket.dispatchCapture(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	var handlerCalls atomic.Int64
	request := DesktopCaptureRequest{
		ExecutionID: "execution-1", ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
		DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), MaxDecodedBytes: 1024,
		MaxDimension: 100, AllowedMIMETypes: []string{"image/png"},
	}
	err := socket.dispatchOpenedCapture(request, func(context.Context, DesktopCaptureRequest) DesktopCaptureResult {
		handlerCalls.Add(1)
		return DesktopCaptureResult{}
	})
	if !errors.Is(err, ErrSessionCaptureDispatchCapacity) {
		t.Fatalf("capture dispatch error = %v, want ErrSessionCaptureDispatchCapacity", err)
	}
	if got := len(socket.captureSlot); got != 0 {
		t.Fatalf("failed dispatch retained capture slot count %d", got)
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("failed dispatch called capture handler %d times", got)
	}
	close(release)
}

func TestSessionSocketCaptureDispatchCapacityTerminatesBlockedWire(t *testing.T) {
	upgrader := websocket.Upgrader{}
	push := make(chan struct{})
	firstResult := make(chan sessionClientFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(sessionServerFrame{Type: "ready"}); err != nil {
			t.Error(err)
			return
		}
		var open sessionClientFrame
		if err := conn.ReadJSON(&open); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteJSON(sessionServerFrame{
			Type: "session.opened", RequestID: open.RequestID,
			ConversationID: "conversation-1", CharacterID: "character-1", Endpoint: EndpointDesktop,
		}); err != nil {
			t.Error(err)
			return
		}
		<-push
		request := DesktopCaptureRequest{
			ExecutionID: "execution-1", ConversationID: "not-opened", TurnID: "turn-1", CallID: "call-1",
			DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), MaxDecodedBytes: 1024,
			MaxDimension: 100, AllowedMIMETypes: []string{"image/png"},
		}
		if err := conn.WriteJSON(sessionServerFrame{Type: "desktop.capture.request", CaptureRequest: &request}); err != nil {
			t.Error(err)
			return
		}
		var result sessionClientFrame
		if err := conn.ReadJSON(&result); err != nil {
			t.Error(err)
			return
		}
		firstResult <- result
		request.ExecutionID = "execution-2"
		request.TurnID = "turn-2"
		request.CallID = "call-2"
		_ = conn.WriteJSON(sessionServerFrame{Type: "desktop.capture.request", CaptureRequest: &request})
	}))
	defer server.Close()

	client, err := New(Options{Endpoint: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	socket.mu.Lock()
	socket.captureDispatchSlots = make(chan struct{}, 1)
	socket.mu.Unlock()
	var handlerCalls atomic.Int64
	if err := socket.SetDesktopCaptureHandler(func(context.Context, DesktopCaptureRequest) DesktopCaptureResult {
		handlerCalls.Add(1)
		return DesktopCaptureResult{}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.OpenSession(t.Context(), OpenSessionRequest{
		Endpoint: EndpointDesktop, EndpointKey: "desktop-1",
		Interaction: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationEmbodied},
	}); err != nil {
		t.Fatal(err)
	}
	close(push)
	select {
	case result := <-firstResult:
		if result.Type != "desktop.capture.result" || result.CaptureResult == nil || result.CaptureResult.ErrorCode != "session_mismatch" {
			t.Fatalf("first capture failure frame = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("first automatic capture failure was not sent")
	}
	select {
	case <-socket.done:
	case <-time.After(time.Second):
		t.Fatal("socket did not terminate after capture dispatch overflow")
	}
	if err := socket.ObserveAmbient(t.Context(), "conversation-1", AmbientObservation{MessageID: "after-close"}); !errors.Is(err, ErrSessionCaptureDispatchCapacity) {
		t.Fatalf("request after socket termination error = %v, want ErrSessionCaptureDispatchCapacity", err)
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("mismatched capture requests called handler %d times", got)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for len(socket.captureDispatchSlots) != 0 {
		select {
		case <-deadline.C:
			t.Fatalf("closed socket retained %d capture dispatch workers", len(socket.captureDispatchSlots))
		case <-ticker.C:
		}
	}
}
