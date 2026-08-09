package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	turn "fairy/agent/conversation"
	"fairy/transport/session"

	"github.com/gorilla/websocket"
)

type testTurnRuntime struct{ service *turn.Service }

func (r testTurnRuntime) OutputCapabilities(conversationID string) session.OutputCapabilities {
	return r.service.OutputCapabilities(conversationID)
}
func (r testTurnRuntime) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	return r.service.ReportExpressionDelivery(result)
}
func (r testTurnRuntime) BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error {
	return r.service.BindOutputCapabilities(ownerID, conversationID, capabilities)
}
func (r testTurnRuntime) UnbindOutputCapabilities(ownerID, conversationID string) {
	r.service.UnbindOutputCapabilities(ownerID, conversationID)
}
func (r testTurnRuntime) SubmitTurn(request TurnSubmission) (any, error) {
	return r.service.SubmitTurn(turn.SubmitTurnRequest{ConversationID: request.ConversationID, Input: request.Input, MessageID: request.MessageID})
}
func (r testTurnRuntime) CancelTurn(conversationID, turnID string) error {
	return r.service.CancelTurn(conversationID, turnID)
}
func (r testTurnRuntime) BindInteraction(conversationID string, binding session.Binding) error {
	return r.service.BindInteraction(conversationID, binding)
}
func (r testTurnRuntime) ActiveBackgroundJobs() int64        { return r.service.ActiveBackgroundJobs() }
func (r testTurnRuntime) AgentLoopMetrics() AgentLoopMetrics { return AgentLoopMetrics{} }

type recordingTurnRuntime struct {
	testTurnRuntime
	mu        sync.Mutex
	reported  []session.ExpressionDeliveryResult
	reportErr error
}

func (r *recordingTurnRuntime) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reported = append(r.reported, result)
	return r.reportErr
}

func (r *recordingTurnRuntime) reportedResults() []session.ExpressionDeliveryResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]session.ExpressionDeliveryResult(nil), r.reported...)
}

func newDeliveryTestConnection(t *testing.T, runtime TurnRuntime, watched ...string) (*sessionConn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *sessionConn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		connection := newSessionConn(&Server{rt: &Dependencies{Turns: runtime}}, conn)
		for _, conversationID := range watched {
			connection.watches[conversationID] = func() {}
		}
		accepted <- connection
	}))
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		httpServer.Close()
		t.Fatal(err)
	}
	connection := <-accepted
	t.Cleanup(func() {
		connection.shutdown(nil)
		_ = client.Close()
		httpServer.Close()
	})
	return connection, client
}

func TestSessionExpressionDeliveryUsesWatchOwnershipNotStickerCapability(t *testing.T) {
	runtime := &recordingTurnRuntime{}
	connection, client := newDeliveryTestConnection(t, runtime, "conversation-1")
	result := session.ExpressionDeliveryResult{
		ConversationID: "conversation-1",
		TurnID:         "turn-1",
		BeatID:         "final-0",
		Status:         session.ExpressionDeliverySucceeded,
	}
	connection.handleExpressionDelivery(wsClientFrame{RequestID: "request-1", DeliveryResult: &result})
	var response wsServerFrame
	if err := client.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "ack" || response.ConversationID != result.ConversationID {
		t.Fatalf("response = %#v", response)
	}
	if got := runtime.reportedResults(); len(got) != 1 || got[0] != result {
		t.Fatalf("reported = %#v", got)
	}
}

func TestSessionExpressionDeliveryRejectsUnwatchedConversation(t *testing.T) {
	runtime := &recordingTurnRuntime{}
	connection, client := newDeliveryTestConnection(t, runtime)
	result := session.ExpressionDeliveryResult{
		ConversationID: "conversation-1",
		TurnID:         "turn-1",
		BeatID:         "final-0",
		Status:         session.ExpressionDeliverySucceeded,
	}
	connection.handleExpressionDelivery(wsClientFrame{RequestID: "request-1", DeliveryResult: &result})
	var response wsServerFrame
	if err := client.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error != "delivery report is unavailable for this session" {
		t.Fatalf("response = %#v", response)
	}
	if got := runtime.reportedResults(); len(got) != 0 {
		t.Fatalf("unwatched delivery reached registry: %#v", got)
	}
}

func TestSessionExpressionDeliveryRejectsInvalidOrUnknownIdentity(t *testing.T) {
	t.Run("invalid frame identity", func(t *testing.T) {
		runtime := &recordingTurnRuntime{}
		connection, client := newDeliveryTestConnection(t, runtime, "conversation-1")
		result := session.ExpressionDeliveryResult{
			ConversationID: "conversation-1",
			TurnID:         "turn-1",
			Status:         session.ExpressionDeliverySucceeded,
		}
		connection.handleExpressionDelivery(wsClientFrame{RequestID: "request-1", DeliveryResult: &result})
		var response wsServerFrame
		if err := client.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.Type != "error" || response.Error != "expression delivery identity is required" {
			t.Fatalf("response = %#v", response)
		}
		if got := runtime.reportedResults(); len(got) != 0 {
			t.Fatalf("invalid delivery reached registry: %#v", got)
		}
	})

	t.Run("registry identity mismatch", func(t *testing.T) {
		runtime := &recordingTurnRuntime{reportErr: errors.New("expression delivery is not pending")}
		connection, client := newDeliveryTestConnection(t, runtime, "conversation-1")
		result := session.ExpressionDeliveryResult{
			ConversationID: "conversation-1",
			TurnID:         "unknown-turn",
			BeatID:         "unknown-beat",
			Status:         session.ExpressionDeliverySucceeded,
		}
		connection.handleExpressionDelivery(wsClientFrame{RequestID: "request-1", DeliveryResult: &result})
		var response wsServerFrame
		if err := client.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.Type != "error" || response.Error != runtime.reportErr.Error() {
			t.Fatalf("response = %#v", response)
		}
	})
}

func TestSessionPlaneDocumentsWebSocketOnly(t *testing.T) {
	removed := []string{
		"POST /v1/sessions",
		"POST /v1/sessions/:id/turns",
		"POST /v1/sessions/:id/group-participation",
		"GET /v1/sessions/:id/events",
		"POST /v1/sessions/:id/turns/:turnId/cancel",
	}
	if len(removed) != 5 {
		t.Fatalf("removed session HTTP/SSE routes = %d", len(removed))
	}
}

func TestSessionConnOverflowSendsTryAgainLaterClose(t *testing.T) {
	connected := make(chan struct{})
	shutdown := make(chan struct{})
	var handler sync.WaitGroup
	handler.Add(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer handler.Done()
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		session := &sessionConn{conn: conn, watches: make(map[string]func())}
		close(connected)
		<-shutdown
		session.shutdown(ErrEventSubscriberOverflow)
	}))
	defer func() {
		server.Close()
		handler.Wait()
	}()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-connected
	close(shutdown)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("read error = %v, want websocket.CloseError", err)
	}
	if closeErr.Code != websocket.CloseTryAgainLater || closeErr.Text != ErrEventSubscriberOverflow.Error() {
		t.Fatalf("close = %#v", closeErr)
	}
}

func TestForwardTurnEventsPreservesOverflowReasonAfterEventStreamCloses(t *testing.T) {
	events := make(chan session.Event)
	failures := make(chan error, 1)
	close(events)
	failures <- ErrEventSubscriberOverflow
	close(failures)
	subscription := EventSubscription{Events: events, Failures: failures}

	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		session := &sessionConn{conn: conn, watches: map[string]func(){"c1": subscription.Unsubscribe}}
		session.forwardTurnEvents("c1", subscription)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseTryAgainLater {
		t.Fatalf("read error = %v, want 1013 close", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("forward turn event did not terminate")
	}
}

func TestSessionOutputCapabilitiesFollowConnectionLifetime(t *testing.T) {
	companionService := turn.NewService()
	apiServer := &Server{rt: &Dependencies{Turns: testTurnRuntime{service: companionService}}}
	accepted := make(chan *sessionConn, 2)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- newSessionConn(apiServer, conn)
	}))
	defer httpServer.Close()

	dial := func() (*websocket.Conn, *sessionConn) {
		t.Helper()
		url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
		client, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		return client, <-accepted
	}
	client1, connection1 := dial()
	defer client1.Close()
	client2, connection2 := dial()
	defer client2.Close()

	const conversationID = "conversation-1"
	if err := connection1.bindOutputCapabilities(conversationID, session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if err := connection1.bindOutputCapabilities(conversationID, session.OutputCapabilities{}); err != nil {
		t.Fatal(err)
	}
	if companionService.OutputCapabilities(conversationID).Sticker {
		t.Fatal("same connection did not replace its prior capability")
	}
	if err := connection1.bindOutputCapabilities(conversationID, session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if err := connection2.bindOutputCapabilities(conversationID, session.OutputCapabilities{}); err != nil {
		t.Fatal(err)
	}
	connection1.shutdown(ErrEventSubscriberOverflow)
	if companionService.OutputCapabilities(conversationID).Sticker {
		t.Fatal("closing true-capability connection left its lease active")
	}
	if err := connection2.bindOutputCapabilities(conversationID, session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	connection1.shutdown(nil)
	if !companionService.OutputCapabilities(conversationID).Sticker {
		t.Fatal("old connection shutdown removed another connection's lease")
	}
	connection2.shutdown(nil)
	if companionService.OutputCapabilities(conversationID).Sticker {
		t.Fatal("last connection shutdown retained sticker capability")
	}
}

func TestSessionOutputCapabilitiesMissingFieldDefaultsFalse(t *testing.T) {
	var frame wsClientFrame
	if err := json.Unmarshal([]byte(`{"type":"session.open"}`), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.OutputCapabilities.Sticker {
		t.Fatal("missing outputCapabilities.sticker defaulted to true")
	}
}

func TestSessionOutputCapabilitiesBindRollsBackAfterShutdown(t *testing.T) {
	companionService := turn.NewService()
	connection := &sessionConn{
		server:             &Server{rt: &Dependencies{Turns: testTurnRuntime{service: companionService}}},
		ownerID:            "owner-1",
		capabilityBindings: make(map[string]struct{}),
	}
	connection.watchMu.Lock()
	connection.closed = true
	connection.watchMu.Unlock()
	if err := connection.bindOutputCapabilities("conversation-1", session.OutputCapabilities{Sticker: true}); err == nil {
		t.Fatal("capability bind succeeded after shutdown")
	}
	if companionService.OutputCapabilities("conversation-1").Sticker {
		t.Fatal("bind after shutdown retained a stale lease")
	}
}

func TestSessionOutputCapabilitiesOpenShutdownRaceReleasesLease(t *testing.T) {
	companionService := turn.NewService()
	apiServer := &Server{rt: &Dependencies{Turns: testTurnRuntime{service: companionService}}}
	accepted := make(chan *sessionConn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- newSessionConn(apiServer, conn)
	}))
	defer httpServer.Close()

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	connection := <-accepted
	start := make(chan struct{})
	bindDone := make(chan error, 1)
	go func() {
		<-start
		bindDone <- connection.bindOutputCapabilities("conversation-race", session.OutputCapabilities{Sticker: true})
	}()
	shutdownDone := make(chan struct{})
	go func() {
		<-start
		connection.shutdown(nil)
		close(shutdownDone)
	}()
	close(start)
	bindErr := <-bindDone
	<-shutdownDone
	if bindErr != nil && !errors.Is(bindErr, errSessionConnectionClosed) {
		t.Fatalf("bind/shutdown race error = %v", bindErr)
	}
	if companionService.OutputCapabilities("conversation-race").Sticker {
		t.Fatal("open/shutdown race retained a stale capability lease")
	}
}

func TestSessionConnectionBoundsCapabilityAssociations(t *testing.T) {
	companionService := turn.NewService()
	connection := &sessionConn{
		server:              &Server{rt: &Dependencies{Turns: testTurnRuntime{service: companionService}}},
		ownerID:             "owner-capacity",
		capabilityBindings:  make(map[string]struct{}),
		associationCapacity: 2,
	}
	if err := connection.bindOutputCapabilities("conversation-1", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if err := connection.bindOutputCapabilities("conversation-2", session.OutputCapabilities{Sticker: true}); err != nil {
		t.Fatal(err)
	}
	if err := connection.bindOutputCapabilities("conversation-1", session.OutputCapabilities{}); err != nil {
		t.Fatalf("same conversation replacement = %v", err)
	}
	if err := connection.bindOutputCapabilities("conversation-3", session.OutputCapabilities{Sticker: true}); !errors.Is(err, errSessionAssociationCapacity) {
		t.Fatalf("overflow bind error = %v", err)
	}
	if companionService.OutputCapabilities("conversation-3").Sticker {
		t.Fatal("overflow bind created a capability lease")
	}
	connection.watchMu.Lock()
	bindings := len(connection.capabilityBindings)
	connection.watchMu.Unlock()
	if bindings != 2 {
		t.Fatalf("capability bindings = %d, want 2", bindings)
	}
	companionService.UnbindOutputCapabilities(connection.ownerID, "conversation-1")
	companionService.UnbindOutputCapabilities(connection.ownerID, "conversation-2")
}

func TestSessionConnectionRollsBackAssociationWhenGlobalCapabilityCapacityIsFull(t *testing.T) {
	companionService := turn.NewService()
	for index := 0; index < turn.OutputCapabilityLeaseCapacity; index++ {
		id := fmt.Sprintf("existing-%d", index)
		if err := companionService.BindOutputCapabilities(id, id, session.OutputCapabilities{}); err != nil {
			t.Fatal(err)
		}
	}
	connection := &sessionConn{
		server:              &Server{rt: &Dependencies{Turns: testTurnRuntime{service: companionService}}},
		ownerID:             "overflow-owner",
		capabilityBindings:  make(map[string]struct{}),
		captureRoutes:       make(map[string]captureRegistration),
		associationCapacity: 2,
	}
	err := connection.bindOutputCapabilities("overflow-conversation", session.OutputCapabilities{Sticker: true})
	if !errors.Is(err, turn.ErrOutputCapabilityCapacity) {
		t.Fatalf("bind error = %v, want ErrOutputCapabilityCapacity", err)
	}
	if len(connection.capabilityBindings) != 0 || len(connection.captureRoutes) != 0 {
		t.Fatalf("rejected bind retained associations=%d captureRoutes=%d", len(connection.capabilityBindings), len(connection.captureRoutes))
	}
	if companionService.OutputCapabilities("overflow-conversation").Sticker {
		t.Fatal("rejected bind retained global capability")
	}
}

func TestSessionConnectionBoundsWatchSubscriptionsBeforeSideEffects(t *testing.T) {
	var turnSubscriptions atomic.Int64
	var participationSubscriptions atomic.Int64
	apiServer := &Server{rt: &Dependencies{
		SubscribeTurnEvents: func(string) (EventSubscription, error) {
			turnSubscriptions.Add(1)
			events := make(chan session.Event)
			failures := make(chan error)
			var once sync.Once
			return EventSubscription{Events: events, Failures: failures, Cancel: func() {
				once.Do(func() {
					close(events)
					close(failures)
				})
			}}, nil
		},
		SubscribeParticipation: func(string) (ParticipationSubscription, error) {
			participationSubscriptions.Add(1)
			events := make(chan ParticipationEvent)
			failures := make(chan error)
			var once sync.Once
			return ParticipationSubscription{Events: events, Failures: failures, Cancel: func() {
				once.Do(func() {
					close(events)
					close(failures)
				})
			}}, nil
		},
	}}
	accepted := make(chan *sessionConn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		connection := newSessionConn(apiServer, conn)
		connection.watchCapacity = 2
		accepted <- connection
	}))
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	connection := <-accepted
	for index, conversationID := range []string{"conversation-1", "conversation-1", "conversation-2", "conversation-3"} {
		connection.handleWatch(wsClientFrame{RequestID: fmt.Sprintf("request-%d", index), ConversationID: conversationID})
		var response wsServerFrame
		if err := client.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if index < 3 && response.Type != "ack" {
			t.Fatalf("response %d = %#v", index, response)
		}
		if index == 3 && (response.Type != "error" || response.Error != errSessionWatchCapacity.Error()) {
			t.Fatalf("overflow response = %#v", response)
		}
	}
	if turnSubscriptions.Load() != 2 || participationSubscriptions.Load() != 2 {
		t.Fatalf("subscription calls = %d/%d, want 2/2", turnSubscriptions.Load(), participationSubscriptions.Load())
	}
	connection.shutdown(nil)
}

func TestSessionConnectionRollsBackPartialWatchAdmission(t *testing.T) {
	var activeTurnSubscriptions atomic.Int64
	apiServer := &Server{rt: &Dependencies{
		SubscribeTurnEvents: func(string) (EventSubscription, error) {
			activeTurnSubscriptions.Add(1)
			events := make(chan session.Event)
			failures := make(chan error)
			var once sync.Once
			return EventSubscription{Events: events, Failures: failures, Cancel: func() {
				once.Do(func() {
					activeTurnSubscriptions.Add(-1)
					close(events)
					close(failures)
				})
			}}, nil
		},
		SubscribeParticipation: func(string) (ParticipationSubscription, error) {
			return ParticipationSubscription{}, ErrParticipationSubscriberCapacity
		},
	}}
	accepted := make(chan *sessionConn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- newSessionConn(apiServer, conn)
	}))
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	connection := <-accepted

	connection.handleWatch(wsClientFrame{RequestID: "request-1", ConversationID: "conversation-1"})
	var response wsServerFrame
	if err := client.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error != ErrParticipationSubscriberCapacity.Error() {
		t.Fatalf("response = %#v", response)
	}
	if got := activeTurnSubscriptions.Load(); got != 0 {
		t.Fatalf("active turn subscriptions = %d, want 0", got)
	}
	connection.watchMu.Lock()
	watches := len(connection.watches)
	connection.watchMu.Unlock()
	if watches != 0 {
		t.Fatalf("watch reservations = %d, want 0", watches)
	}
	connection.shutdown(nil)
}

func TestSessionConnectionBoundsAsyncTurnSubmissionSlots(t *testing.T) {
	connection := &sessionConn{turnCapacity: 2}
	releaseFirst, err := connection.acquireTurnSubmission()
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond, err := connection.acquireTurnSubmission()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.acquireTurnSubmission(); !errors.Is(err, errSessionTurnCapacity) {
		t.Fatalf("overflow admission = %v", err)
	}
	releaseFirst()
	releaseAfterCompletion, err := connection.acquireTurnSubmission()
	if err != nil {
		t.Fatalf("readmission after release = %v", err)
	}
	releaseAfterCompletion()
	releaseSecond()
	connection.watchMu.Lock()
	connection.closed = true
	connection.watchMu.Unlock()
	if _, err := connection.acquireTurnSubmission(); !errors.Is(err, errSessionConnectionClosed) {
		t.Fatalf("post-close admission = %v", err)
	}
}
