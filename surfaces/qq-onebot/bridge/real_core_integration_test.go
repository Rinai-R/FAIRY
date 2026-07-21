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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/coreclient"
	"github.com/RomiChan/websocket"
)

func TestZeroBotBridgeRealCoreProviderUsage(t *testing.T) {
	endpoint := os.Getenv("FAIRY_TEST_REAL_CORE_ENDPOINT")
	token := os.Getenv("FAIRY_TEST_REAL_CORE_TOKEN")
	if endpoint == "" {
		t.Skip("real Core endpoint is required")
	}
	client, err := coreclient.New(coreclient.Options{Endpoint: endpoint, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	before, err := client.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var beforeTurns []providerUsageTurn
	if err := json.Unmarshal(before.Usage.Turns, &beforeTurns); err != nil {
		t.Fatal(err)
	}
	previousTurnIDs := make(map[string]struct{}, len(beforeTurns))
	for _, turn := range beforeTurns {
		previousTurnIDs[turn.TurnID] = struct{}{}
	}
	session, err := client.OpenSession(ctx, coreclient.OpenSessionRequest{Surface: "im_group", SurfaceKey: "onebot-group:20001"})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := client.OpenEvents(ctx, session.ConversationID, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observedEvents := make(chan string, 16)
	go func() {
		defer close(observedEvents)
		for {
			raw, err := observer.Next()
			if err != nil {
				return
			}
			if raw.Event != "harness" {
				continue
			}
			harness, err := coreclient.DecodeHarnessEvent(raw)
			if err != nil {
				observedEvents <- "decode_error"
				return
			}
			var payload struct {
				Type string `json:"type"`
				Kind string `json:"kind"`
			}
			if json.Unmarshal(harness.Payload, &payload) != nil {
				observedEvents <- "payload_error"
				return
			}
			value := payload.Type
			if payload.Kind != "" {
				value += ":" + payload.Kind
			}
			observedEvents <- value
		}
	}()

	var actions atomic.Int32
	actionReceived := make(chan struct{}, 1)
	oneBotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_ = conn.SetWriteDeadline(time.Now().Add(90 * time.Second))
		_ = conn.WriteJSON(map[string]any{"post_type": "meta_event", "meta_event_type": "lifecycle", "self_id": 10001})
		_ = conn.WriteJSON(map[string]any{
			"post_type": "message", "message_type": "group", "message_id": 39001,
			"group_id": 20001, "user_id": 49001, "self_id": 10001,
			"message": "[CQ:at,qq=10001] 请用一句简短的话确认收到测试消息", "sender": map[string]any{"card": "测试成员"},
		})
		for {
			var action map[string]any
			if err := conn.ReadJSON(&action); err != nil {
				return
			}
			if action["action"] != "send_group_msg" {
				continue
			}
			actions.Add(1)
			select {
			case actionReceived <- struct{}{}:
			default:
			}
			_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 59001}, "echo": action["echo"]})
		}
	}))
	defer oneBotServer.Close()

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZeroBotBridgeServeHelper$")
	bridgeToken := token
	if bridgeToken == "" {
		bridgeToken = "bridge-e2e-token"
	}
	command.Env = append(os.Environ(),
		"FAIRY_TEST_BRIDGE_HELPER=1",
		"FAIRY_TEST_CORE_URL="+endpoint,
		"FAIRY_TEST_CORE_TOKEN="+bridgeToken,
		"FAIRY_TEST_ONEBOT_URL=ws"+strings.TrimPrefix(oneBotServer.URL, "http"),
		"FAIRY_TEST_BRIDGE_REPORT_ERRORS=1",
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Signal(os.Interrupt)
		_ = command.Wait()
	}()

	var usage providerUsageEvidence
	actionSeen := false
	observed := make([]string, 0, 8)
	for {
		select {
		case <-actionReceived:
			actionSeen = true
		case event, ok := <-observedEvents:
			if ok {
				observed = append(observed, event)
			}
		default:
		}
		after, err := client.Metrics(ctx)
		if err == nil && after.Usage.TurnCount > before.Usage.TurnCount {
			if err := json.Unmarshal(after.Usage.Turns, &usage.Turns); err != nil {
				t.Fatal(err)
			}
			status, found := usage.newTurnStatus(previousTurnIDs)
			if found && status != "completed" {
				t.Fatalf("real provider turn reached terminal status %q before delivery", status)
			}
			if actionSeen && usage.hasCompletedNonzeroTurn(previousTurnIDs) {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("real provider bridge timeout: %v (actions=%d observed=%v child=%q)", ctx.Err(), actions.Load(), observed, output.String())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if actions.Load() == 0 {
		t.Fatal("ZeroBot did not send a group action")
	}
	fmt.Fprintf(t.Output(), "real provider usage: input=%d output=%d actions=%d\n", usage.InputTokens, usage.OutputTokens, actions.Load())
}

type providerUsageEvidence struct {
	Turns        []providerUsageTurn
	InputTokens  uint64
	OutputTokens uint64
}

type providerUsageTurn struct {
	TurnID string `json:"turnId"`
	Status string `json:"status"`
	Lanes  []struct {
		InputTokens  uint64 `json:"inputTokens"`
		OutputTokens uint64 `json:"outputTokens"`
	} `json:"lanes"`
}

func (e *providerUsageEvidence) hasCompletedNonzeroTurn(previous map[string]struct{}) bool {
	for _, turn := range e.Turns {
		if _, existed := previous[turn.TurnID]; existed {
			continue
		}
		if turn.Status != "completed" {
			continue
		}
		var input, output uint64
		for _, lane := range turn.Lanes {
			input += lane.InputTokens
			output += lane.OutputTokens
		}
		if input > 0 && output > 0 {
			e.InputTokens = input
			e.OutputTokens = output
			return true
		}
	}
	return false
}

func (e *providerUsageEvidence) newTurnStatus(previous map[string]struct{}) (string, bool) {
	for _, turn := range e.Turns {
		if _, existed := previous[turn.TurnID]; !existed {
			return turn.Status, true
		}
	}
	return "", false
}
