package coreclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fairy/session"

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
		if err := conn.WriteJSON(sessionServerFrame{Type: "session.opened", RequestID: open.RequestID, ConversationID: "conversation-1", CharacterID: "character-1", Endpoint: session.EndpointDesktop}); err != nil {
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
	called := make(chan DesktopCaptureRequest, 1)
	if err := socket.SetDesktopCaptureHandler(func(_ context.Context, request DesktopCaptureRequest) DesktopCaptureResult {
		called <- request
		return DesktopCaptureResult{Status: "failed", ErrorCode: "permission_denied", ErrorMessage: "not persisted by Core"}
	}); err != nil {
		t.Fatal(err)
	}
	close(start)
	if _, err := socket.OpenSession(t.Context(), OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desktop-1",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied},
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
