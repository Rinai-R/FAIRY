package session

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

	"github.com/gorilla/websocket"
)

func TestOpenSessionSendsEndpointFacts(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "session.open" || frame.Endpoint != EndpointIM || frame.EndpointKey != "onebot-group:123" || frame.Interaction.Audience != AudienceMulti || !frame.OutputCapabilities.Sticker {
			t.Fatalf("frame = %#v", frame)
		}
		_ = conn.WriteJSON(sessionServerFrame{
			Type: "session.opened", RequestID: frame.RequestID,
			ConversationID: "c1", CharacterID: "ch1", MessageCount: 0, Endpoint: EndpointIM,
		})
	})
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.OpenSession(context.Background(), OpenSessionRequest{
		Endpoint: EndpointIM, EndpointKey: "onebot-group:123",
		Interaction:        Context{Audience: AudienceMulti, Initiation: InitiationAmbient, Presentation: PresentationChat},
		OutputCapabilities: OutputCapabilities{Sticker: true},
	})
	if err != nil || response.ConversationID != "c1" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestOpenSessionRequestExposesOnlyInteractionFacts(t *testing.T) {
	raw, err := json.Marshal(OpenSessionRequest{
		Endpoint:    EndpointIM,
		EndpointKey: "onebot-group:123",
		Interaction: Context{Audience: AudienceMulti, Initiation: InitiationAmbient, Presentation: PresentationChat},
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
			t.Fatalf("open exposes forbidden field %q: %s", forbidden, raw)
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
		if frame.EvaluationReason != "message" || len(frame.Messages) != 1 || frame.Messages[0].SenderName != "群友" || !frame.Messages[0].DirectedToBot || !frame.Messages[0].IsNew ||
			len(frame.Messages[0].Mentions) != 1 || frame.Messages[0].Mentions[0] != (MessageMention{UserID: "u2", DisplayName: "小明"}) {
			t.Fatalf("frame = %#v", frame)
		}
		_ = conn.WriteJSON(sessionServerFrame{
			Type: "result", RequestID: frame.RequestID, Payload: json.RawMessage(`{"action":"wait","waitSeconds":7}`),
		})
	})
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	response, err := client.DecideParticipation(t.Context(), "c/1", ParticipationRequest{EvaluationReason: "message", Messages: []AmbientObservation{{
		MessageID: "m1", SenderID: "u1", SenderName: "群友", Text: "@小明 不用回", Mentions: []MessageMention{{UserID: "u2", DisplayName: "小明"}}, DirectedToBot: true, IsNew: true, TimestampUnixMS: 1,
	}}})
	if err != nil || response.Action != "wait" || response.WaitSeconds == nil || *response.WaitSeconds != 7 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestObserveDesktopUsesTypedSessionFrame(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "desktop.observe" || frame.ConversationID != "c1" || frame.DesktopObservation == nil {
			t.Fatalf("frame = %#v", frame)
		}
		if frame.DesktopObservation.ObservationID != "obs-1" || frame.DesktopObservation.Activity != DesktopActivityWorking || frame.DesktopObservation.Privacy != DesktopPrivacyNormal {
			t.Fatalf("observation = %#v", frame.DesktopObservation)
		}
		_ = conn.WriteJSON(sessionServerFrame{Type: "result", RequestID: frame.RequestID, Payload: json.RawMessage(`{"action":"silent","nodes":[]}`)})
	})
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ObserveDesktop(t.Context(), "c1", DesktopObservation{
		ObservationID: "obs-1", TimestampUnixMS: time.Now().UnixMilli(), Trigger: DesktopTriggerPeriodic,
		Activity: DesktopActivityWorking, Lifecycle: DesktopLifecycleNone, Privacy: DesktopPrivacyNormal,
	})
	if err != nil || result.Action != "silent" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestReportExpressionDeliveryUsesCorrelatedSessionFrame(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "expression.delivery" || frame.DeliveryResult == nil ||
			frame.DeliveryResult.BeatID != "final-0" || frame.DeliveryResult.Status != ExpressionDeliverySucceeded {
			t.Fatalf("delivery frame = %#v", frame)
		}
		_ = conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: "conversation-1"})
	})
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := socket.ReportExpressionDelivery(t.Context(), ExpressionDeliveryResult{
		ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "final-0",
		Status: ExpressionDeliverySucceeded,
	}); err != nil {
		t.Fatal(err)
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
		io.WriteString(w, `{"bootstrap":{},"configRoot":"/tmp","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0,"database":{"ready":true,"mode":"production"},"secretKey":{"ready":true,"mode":"production"}}`)
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

func TestSessionSocketSubmitTurnPreservesExternalMessageID(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Error(err)
			return
		}
		if frame.Type != "turn.submit" || frame.ConversationID != "c1" || frame.Input != "hello" || frame.MessageID != "qq-private-42" {
			t.Errorf("submit frame = %#v", frame)
			return
		}
		_ = conn.WriteJSON(sessionServerFrame{
			Type: "result", RequestID: frame.RequestID,
			Payload: json.RawMessage(`{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"ok"}}`),
		})
	})
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.SubmitTurn(t.Context(), "c1", SubmitTurnRequest{Input: "hello", MessageID: "qq-private-42"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome.TurnID != "t1" {
		t.Fatalf("turn = %#v", response)
	}
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

func TestSessionSocketRequestCapacityRejectsBeforeWireAndRecovers(t *testing.T) {
	var received atomic.Int64
	firstTwo := make(chan struct{})
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		for {
			var frame sessionClientFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			count := received.Add(1)
			if count == 2 {
				close(firstTwo)
			}
			if count > 2 {
				if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID}); err != nil {
					return
				}
			}
		}
	})
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	socket.mu.Lock()
	socket.requestCapacity = 2
	socket.mu.Unlock()

	requestCtx, cancelRequests := context.WithCancel(t.Context())
	var requests sync.WaitGroup
	requests.Add(2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			defer requests.Done()
			_ = socket.ObserveAmbient(requestCtx, "conversation-1", AmbientObservation{MessageID: fmt.Sprintf("message-%d", index)})
		}(index)
	}
	select {
	case <-firstTwo:
	case <-time.After(time.Second):
		t.Fatal("server did not receive the admitted requests")
	}
	if err := socket.ObserveAmbient(t.Context(), "conversation-1", AmbientObservation{MessageID: "overload"}); !errors.Is(err, ErrSessionRequestCapacity) {
		t.Fatalf("overload error = %v, want ErrSessionRequestCapacity", err)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("wire frames = %d, want 2", got)
	}

	cancelRequests()
	requests.Wait()
	socket.mu.Lock()
	pending := len(socket.pending)
	socket.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending requests after cancellation = %d", pending)
	}
	if err := socket.ObserveAmbient(t.Context(), "conversation-1", AmbientObservation{MessageID: "replacement"}); err != nil {
		t.Fatalf("request after release error = %v", err)
	}
	if got := received.Load(); got != 3 {
		t.Fatalf("wire frames after release = %d, want 3", got)
	}
}

func TestSessionSocketWatchCapacityAndCommittedReuse(t *testing.T) {
	var received atomic.Int64
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		for {
			var frame sessionClientFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			received.Add(1)
			if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID}); err != nil {
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
	defer socket.Close()
	socket.mu.Lock()
	socket.watchCapacity = 2
	socket.mu.Unlock()

	first, err := socket.Watch(t.Context(), "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := socket.Watch(t.Context(), "conversation-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.Watch(t.Context(), "conversation-3"); !errors.Is(err, ErrSessionWatchCapacity) {
		t.Fatalf("watch overload error = %v, want ErrSessionWatchCapacity", err)
	}
	reused, err := socket.Watch(t.Context(), "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if reused != first {
		t.Fatal("committed Watch did not reuse the turn channel")
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("watch wire frames = %d, want 2", got)
	}
	socket.mu.Lock()
	turnOwners, participationOwners, attempts := len(socket.turnEvents), len(socket.participationEvents), len(socket.watchAttempts)
	socket.mu.Unlock()
	if turnOwners != 2 || participationOwners != 2 || attempts != 0 {
		t.Fatalf("watch owners turn=%d participation=%d attempts=%d", turnOwners, participationOwners, attempts)
	}
}

func TestSessionSocketConcurrentWatchUsesSingleWireOwner(t *testing.T) {
	var received atomic.Int64
	firstReceived := make(chan struct{})
	releaseReply := make(chan struct{})
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		for {
			var frame sessionClientFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			if received.Add(1) == 1 {
				close(firstReceived)
				<-releaseReply
			}
			if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID}); err != nil {
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
	defer socket.Close()

	type watchResult struct {
		ch  <-chan TurnEvent
		err error
	}
	leader := make(chan watchResult, 1)
	follower := make(chan watchResult, 1)
	go func() {
		ch, watchErr := socket.Watch(t.Context(), "conversation-1")
		leader <- watchResult{ch: ch, err: watchErr}
	}()
	<-firstReceived
	go func() {
		ch, watchErr := socket.Watch(t.Context(), "conversation-1")
		follower <- watchResult{ch: ch, err: watchErr}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		socket.mu.Lock()
		attempt := socket.watchAttempts["conversation-1"]
		waiters := 0
		if attempt != nil {
			waiters = attempt.waiters
		}
		socket.mu.Unlock()
		if waiters == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent Watch did not join the existing attempt")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseReply)
	leaderResult := <-leader
	followerResult := <-follower
	if leaderResult.err != nil || followerResult.err != nil {
		t.Fatalf("watch errors leader=%v follower=%v", leaderResult.err, followerResult.err)
	}
	if leaderResult.ch != followerResult.ch {
		t.Fatal("concurrent Watch calls returned different channels")
	}
	if got := received.Load(); got != 1 {
		t.Fatalf("watch wire frames = %d, want 1", got)
	}
}

func TestSessionSocketConcurrentWatchFollowerCancellationKeepsOwner(t *testing.T) {
	var received atomic.Int64
	firstReceived := make(chan struct{})
	releaseReply := make(chan struct{})
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		for {
			var frame sessionClientFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			if received.Add(1) == 1 {
				close(firstReceived)
				<-releaseReply
			}
			if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID}); err != nil {
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
	defer socket.Close()

	type watchResult struct {
		ch  <-chan TurnEvent
		err error
	}
	leader := make(chan watchResult, 1)
	go func() {
		ch, watchErr := socket.Watch(t.Context(), "conversation-1")
		leader <- watchResult{ch: ch, err: watchErr}
	}()
	<-firstReceived

	followerCtx, cancelFollower := context.WithCancel(t.Context())
	follower := make(chan error, 1)
	go func() {
		_, watchErr := socket.Watch(followerCtx, "conversation-1")
		follower <- watchErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		socket.mu.Lock()
		attempt := socket.watchAttempts["conversation-1"]
		waiters := 0
		if attempt != nil {
			waiters = attempt.waiters
		}
		socket.mu.Unlock()
		if waiters == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceling Watch did not join the existing attempt")
		}
		time.Sleep(time.Millisecond)
	}
	cancelFollower()
	if followerErr := <-follower; !errors.Is(followerErr, context.Canceled) {
		t.Fatalf("follower error = %v, want context.Canceled", followerErr)
	}
	socket.mu.Lock()
	attempt := socket.watchAttempts["conversation-1"]
	waiters := 0
	if attempt != nil {
		waiters = attempt.waiters
	}
	socket.mu.Unlock()
	if attempt == nil || waiters != 0 {
		t.Fatalf("leader attempt after follower cancellation = %v, waiters = %d", attempt != nil, waiters)
	}
	if got := received.Load(); got != 1 {
		t.Fatalf("watch wire frames before leader reply = %d, want 1", got)
	}

	close(releaseReply)
	leaderResult := <-leader
	if leaderResult.err != nil {
		t.Fatalf("leader Watch error = %v", leaderResult.err)
	}
	reused, err := socket.Watch(t.Context(), "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if reused != leaderResult.ch {
		t.Fatal("follower cancellation replaced the leader channel")
	}
	if got := received.Load(); got != 1 {
		t.Fatalf("watch wire frames after committed reuse = %d, want 1", got)
	}
}

func TestSessionSocketWatchDrainsAckedEventBeforeDisconnect(t *testing.T) {
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		var frame sessionClientFrame
		if err := conn.ReadJSON(&frame); err != nil {
			return
		}
		if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID}); err != nil {
			return
		}
		if err := conn.WriteJSON(sessionServerFrame{
			Type: "turn.event", ConversationID: "conversation-1",
			Event: &TurnEvent{
				ConversationID: "conversation-1", TurnID: "turn-1", Sequence: 1,
				State: "responding", Payload: json.RawMessage(`{"type":"state_changed"}`),
			},
		}); err != nil {
			return
		}
		_ = conn.Close()
	})
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	socket, err := client.DialSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()

	events, err := socket.Watch(t.Context(), "conversation-1")
	if err != nil {
		t.Fatalf("Watch error after ack = %v", err)
	}
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed before draining the acknowledged event")
		}
		if event.TurnID != "turn-1" {
			t.Fatalf("turn ID = %q, want turn-1", event.TurnID)
		}
	case <-time.After(time.Second):
		t.Fatal("acknowledged event was not delivered")
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("event channel remained open after disconnect")
		}
	case <-time.After(time.Second):
		t.Fatal("event channel did not close after disconnect")
	}
}

func TestSessionSocketConcurrentWatchSharesFailureAndAllowsRetry(t *testing.T) {
	var received atomic.Int64
	firstReceived := make(chan struct{})
	releaseFailure := make(chan struct{})
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		for {
			var frame sessionClientFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			count := received.Add(1)
			if count == 1 {
				close(firstReceived)
				<-releaseFailure
				if err := conn.WriteJSON(sessionServerFrame{Type: "error", RequestID: frame.RequestID, Error: "watch rejected"}); err != nil {
					return
				}
				continue
			}
			if err := conn.WriteJSON(sessionServerFrame{Type: "ack", RequestID: frame.RequestID}); err != nil {
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
	defer socket.Close()

	results := make(chan error, 2)
	go func() {
		_, watchErr := socket.Watch(t.Context(), "conversation-1")
		results <- watchErr
	}()
	<-firstReceived
	go func() {
		_, watchErr := socket.Watch(t.Context(), "conversation-1")
		results <- watchErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		socket.mu.Lock()
		attempt := socket.watchAttempts["conversation-1"]
		waiters := 0
		if attempt != nil {
			waiters = attempt.waiters
		}
		socket.mu.Unlock()
		if waiters == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failing Watch did not share the existing attempt")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFailure)
	for index := 0; index < 2; index++ {
		if watchErr := <-results; watchErr == nil || watchErr.Error() != "watch rejected" {
			t.Fatalf("shared watch error = %v", watchErr)
		}
	}
	socket.mu.Lock()
	_, turnRetained := socket.turnEvents["conversation-1"]
	_, participationRetained := socket.participationEvents["conversation-1"]
	_, attemptRetained := socket.watchAttempts["conversation-1"]
	socket.mu.Unlock()
	if turnRetained || participationRetained || attemptRetained {
		t.Fatal("failed Watch retained temporary owners")
	}
	if _, err := socket.Watch(t.Context(), "conversation-1"); err != nil {
		t.Fatalf("Watch retry error = %v", err)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("watch wire frames after retry = %d, want 2", got)
	}
}

func TestSessionSocketOpenedConversationCapacityTerminatesSocket(t *testing.T) {
	var received atomic.Int64
	server := newSessionWSServer(t, func(conn *websocket.Conn) {
		for {
			var frame sessionClientFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			index := received.Add(1)
			if err := conn.WriteJSON(sessionServerFrame{
				Type: "session.opened", RequestID: frame.RequestID,
				ConversationID: fmt.Sprintf("conversation-%d", index), CharacterID: "character-1",
				Endpoint: EndpointDesktop,
			}); err != nil {
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
	socket.mu.Lock()
	socket.openedConversationCapacity = 2
	socket.mu.Unlock()
	request := OpenSessionRequest{
		Endpoint: EndpointDesktop, EndpointKey: "desktop:test",
		Interaction: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationEmbodied},
	}
	if _, err := socket.OpenSession(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.OpenSession(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.OpenSession(t.Context(), request); !errors.Is(err, ErrSessionOpenedConversationCapacity) {
		t.Fatalf("opened overload error = %v, want ErrSessionOpenedConversationCapacity", err)
	}
	select {
	case <-socket.done:
	case <-time.After(time.Second):
		t.Fatal("socket did not terminate after opened projection overflow")
	}
	socket.mu.Lock()
	opened, pending, attempts := len(socket.openedConversations), len(socket.pending), len(socket.watchAttempts)
	socket.mu.Unlock()
	if opened != 0 || pending != 0 || attempts != 0 {
		t.Fatalf("closed socket retained opened=%d pending=%d attempts=%d", opened, pending, attempts)
	}
	if got := received.Load(); got != 3 {
		t.Fatalf("open wire frames = %d, want 3", got)
	}
	if err := socket.Close(); err != nil && !errors.Is(err, websocket.ErrCloseSent) {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-socket.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() remained open after Close()")
	}
	if socket.Err() == nil {
		t.Fatal("Err() = nil after Close()")
	}
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
