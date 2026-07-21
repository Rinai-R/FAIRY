package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/RomiChan/websocket"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
)

func TestZeroBotDriverEventAndActionRoundTrip(t *testing.T) {
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" {
			serverErr <- fmt.Errorf("authorization = %q", r.Header.Get("Authorization"))
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		deadline := time.Now().Add(5 * time.Second)
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
		if err := conn.WriteJSON(map[string]any{"post_type": "meta_event", "meta_event_type": "lifecycle", "self_id": 10001}); err != nil {
			serverErr <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{"post_type": "message", "message_type": "group", "message_id": 30001, "group_id": 20001, "user_id": 40001, "self_id": 10001, "message": "hello"}); err != nil {
			serverErr <- err
			return
		}
		var request zero.APIRequest
		if err := conn.ReadJSON(&request); err != nil {
			serverErr <- err
			return
		}
		if request.Action != "send_group_msg" || request.Echo == 0 {
			serverErr <- fmt.Errorf("request = %#v", request)
			return
		}
		serverErr <- conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 50001}, "echo": request.Echo})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZeroBotDriverHelper$")
	command.Env = append(os.Environ(),
		"FAIRY_TEST_ZEROBOT_HELPER=1",
		"FAIRY_TEST_ZEROBOT_URL=ws"+strings.TrimPrefix(server.URL, "http"),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ZeroBot helper failed: %v\n%s", err, output)
	}
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("OneBot integration server did not finish")
	}
}

func TestZeroBotDriverHelper(t *testing.T) {
	if os.Getenv("FAIRY_TEST_ZEROBOT_HELPER") != "1" {
		return
	}
	client := driver.NewWebSocketClient(os.Getenv("FAIRY_TEST_ZEROBOT_URL"), "exact-token")
	client.Connect()
	client.Listen(func(_ []byte, caller zero.APICaller) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, err := caller.CallAPI(ctx, zero.APIRequest{
				Action: "send_group_msg",
				Params: zero.Params{"group_id": int64(20001), "message": "reply"},
			})
			if err != nil || response.Status != "ok" || response.RetCode != 0 {
				data, _ := json.Marshal(response)
				fmt.Fprintf(os.Stderr, "action failed: %v %s", err, data)
				os.Exit(2)
			}
			os.Exit(0)
		}()
	})
}
