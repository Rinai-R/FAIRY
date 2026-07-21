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
)

func TestZeroBotWebhookCoreRoundTrip(t *testing.T) {
	turnStarted := make(chan struct{})
	silentDecided := make(chan struct{})
	waitDecided := make(chan struct{})
	actionReceived := make(chan struct{})
	errorsCh := make(chan error, 8)
	var sessionCalls atomic.Int32
	var participationCalls atomic.Int32
	var turnCalls atomic.Int32
	var actionCalls atomic.Int32

	coreServer := newCoreTestServer(turnStarted, silentDecided, waitDecided, &sessionCalls, &participationCalls, &turnCalls, errorsCh)
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
	if sessionCalls.Load() != 0 {
		t.Fatalf("invalid signature reached Core: sessions=%d", sessionCalls.Load())
	}

	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(99999, 30001, "不应触发"))
	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(20001, 30002, "保持安静"))
	select {
	case <-silentDecided:
	case err := <-errorsCh:
		t.Fatalf("silent participation failed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatalf("silent participation timeout: %v\n%s", ctx.Err(), output.String())
	}
	time.Sleep(100 * time.Millisecond)
	if turnCalls.Load() != 0 || actionCalls.Load() != 0 {
		t.Fatalf("silent window created turn=%d action=%d", turnCalls.Load(), actionCalls.Load())
	}
	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(20001, 30003, "你好"))
	select {
	case <-waitDecided:
	case err := <-errorsCh:
		t.Fatalf("wait participation failed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatalf("wait participation timeout: %v\n%s", ctx.Err(), output.String())
	}
	postSignedWebhook(t, ctx, webhookEndpoint, groupEvent(20001, 30004, "最近如何"))
	select {
	case <-actionReceived:
	case err := <-errorsCh:
		t.Fatalf("round trip failed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatalf("round trip timeout: %v\n%s", ctx.Err(), output.String())
	}
	if sessionCalls.Load() != 3 || participationCalls.Load() != 3 || turnCalls.Load() != 1 || actionCalls.Load() != 1 {
		t.Fatalf("calls session=%d participation=%d turn=%d action=%d", sessionCalls.Load(), participationCalls.Load(), turnCalls.Load(), actionCalls.Load())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output.String())
	}
}

func newCoreTestServer(turnStarted, silentDecided, waitDecided chan struct{}, sessionCalls, participationCalls, turnCalls *atomic.Int32, errorsCh chan<- error) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer core-token" {
			errorsCh <- fmt.Errorf("Core authorization = %q", r.Header.Get("Authorization"))
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			w.Header().Set("Content-Type", "application/json")
			sessionCalls.Add(1)
			var request coreSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				errorsCh <- err
				return
			}
			if request.Surface != "im_group" || request.SurfaceKey != "onebot-group:20001" {
				errorsCh <- fmt.Errorf("session request = %#v", request)
				return
			}
			fmt.Fprint(w, `{"conversationId":"c1","characterId":"character-1","messageCount":0,"surface":"im_group"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/c1/group-participation":
			w.Header().Set("Content-Type", "application/json")
			call := participationCalls.Add(1)
			var request struct {
				EvaluationReason string `json:"evaluationReason"`
				Messages         []struct {
					MessageID     string `json:"messageId"`
					SenderID      string `json:"senderId"`
					SenderName    string `json:"senderName"`
					Text          string `json:"text"`
					DirectedToBot bool   `json:"directedToBot"`
					IsNew         bool   `json:"isNew"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				errorsCh <- err
				return
			}
			if request.EvaluationReason != "message" {
				errorsCh <- fmt.Errorf("evaluation reason = %q", request.EvaluationReason)
				return
			}
			switch call {
			case 1:
				if len(request.Messages) != 1 || request.Messages[0].Text != "保持安静" || !request.Messages[0].IsNew {
					errorsCh <- fmt.Errorf("silent participation request = %#v", request)
					return
				}
				if request.Messages[0].DirectedToBot {
					errorsCh <- fmt.Errorf("ordinary group message marked directed: %#v", request.Messages[0])
					return
				}
				close(silentDecided)
				fmt.Fprint(w, `{"action":"silent"}`)
				return
			case 2:
				if len(request.Messages) != 2 || request.Messages[0].Text != "保持安静" || request.Messages[1].Text != "你好" || request.Messages[0].IsNew || !request.Messages[1].IsNew {
					errorsCh <- fmt.Errorf("wait participation request = %#v", request)
					return
				}
				fmt.Fprint(w, `{"action":"wait","waitSeconds":300}`)
				close(waitDecided)
				return
			case 3:
				if len(request.Messages) != 3 || request.Messages[0].Text != "保持安静" || request.Messages[1].Text != "你好" || request.Messages[2].Text != "最近如何" || request.Messages[0].IsNew || request.Messages[1].IsNew || !request.Messages[2].IsNew || request.Messages[1].SenderID != "40001" || request.Messages[1].SenderName != "测试成员" {
					errorsCh <- fmt.Errorf("fresh participation request = %#v", request)
					return
				}
				fmt.Fprint(w, `{"action":"reply","targetMessageId":"30004"}`)
				return
			default:
				errorsCh <- fmt.Errorf("unexpected participation call %d", call)
				return
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/c1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: ready\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-turnStarted:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "event: harness\ndata: {\"conversationId\":\"c1\",\"turnId\":\"t1\",\"sequence\":1,\"state\":\"responding\",\"payload\":{\"type\":\"beat.ready\",\"beatId\":\"b1\",\"kind\":\"final\",\"displayText\":\"真实回复\"}}\n\n")
			fmt.Fprint(w, "event: harness\ndata: {\"conversationId\":\"c1\",\"turnId\":\"t1\",\"sequence\":2,\"state\":\"completed\",\"payload\":{\"type\":\"completed\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/c1/turns":
			w.Header().Set("Content-Type", "application/json")
			turnCalls.Add(1)
			var request coreTurnRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				errorsCh <- err
				return
			}
			if request.Input != "[测试成员/40001] 保持安静\n[测试成员/40001] 你好\n[reply-target][测试成员/40001] 最近如何" || request.Surface != "im_group" {
				errorsCh <- fmt.Errorf("turn request = %#v", request)
				return
			}
			close(turnStarted)
			fmt.Fprint(w, `{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"真实回复"},"surface":"im_group"}`)
		default:
			http.NotFound(w, r)
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

type coreSessionRequest struct {
	Surface    string `json:"surface"`
	SurfaceKey string `json:"surfaceKey"`
}

type coreTurnRequest struct {
	Input   string `json:"input"`
	Surface string `json:"surface"`
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
