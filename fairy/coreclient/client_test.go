package coreclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/interaction"
	"github.com/gorilla/websocket"
)

func TestOpenSessionSendsEndpointFacts(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "session.open" || frame.Endpoint != interaction.EndpointIM || frame.EndpointKey != "onebot-group:123" || frame.Interaction.Audience != interaction.AudienceMulti {
			t.Fatalf("frame = %#v", frame)
		}
		_ = conn.WriteJSON(sessionServerFrame{
			Type: "session.opened", RequestID: frame.RequestID,
			ConversationID: "c1", CharacterID: "ch1", MessageCount: 0, Endpoint: interaction.EndpointIM,
		})
	})
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.OpenSession(context.Background(), OpenSessionRequest{Endpoint: interaction.EndpointIM, EndpointKey: "onebot-group:123", Interaction: interaction.Context{Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient, Presentation: interaction.PresentationChat}})
	if err != nil || response.ConversationID != "c1" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestOpenSessionRequestExposesOnlyInteractionFacts(t *testing.T) {
	raw, err := json.Marshal(OpenSessionRequest{
		Endpoint:    interaction.EndpointIM,
		EndpointKey: "onebot-group:123",
		Interaction: interaction.Context{Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient, Presentation: interaction.PresentationChat},
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"memoryPolicy", "presenceProjection", "presenceGuidance", "prompt", "trust", "replyFrequency", "participationScore"} {
		if _, ok := envelope[forbidden]; ok {
			t.Fatalf("session.open exposes forbidden field %q: %s", forbidden, raw)
		}
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal(envelope["interaction"], &facts); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"memoryPolicy", "presenceProjection", "presenceGuidance", "prompt", "trust", "replyFrequency", "participationScore"} {
		if _, ok := facts[forbidden]; ok {
			t.Fatalf("interaction facts expose forbidden field %q: %s", forbidden, envelope["interaction"])
		}
	}
}

func TestDecideParticipationUsesTypedSessionEndpoint(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "participation.decide" || frame.ConversationID != "c/1" {
			t.Fatalf("frame = %#v", frame)
		}
		if frame.EvaluationReason != "message" || len(frame.Messages) != 1 || frame.Messages[0].SenderName != "群友" || !frame.Messages[0].DirectedToBot || !frame.Messages[0].IsNew {
			t.Fatalf("frame = %#v", frame)
		}
		_ = conn.WriteJSON(sessionServerFrame{
			Type: "result", RequestID: frame.RequestID, Payload: json.RawMessage(`{"action":"wait","waitSeconds":7}`),
		})
	})
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	response, err := client.DecideParticipation(t.Context(), "c/1", ParticipationRequest{EvaluationReason: "message", Messages: []AmbientObservation{{
		MessageID: "m1", SenderID: "u1", SenderName: "群友", Text: "不用回", DirectedToBot: true, IsNew: true, TimestampUnixMS: 1,
	}}})
	if err != nil || response.Action != "wait" || response.WaitSeconds == nil || *response.WaitSeconds != 7 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestDecideParticipationRejectsInvalidActionShapes(t *testing.T) {
	for _, body := range []string{
		`{"action":"maybe"}`,
		`{"action":"reply"}`,
		`{"action":"wait","waitSeconds":301}`,
		`{"action":"silent","waitSeconds":1}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := newSessionWSServer(t, func(conn *websocket.Conn) {
				var frame sessionClientFrame
				if err := conn.ReadJSON(&frame); err != nil {
					t.Fatal(err)
				}
				_ = conn.WriteJSON(sessionServerFrame{Type: "result", RequestID: frame.RequestID, Payload: json.RawMessage(body)})
			})
			defer server.Close()
			client, _ := New(Options{Endpoint: server.URL})
			if _, err := client.DecideParticipation(t.Context(), "c1", ParticipationRequest{}); err == nil {
				t.Fatal("invalid action accepted")
			}
		})
	}
}

func TestListMessagesSendsPaginationAndRequiresMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/c%2F1/messages" && r.URL.Path != "/v1/sessions/c/1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("beforeSequence") != "42" || r.URL.Query().Get("limit") != "20" {
			t.Fatalf("query = %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"messages":[{"id":"m","conversationId":"c/1","turnId":"t","sequence":41,"role":"assistant","content":"ok","createdAtUnixMs":1}],"nextBeforeSequence":41}`)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	page, err := client.ListMessages(context.Background(), "c/1", 42, 20)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Sequence != 41 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestVisualAssetUsesBearerAndBoundsType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.EscapedPath() != "/v1/visual-assets/fairy.test/images/idle.png" {
			t.Fatalf("path = %q", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png-bytes"))
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, Token: "exact-token"})
	data, err := client.VisualAsset(context.Background(), "fairy.test", "images/idle.png")
	if err != nil || string(data) != "png-bytes" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := client.VisualAsset(context.Background(), "fairy.test", "../idle.png"); err == nil {
		t.Fatal("traversal asset path accepted")
	}
}

func TestClientStatusUsesBearerAndRejectsInvalidResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"bootstrap":{},"configRoot":"/tmp","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0,"database":{"ready":true,"mode":"production"},"qdrant":{"ready":true,"mode":"production"},"secretKey":{"ready":true,"mode":"production"}}`)
	}))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, Token: "exact-token"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil || status.ConfigRoot != "/tmp" {
		t.Fatalf("status=%#v err=%v", status, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, `{}`)
	}))
	defer bad.Close()
	client, _ = New(Options{Endpoint: bad.URL})
	if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("error = %v", err)
	}
}

func TestOwnerIdentityAdminNeverRequiresRawSubjectInResponse(t *testing.T) {
	const rawSubject = "raw-owner-123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" || r.URL.Path != "/v1/identities/owners" {
			t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut, http.MethodDelete:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["namespace"] != "qq.onebot" || body["subject"] != rawSubject {
				t.Fatalf("body = %#v, %v", body, err)
			}
			if r.Method == http.MethodPut {
				fmt.Fprintf(w, `{"namespace":"qq.onebot","principalDigest":"%s"}`, strings.Repeat("a", 64))
			} else {
				io.WriteString(w, `{"ok":true}`)
			}
		case http.MethodGet:
			fmt.Fprintf(w, `[{"namespace":"qq.onebot","principalDigest":"%s"}]`, strings.Repeat("a", 64))
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, Token: "exact-token"})
	bound, err := client.BindOwnerIdentity(t.Context(), "qq.onebot", rawSubject)
	if err != nil || strings.Contains(bound.PrincipalDigest, rawSubject) {
		t.Fatalf("bound = %#v, %v", bound, err)
	}
	listed, err := client.ListOwnerIdentities(t.Context())
	if err != nil || len(listed) != 1 || listed[0].PrincipalDigest != bound.PrincipalDigest {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	if err := client.UnbindOwnerIdentity(t.Context(), "qq.onebot", rawSubject); err != nil {
		t.Fatal(err)
	}
}

func TestClientFiniteTimeoutAndTurnCallerDeadline(t *testing.T) {
	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"bootstrap":{},"configRoot":"/tmp","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0}`)
	}))
	defer statusServer.Close()
	client, _ := New(Options{Endpoint: statusServer.URL, Timeout: 10 * time.Millisecond})
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("finite status request did not time out")
	}

	turnServer := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
		_ = conn.WriteJSON(sessionServerFrame{
			Type: "result", RequestID: frame.RequestID,
			Payload: json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"ok"}}`),
		})
	})
	defer turnServer.Close()
	client, _ = New(Options{Endpoint: turnServer.URL, Timeout: 10 * time.Millisecond})
	turn, err := client.SubmitTurn(context.Background(), "c1", SubmitTurnRequest{Input: "hello"})
	if err != nil || turn.Outcome.TurnID != "t1" {
		t.Fatalf("turn=%#v err=%v", turn, err)
	}
}

func newSessionWSServer(t *testing.T, handle func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if err := conn.WriteJSON(sessionServerFrame{Type: "ready"}); err != nil {
			return
		}
		handle(conn)
	}))
}

func TestSessionSocketTurnEventOverflowFailsConnection(t *testing.T) {
	sent := make(chan struct{})
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var watch sessionClientFrame
		if err := conn.ReadJSON(&watch); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: watch.RequestID}); err != nil {
			return
		}
		for sequence := 1; sequence <= 65; sequence++ {
			event := TurnEvent{ConversationID: "c1", TurnID: "t1", Sequence: uint64(sequence)}
			if err := conn.WriteJSON(sessionServerFrame{Type: "turn.event", ConversationID: "c1", Event: &event}); err != nil {
				return
			}
		}
		close(sent)
		var ignored sessionClientFrame
		_ = conn.ReadJSON(&ignored)
	})
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if _, err := socket.Watch(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("server did not send overflow fixture")
	}
	select {
	case <-socket.done:
	case <-time.After(time.Second):
		t.Fatal("socket did not terminate after turn-event overflow")
	}
	socket.mu.Lock()
	err = socket.closeErr
	socket.mu.Unlock()
	if !errors.Is(err, ErrTurnEventConsumerOverflow) {
		t.Fatalf("socket error = %v, want ErrTurnEventConsumerOverflow", err)
	}
}

func TestSessionSocketCloseWhileReceivingTurnEventIsRaceFree(t *testing.T) {
	started := make(chan struct{})
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var watch sessionClientFrame
		if err := conn.ReadJSON(&watch); err != nil {
			return
		}
		if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: watch.RequestID}); err != nil {
			return
		}
		close(started)
		for sequence := 1; ; sequence++ {
			event := TurnEvent{ConversationID: "c1", TurnID: "t1", Sequence: uint64(sequence)}
			if err := conn.WriteJSON(sessionServerFrame{Type: "turn.event", ConversationID: "c1", Event: &event}); err != nil {
				return
			}
		}
	})
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := socket.Watch(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range stream {
		}
	}()
	if err := socket.Close(); err != nil && !errors.Is(err, websocket.ErrCloseSent) {
		t.Fatalf("Close() error = %v", err)
	}
	wg.Wait()
}

func TestClientRejectsSecretWhitespaceAndRedactsServerErrors(t *testing.T) {
	if _, err := New(Options{Token: " secret "}); err == nil {
		t.Fatal("token whitespace accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"provider Authorization: Bearer abc api_key=sk-test"}`)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	_, err := client.Status(context.Background())
	if err == nil || strings.Contains(err.Error(), "abc") || strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("error leaked credential: %v", err)
	}
}

func TestAdminRejectsMalformedAndOversizedJSONBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	if _, err := client.ApplyConfig(context.Background(), "model", []byte(`{"missing"`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	oversized := []byte(`{"value":"` + strings.Repeat("x", maxRequestBody) + `"}`)
	if _, err := client.CreateCharacter(context.Background(), oversized); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestClientRejectsOversizedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":"`+strings.Repeat("x", maxJSONBody)+`"}`)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestSSEDecoderRejectsIncompleteAndOversizedFrames(t *testing.T) {
	if _, err := NewSSEDecoder(strings.NewReader("event: log\ndata: {}")).Next(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete error = %v", err)
	}
	oversized := "data: " + strings.Repeat("x", maxSSELine) + "\n\n"
	if _, err := NewSSEDecoder(strings.NewReader(oversized)).Next(); err == nil || !strings.Contains(err.Error(), "line exceeds") {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestOpenLogsReadyTimeoutAndDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not a flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: ready\ndata: {\"ok\":true}\n\n")
		flusher.Flush()
		time.Sleep(60 * time.Millisecond)
		io.WriteString(w, "id: 1\nevent: log\ndata: {\"sequence\":1,\"timestampUnixMs\":1,\"level\":\"warn\",\"logger\":\"test\",\"message\":\"late\",\"messageTruncated\":false,\"fields\":[],\"fieldsTruncated\":false}\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, Timeout: 10 * time.Millisecond})
	stream, err := client.OpenLogs(context.Background(), LogQuery{}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	event, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := DecodeLogEntry(event)
	if err != nil || entry.Message != "late" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}

	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer blocked.Close()
	client, _ = New(Options{Endpoint: blocked.URL})
	if _, err := client.OpenLogs(context.Background(), LogQuery{}, 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "ready timeout") {
		t.Fatalf("ready timeout error = %v", err)
	}
}

func TestSSEDecoderParsesMultilineData(t *testing.T) {
	event, err := NewSSEDecoder(strings.NewReader("id: 4\nevent: log\ndata: one\ndata: two\n\n")).Next()
	if err != nil || event.ID != "4" || event.Event != "log" || string(event.Data) != "one\ntwo" {
		t.Fatalf("event=%#v err=%v", event, err)
	}
}

func TestLogQueryValidation(t *testing.T) {
	client, _ := New(Options{})
	if _, err := client.Logs(context.Background(), LogQuery{Level: "verbose"}); err == nil {
		t.Fatal("invalid level accepted")
	}
	if _, err := client.OpenLogs(context.Background(), LogQuery{Limit: 2}, time.Second); err == nil {
		t.Fatal("stream limit accepted")
	}
}

func TestDecodeTurnEventRejectsMissingFields(t *testing.T) {
	_, err := DecodeTurnEvent(SSEEvent{Data: []byte(`{"conversationId":"c"}`)})
	if err == nil {
		t.Fatal("incomplete turn event accepted")
	}
}

func TestClientErrorSupportsErrorsAs(t *testing.T) {
	err := &Error{Action: "read", Endpoint: "http://example.test", Status: 401, Message: "unauthorized"}
	var target *Error
	if !errors.As(err, &target) || !strings.Contains(fmt.Sprint(err), "401") {
		t.Fatalf("error = %v", err)
	}
}
