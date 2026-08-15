package edge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/plugin/qqonebot"
	"fairy/runtime/wasm"
	"fairy/transport/session"
)

type fakeQQSession struct {
	mu         sync.Mutex
	opens      []session.OpenSessionRequest
	ambient    []session.AmbientObservation
	turns      []session.SubmitTurnRequest
	deliveries []session.ExpressionDeliveryResult
	watch      chan session.TurnEvent
	openErr    error
}

func (f *fakeQQSession) OpenSession(_ context.Context, request session.OpenSessionRequest) (session.OpenSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, request)
	if f.openErr != nil {
		return session.OpenSessionResponse{}, f.openErr
	}
	if f.watch == nil {
		f.watch = make(chan session.TurnEvent, 8)
	}
	return session.OpenSessionResponse{ConversationID: "c-" + request.EndpointKey}, nil
}

func (f *fakeQQSession) Watch(_ context.Context, conversationID string) (<-chan session.TurnEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watch == nil {
		f.watch = make(chan session.TurnEvent, 8)
	}
	return f.watch, nil
}

func (f *fakeQQSession) ObserveAmbient(_ context.Context, conversationID string, message session.AmbientObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	message.Text = conversationID + ":" + message.Text
	f.ambient = append(f.ambient, message)
	return nil
}

func (f *fakeQQSession) SubmitTurn(_ context.Context, conversationID string, request session.SubmitTurnRequest) (session.SubmitTurnResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request.Input = conversationID + ":" + request.Input
	f.turns = append(f.turns, request)
	return session.SubmitTurnResponse{}, nil
}

func (f *fakeQQSession) ReportExpressionDelivery(_ context.Context, result session.ExpressionDeliveryResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = append(f.deliveries, result)
	return nil
}

func (f *fakeQQSession) closeWatch() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watch == nil {
		return
	}
	close(f.watch)
	f.watch = nil
}

func TestQQBridgePollsGroupBatchAndKeepsPrivateDirectTurn(t *testing.T) {
	socket := &fakeQQSession{}
	bridge := mustQQBridge(t, socket, nil, qqonebot.InstanceConfig{GroupAllowlist: []string{"20001"}}, nil)
	if err := bridge.Ingest(t.Context(), groupEvent(t, 11, 20001, "第一条")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Ingest(t.Context(), groupEvent(t, 12, 20001, "第二条")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Ingest(t.Context(), privateEvent(t, 13, 40001, "私聊")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Ingest(t.Context(), groupEvent(t, 14, 99999, "未授权")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	socket.mu.Lock()
	defer socket.mu.Unlock()
	if len(socket.ambient) != 2 || socket.ambient[0].MessageID != "11" || socket.ambient[1].MessageID != "12" {
		t.Fatalf("ambient = %#v", socket.ambient)
	}
	if !strings.HasPrefix(socket.ambient[0].Text, "c-onebot-group:20001:") {
		t.Fatalf("group conversation = %q", socket.ambient[0].Text)
	}
	if len(socket.turns) != 1 || socket.turns[0].MessageID != "13" || !strings.Contains(socket.turns[0].Input, "私聊") {
		t.Fatalf("private turns = %#v", socket.turns)
	}
	for _, observation := range socket.ambient {
		if strings.Contains(observation.Text, "未授权") {
			t.Fatal("non-allowlisted group was observed")
		}
	}
}

func TestQQBridgeInvalidEventDoesNotBreakDesktopSession(t *testing.T) {
	socket := &fakeQQSession{}
	bridge := mustQQBridge(t, socket, nil, qqonebot.InstanceConfig{GroupAllowlist: []string{"20001"}}, nil)
	if err := bridge.Ingest(t.Context(), json.RawMessage(`{"post_type":"notice"}`)); err == nil {
		t.Fatal("invalid event accepted")
	}
	if err := bridge.Ingest(t.Context(), groupEvent(t, 21, 20001, "恢复")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	socket.mu.Lock()
	if len(socket.ambient) != 1 || socket.ambient[0].MessageID != "21" {
		socket.mu.Unlock()
		t.Fatalf("recovery ambient = %#v", socket.ambient)
	}
	socket.mu.Unlock()
	socket.closeWatch()
	if _, err := socket.OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desktop",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat, Principal: &session.PrincipalRef{Namespace: "local", Subject: "owner"}},
	}); err != nil {
		t.Fatalf("desktop session after plugin fault: %v", err)
	}
}

func TestQQBridgeDeliversTextReceiptAndQuoteThroughPluginSend(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0,"data":{"message_id":50001}}`)
	}))
	t.Cleanup(server.Close)
	socket := &fakeQQSession{watch: make(chan session.TurnEvent, 2)}
	httpCall := wasmHTTPCall(t, server.URL)
	bridge := mustQQBridge(t, socket, nil, qqonebot.InstanceConfig{GroupAllowlist: []string{"20001"}, APIBaseURL: server.URL}, httpCall)
	if err := bridge.Ingest(t.Context(), groupEvent(t, 31, 20001, "你好")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "beat.ready", "beatId": "b1", "kind": "final", "displayText": "真实回复",
		"replyTargetMessageId": "31",
		"part":                 map[string]any{"kind": "utterance", "text": "真实回复"},
	})
	socket.watch <- session.TurnEvent{ConversationID: "c-onebot-group:20001", TurnID: "turn-1", Payload: payload, State: "completed"}
	waitFor(t, func() bool {
		socket.mu.Lock()
		defer socket.mu.Unlock()
		return len(socket.deliveries) == 1
	})
	socket.mu.Lock()
	defer socket.mu.Unlock()
	got := socket.deliveries[0]
	if got.Status != session.ExpressionDeliverySucceeded || got.ExternalMessageID != "50001" || got.TurnID != "turn-1" {
		t.Fatalf("delivery = %#v", got)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "真实回复") {
		t.Fatalf("send bodies = %#v", bodies)
	}
}

func TestQQBridgeStickerReceiptUsesHostHTTP(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0,"data":{"message_id":50002}}`)
	}))
	t.Cleanup(server.Close)
	socket := &fakeQQSession{watch: make(chan session.TurnEvent, 2)}
	bridge := mustQQBridge(t, socket, func(context.Context, string) (session.StickerContent, error) {
		return session.StickerContent{MIMEType: "image/png", Bytes: []byte("PNG")}, nil
	}, qqonebot.InstanceConfig{GroupAllowlist: []string{"20001"}, APIBaseURL: server.URL}, wasmHTTPCall(t, server.URL))
	if err := bridge.Ingest(t.Context(), groupEvent(t, 41, 20001, "表情")); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "beat.ready", "beatId": "b2", "kind": "final",
		"part": map[string]any{"kind": "sticker", "sticker": map[string]any{"id": "sticker-1", "mimeType": "image/png"}},
	})
	socket.watch <- session.TurnEvent{ConversationID: "c-onebot-group:20001", TurnID: "turn-2", Payload: payload}
	waitFor(t, func() bool {
		socket.mu.Lock()
		defer socket.mu.Unlock()
		return len(socket.deliveries) == 1
	})
	if !strings.Contains(strings.Join(bodies, ""), "base64://") {
		t.Fatalf("sticker body = %#v", bodies)
	}
	socket.mu.Lock()
	defer socket.mu.Unlock()
	if socket.deliveries[0].Status != session.ExpressionDeliverySucceeded || socket.deliveries[0].ExternalMessageID != "50002" {
		t.Fatalf("sticker delivery = %#v", socket.deliveries[0])
	}
}

func TestManagementQQWithoutPluginReturnsEmptyAllowlist(t *testing.T) {
	management := &Management{runtime: &Runtime{}}
	settings, err := management.QQ()
	if err != nil || settings.Ready || len(settings.GroupAllowlist) != 0 {
		t.Fatalf("QQ() = (%#v, %v)", settings, err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "token") || strings.Contains(string(raw), "sk-live") {
		t.Fatalf("QQ settings leaked credential: %s", raw)
	}
	if _, err := management.SaveQQ(QQSettings{GroupAllowlist: []string{"1"}}); !errors.Is(err, ErrQQPluginNotInstalled) {
		t.Fatalf("SaveQQ() = %v", err)
	}
}

func mustQQBridge(t *testing.T, socket qqSession, stickers qqStickerReader, config qqonebot.InstanceConfig, httpCall func(context.Context, string, json.RawMessage) ([]byte, error)) *QQBridge {
	t.Helper()
	bridge, err := newQQBridge(socket, stickers, "qq-1", config, wasm.Grant{}, httpCall)
	if err != nil {
		t.Fatal(err)
	}
	if fake, ok := socket.(*fakeQQSession); ok {
		t.Cleanup(fake.closeWatch)
	}
	return bridge
}

func groupEvent(t *testing.T, messageID, groupID int64, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group", "time": 1,
		"message_id": messageID, "user_id": 40001, "group_id": groupID,
		"message": text, "sender": map[string]any{"nickname": "测试成员"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func privateEvent(t *testing.T, messageID, userID int64, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "private", "time": 1,
		"message_id": messageID, "user_id": userID,
		"message": text, "sender": map[string]any{"nickname": "好友"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func wasmHTTPCall(t *testing.T, baseURL string) func(context.Context, string, json.RawMessage) ([]byte, error) {
	t.Helper()
	grant, err := wasm.HTTPRequestGrantFromURLMethods(baseURL, 64<<10, http.MethodPost)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.SetCredential(qqCredentialHandle, "onebot-token"); err != nil {
		t.Fatal(err)
	}
	host, err := wasm.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	return func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return host.HTTPRequest(ctx, wasm.Grant{HTTPRequest: grant}, payload)
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestLoopbackBindRejectsNonLocalAddresses(t *testing.T) {
	if _, err := loopbackBind("0.0.0.0:8080"); !errors.Is(err, ErrQQIngressBindInvalid) {
		t.Fatalf("public bind = %v", err)
	}
	got, err := loopbackBind("127.0.0.1:5701")
	if err != nil || got != "127.0.0.1:5701" {
		t.Fatalf("loopback bind = (%q, %v)", got, err)
	}
}
