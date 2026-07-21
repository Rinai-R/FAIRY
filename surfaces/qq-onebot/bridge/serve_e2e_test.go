package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomiChan/websocket"
)

func TestZeroBotBridgeEndToEnd(t *testing.T) {
	turnStarted := make(chan struct{})
	actionReceived := make(chan struct{})
	var turnOnce sync.Once
	var actionOnce sync.Once
	coreErr := make(chan error, 4)
	oneBotErr := make(chan error, 4)

	coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer core-token" {
			coreErr <- fmt.Errorf("Core authorization = %q", r.Header.Get("Authorization"))
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			var request struct {
				Surface    string `json:"surface"`
				SurfaceKey string `json:"surfaceKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				coreErr <- err
				return
			}
			if request.Surface != "im_group" || request.SurfaceKey != "onebot-group:20001" {
				coreErr <- fmt.Errorf("session request = %#v", request)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"conversationId":"c1","characterId":"character-1","messageCount":0,"surface":"im_group"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/c1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: ready\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-turnStarted:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "event: harness\ndata: {\"conversationId\":\"c1\",\"turnId\":\"t1\",\"sequence\":1,\"state\":\"responding\",\"payload\":{\"type\":\"beat.ready\",\"beatId\":\"b1\",\"kind\":\"final\",\"displayText\":\"完整回复\",\"visualState\":\"idle\"}}\n\n")
			fmt.Fprint(w, "event: harness\ndata: {\"conversationId\":\"c1\",\"turnId\":\"t1\",\"sequence\":2,\"state\":\"completed\",\"payload\":{\"type\":\"completed\",\"text\":\"完整回复\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/c1/turns":
			var request struct {
				Input   string `json:"input"`
				Surface string `json:"surface"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				coreErr <- err
				return
			}
			if request.Surface != "im_group" || request.Input != "测试成员：你好" {
				coreErr <- fmt.Errorf("turn request = %#v", request)
				return
			}
			turnOnce.Do(func() { close(turnStarted) })
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"完整回复"},"surface":"im_group"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer coreServer.Close()

	oneBotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer onebot-token" {
			oneBotErr <- fmt.Errorf("OneBot authorization = %q", r.Header.Get("Authorization"))
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			oneBotErr <- err
			return
		}
		defer conn.Close()
		deadline := time.Now().Add(10 * time.Second)
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
		if err := conn.WriteJSON(map[string]any{"post_type": "meta_event", "meta_event_type": "lifecycle", "self_id": 10001}); err != nil {
			oneBotErr <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"post_type": "message", "message_type": "group", "message_id": 30001,
			"group_id": 20001, "user_id": 40001, "self_id": 10001,
			"message": "[CQ:at,qq=10001] 你好", "sender": map[string]any{"card": "测试成员"},
		}); err != nil {
			oneBotErr <- err
			return
		}
		var action map[string]any
		if err := conn.ReadJSON(&action); err != nil {
			oneBotErr <- err
			return
		}
		raw, _ := json.Marshal(action)
		if action["action"] != "send_group_msg" || !bytes.Contains(raw, []byte("完整回复")) {
			oneBotErr <- fmt.Errorf("action = %s", raw)
			return
		}
		actionOnce.Do(func() { close(actionReceived) })
		oneBotErr <- conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 50001}, "echo": action["echo"]})
	}))
	defer oneBotServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZeroBotBridgeServeHelper$")
	command.Env = append(os.Environ(),
		"FAIRY_TEST_BRIDGE_HELPER=1",
		"FAIRY_TEST_CORE_URL="+coreServer.URL,
		"FAIRY_TEST_CORE_TOKEN=core-token",
		"FAIRY_TEST_ONEBOT_URL=ws"+strings.TrimPrefix(oneBotServer.URL, "http"),
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-actionReceived:
	case <-ctx.Done():
		t.Fatalf("bridge action timeout: %v\n%s", ctx.Err(), output.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("bridge helper failed: %v\n%s", err, output.String())
	}
	select {
	case err := <-coreErr:
		t.Fatal(err)
	default:
	}
	select {
	case err := <-oneBotErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("OneBot response was not completed")
	}
}

func TestZeroBotBridgeServeHelper(t *testing.T) {
	if os.Getenv("FAIRY_TEST_BRIDGE_HELPER") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	config := Config{
		CoreEndpoint:   os.Getenv("FAIRY_TEST_CORE_URL"),
		CoreToken:      os.Getenv("FAIRY_TEST_CORE_TOKEN"),
		OneBotEndpoint: os.Getenv("FAIRY_TEST_ONEBOT_URL"),
		OneBotToken:    "onebot-token",
		SelfID:         "10001",
		GroupAllowlist: []string{"20001"},
	}
	var err error
	if os.Getenv("FAIRY_TEST_BRIDGE_REPORT_ERRORS") == "1" {
		err = serve(ctx, config, func(stage string, err error) {
			fmt.Fprintf(os.Stderr, "bridge stage %s failed: %v\n", stage, err)
		})
	} else {
		err = Serve(ctx, config)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
