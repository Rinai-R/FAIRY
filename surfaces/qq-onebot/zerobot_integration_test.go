package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestZeroBotWebhookCoreRoundTrip(t *testing.T) {
	silentObserved := make(chan struct{})
	waitObserved := make(chan struct{})
	actionReceived := make(chan struct{})
	errorsCh := make(chan error, 8)
	var openCalls atomic.Int32
	var observeCalls atomic.Int32
	var actionCalls atomic.Int32

	coreServer := newCoreWSTestServer(silentObserved, waitObserved, &openCalls, &observeCalls, errorsCh)
	defer coreServer.Close()
	actionServer := newOneBotActionServer(actionReceived, &actionCalls, errorsCh)
	defer actionServer.Close()
	webhookEndpoint := availableLoopbackEndpoint(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZeroBotWebhookServeHelper$")
	command.Env = append(os.Environ(),
		"FAIRY_ZEROBOT_TEST_HELPER=1",
		"FAIRY_TEST_CORE_URL="+coreServer.URL,
		"FAIRY_TEST_WEBHOOK_URL="+webhookEndpoint,
		"FAIRY_TEST_ONEBOT_API_URL="+actionServer.URL,
	)
	var output lockedBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	invalidEvent := groupEvent(20001, 30000, "invalid")
	response := postWebhookWhenReady(t, ctx, webhookEndpoint, invalidEvent, "sha1=invalid")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid signature status = %d, want 403", response.StatusCode)
	}
	response.Body.Close()
	if openCalls.Load() != 0 {
		t.Fatalf("invalid signature opened session: opens=%d", openCalls.Load())
	}

	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(99999, 30001, "不应触发"))
	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(20001, 30002, "保持安静"))
	select {
	case <-silentObserved:
	case err := <-errorsCh:
		t.Fatalf("silent observe failed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatalf("silent observe timeout: %v\n%s", ctx.Err(), output.String())
	}
	time.Sleep(100 * time.Millisecond)
	if actionCalls.Load() != 0 {
		t.Fatalf("silent window created action=%d", actionCalls.Load())
	}
	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(20001, 30003, "你好"))
	select {
	case <-waitObserved:
	case err := <-errorsCh:
		t.Fatalf("wait observe failed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatalf("wait observe timeout: %v\n%s", ctx.Err(), output.String())
	}
	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(20001, 30004, "最近如何"))
	select {
	case <-actionReceived:
	case err := <-errorsCh:
		t.Fatalf("round trip failed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatalf("round trip timeout: %v\n%s", ctx.Err(), output.String())
	}
	if openCalls.Load() != 1 || observeCalls.Load() != 3 || actionCalls.Load() != 1 {
		t.Fatalf("calls open=%d observe=%d action=%d", openCalls.Load(), observeCalls.Load(), actionCalls.Load())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output.String())
	}
}

func newCoreWSTestServer(silentObserved, waitObserved chan struct{}, openCalls, observeCalls *atomic.Int32, errorsCh chan<- error) *httptest.Server {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var mu sync.Mutex
	var watchConn *websocket.Conn
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer core-token" {
			errorsCh <- fmt.Errorf("Core authorization = %q", r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/session/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errorsCh <- err
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{"type": "ready"}); err != nil {
			return
		}
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			requestID, _ := frame["requestId"].(string)
			switch frame["type"] {
			case "session.open":
				openCalls.Add(1)
				endpoint, _ := frame["endpoint"].(string)
				endpointKey, _ := frame["endpointKey"].(string)
				if endpoint != "im" || endpointKey != "onebot-group:20001" {
					errorsCh <- fmt.Errorf("session open = %#v", frame)
					return
				}
				_ = conn.WriteJSON(map[string]any{
					"type": "session.opened", "requestId": requestID,
					"conversationId": "c1", "characterId": "character-1", "messageCount": 0, "endpoint": "im",
				})
			case "session.watch":
				mu.Lock()
				watchConn = conn
				mu.Unlock()
				_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID, "conversationId": "c1"})
			case "ambient.observe":
				call := observeCalls.Add(1)
				message, _ := frame["message"].(map[string]any)
				text, _ := message["text"].(string)
				_ = conn.WriteJSON(map[string]any{"type": "ack", "requestId": requestID, "conversationId": "c1"})
				switch call {
				case 1:
					if text != "保持安静" {
						errorsCh <- fmt.Errorf("silent observe = %#v", frame)
						return
					}
					close(silentObserved)
				case 2:
					if text != "你好" {
						errorsCh <- fmt.Errorf("wait observe = %#v", frame)
						return
					}
					close(waitObserved)
				case 3:
					if text != "最近如何" {
						errorsCh <- fmt.Errorf("reply observe = %#v", frame)
						return
					}
					mu.Lock()
					target := watchConn
					mu.Unlock()
					if target == nil {
						errorsCh <- fmt.Errorf("no watch connection for harness")
						return
					}
					_ = target.WriteJSON(map[string]any{
						"type": "harness", "conversationId": "c1",
						"event": map[string]any{
							"conversationId": "c1", "turnId": "t1", "sequence": 1, "state": "responding",
							"payload": json.RawMessage(`{"type":"beat.ready","beatId":"b1","kind":"final","displayText":"真实回复"}`),
						},
					})
				default:
					errorsCh <- fmt.Errorf("unexpected observe call %d", call)
				}
			default:
				errorsCh <- fmt.Errorf("unexpected frame %#v", frame)
			}
		}
	}))
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func newOneBotActionServer(actionReceived chan struct{}, actionCalls *atomic.Int32, errorsCh chan<- error) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer onebot-token" {
			errorsCh <- fmt.Errorf("OneBot authorization = %q", r.Header.Get("Authorization"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/get_login_info":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":10001,"nickname":"bot"}}`)
		case "/send_group_msg":
			actionCalls.Add(1)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				errorsCh <- err
				return
			}
			if !bytes.Contains(body, []byte(`"group_id":20001`)) || !bytes.Contains(body, []byte("真实回复")) {
				errorsCh <- fmt.Errorf("send_group_msg body = %s", body)
				return
			}
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":50001}}`)
			close(actionReceived)
		default:
			http.NotFound(w, r)
		}
	}))
}

func availableLoopbackEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + address
}

func groupEvent(groupID, messageID int64, message string) []byte {
	body, _ := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group", "sub_type": "normal",
		"message_id": messageID, "message": message,
		"group_id": groupID, "user_id": 40001, "self_id": 10001, "time": time.Now().Unix(),
		"sender": map[string]any{"user_id": 40001, "nickname": "测试成员", "card": "测试成员", "role": "member"},
	})
	return body
}

func postWebhookWhenReady(t *testing.T, ctx context.Context, endpoint string, body []byte, signature string) *http.Response {
	t.Helper()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Signature", signature)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			return response
		}
		select {
		case <-ctx.Done():
			t.Fatalf("webhook did not start: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func postSignedWebhook(t *testing.T, ctx context.Context, endpoint string, body []byte) {
	t.Helper()
	mac := hmac.New(sha1.New, []byte("onebot-token"))
	mac.Write(body)
	signature := "sha1=" + hex.EncodeToString(mac.Sum(nil))
	response := postWebhookWhenReady(t, ctx, endpoint, body, signature)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("signed webhook status = %d", response.StatusCode)
	}
}

func TestZeroBotWebhookServeHelper(t *testing.T) {
	if os.Getenv("FAIRY_ZEROBOT_TEST_HELPER") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := runBot(ctx, Config{
		CoreEndpoint: os.Getenv("FAIRY_TEST_CORE_URL"), CoreToken: "core-token",
		OneBotWebhookEndpoint: os.Getenv("FAIRY_TEST_WEBHOOK_URL"),
		OneBotAPIEndpoint:     os.Getenv("FAIRY_TEST_ONEBOT_API_URL"),
		OneBotToken:           "onebot-token", GroupAllowlist: []string{"20001"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
