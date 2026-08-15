package qqonebot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fairy/plugin"
	"fairy/plugin/qqonebot"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
)

func TestManifestDeclaresOneBotCapabilities(t *testing.T) {
	manifest := qqonebot.Manifest()
	if err := plugin.CheckCompatibility(plugin.ABIVersion, manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ID != qqonebot.PluginID {
		t.Fatalf("manifest = %#v", manifest)
	}
	want := []string{"http.request", "http.ingress", "event.emit", "action.complete"}
	if strings.Join(manifest.Capabilities, ",") != strings.Join(want, ",") {
		t.Fatalf("capabilities = %#v", manifest.Capabilities)
	}
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	file, err := plugin.ParseManifest(bytes.NewReader(raw))
	if err != nil || file.ID != manifest.ID {
		t.Fatalf("manifest.json = (%#v, %v)", file, err)
	}
}

func TestParseEventPreservesMentionsAndStringMessageIDs(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group", "time": 1,
		"message_id": 7, "user_id": 10001, "group_id": 20001,
		"raw_message": "是吗[CQ:at,qq=718249954,name=秋] 快看新同学[CQ:at,qq=718249954,name=秋]",
		"sender":      map[string]any{"card": "白色季节"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := qqonebot.ParseEvent(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.MessageID != "7" || event.Text != "是吗 @秋 快看新同学 @秋" || event.DirectedToBot || event.EndpointKey != "onebot-group:20001" {
		t.Fatalf("event = %#v", event)
	}
	if len(event.Mentions) != 1 || event.Mentions[0] != (qqonebot.Mention{UserID: "718249954", DisplayName: "秋"}) {
		t.Fatalf("mentions = %#v", event.Mentions)
	}

	raw, err = json.Marshal(map[string]any{
		"post_type": "message", "message_type": "private", "time": 1,
		"message_id": "guild-message-7", "user_id": 10001,
		"message": []map[string]any{{"type": "text", "data": map[string]any{"text": "你好"}}},
		"sender":  map[string]any{"nickname": "群友"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err = qqonebot.ParseEvent(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.MessageID != "guild-message-7" || event.Kind != "private" || event.EndpointKey != "onebot-private:10001" {
		t.Fatalf("private = %#v", event)
	}
}

func TestParseEventDirectedToBotAndInvalidMention(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group", "time": 1, "to_me": true,
		"message_id": 8, "user_id": 10001, "group_id": 20001, "self_id": 527338184,
		"raw_message": "[CQ:at,qq=527338184,name=亚托莉] 你好",
		"sender":      map[string]any{"nickname": "群友"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := qqonebot.ParseEvent(raw, "527338184")
	if err != nil {
		t.Fatal(err)
	}
	if !event.DirectedToBot || event.Text != "@亚托莉 你好" || len(event.Mentions) != 1 || event.Mentions[0].DisplayName != "亚托莉" {
		t.Fatalf("directed = %#v", event)
	}

	raw, err = json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group", "time": 1,
		"message_id": 9, "user_id": 10001, "group_id": 20001,
		"raw_message": "你好[CQ:at,qq=invalid,name=坏数据]",
		"sender":      map[string]any{"nickname": "群友"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := qqonebot.ParseEvent(raw, ""); err == nil {
		t.Fatal("invalid mention accepted")
	}

	raw, err = json.Marshal(map[string]any{
		"post_type": "message", "message_type": "group", "time": 1,
		"message_id": 10, "user_id": 10001, "group_id": 20001,
		"raw_message": "看这里[CQ:at,qq=718249954]",
		"sender":      map[string]any{"nickname": "群友"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err = qqonebot.ParseEvent(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if event.Text != "看这里 @718249954" || len(event.Mentions) != 1 || event.Mentions[0].DisplayName != "718249954" {
		t.Fatalf("fallback mention = %#v", event)
	}
}

func TestReplyDistanceTrackerUsesBoundedPositionAndTimePolicy(t *testing.T) {
	base := time.Unix(100, 0)
	tracker := &qqonebot.ReplyDistanceTracker{}
	tracker.Observe("target", base)
	if tracker.ShouldQuote("target", base.Add(15*time.Minute-time.Second), 2) {
		t.Fatal("near target was quoted before threshold")
	}
	tracker.Observe("later-1", base.Add(time.Second))
	if tracker.ShouldQuote("target", base.Add(2*time.Second), 2) {
		t.Fatal("one later message exceeded the message gap")
	}
	tracker.Observe("later-2", base.Add(2*time.Second))
	if !tracker.ShouldQuote("target", base.Add(3*time.Second), 2) {
		t.Fatal("two later messages did not trigger quote")
	}

	timed := &qqonebot.ReplyDistanceTracker{}
	timed.Observe("slow-target", base)
	if !timed.ShouldQuote("slow-target", base.Add(15*time.Minute), 5) {
		t.Fatal("elapsed threshold did not trigger quote")
	}

	maximumGap := &qqonebot.ReplyDistanceTracker{}
	maximumGap.Observe("maximum-target", base)
	for index := uint64(1); index < 5; index++ {
		maximumGap.Observe(fmt.Sprintf("maximum-later-%d", index), base.Add(time.Duration(index)*time.Second))
	}
	if maximumGap.ShouldQuote("maximum-target", base.Add(5*time.Second), 5) {
		t.Fatal("maximum gap triggered before five later messages")
	}
	maximumGap.Observe("maximum-later-5", base.Add(5*time.Second))
	if !maximumGap.ShouldQuote("maximum-target", base.Add(6*time.Second), 5) {
		t.Fatal("maximum gap did not trigger after five later messages")
	}
}

func TestReplyDistanceTrackerBoundsAndDeduplicatesMessageIDs(t *testing.T) {
	base := time.Unix(100, 0)
	tracker := &qqonebot.ReplyDistanceTracker{}
	tracker.Observe("duplicate", base)
	tracker.Observe("duplicate", base.Add(time.Second))
	for index := 0; index < 64; index++ {
		tracker.Observe(fmt.Sprintf("message-%d", index), base.Add(time.Duration(index+2)*time.Second))
	}
	if tracker.ShouldQuote("duplicate", base.Add(time.Hour), 2) {
		t.Fatal("evicted target remained available")
	}
}

func TestTurnReplyClaimsConsumeOnceAndReleaseAtTerminal(t *testing.T) {
	samples := []uint64{2, 5, 3}
	sampleIndex := 0
	claims := qqonebot.NewTurnReplyClaimsWithSampler(func() uint64 {
		value := samples[sampleIndex]
		sampleIndex++
		return value
	})
	gap, claimed := claims.Claim("turn-1")
	if !claimed || gap != 2 {
		t.Fatalf("first turn gap = %d, claimed=%v", gap, claimed)
	}
	if _, claimed = claims.Claim("turn-1"); claimed || sampleIndex != 1 {
		t.Fatal("turn anchor was not consumed exactly once")
	}
	claims.Release("turn-1")
	if !qqonebot.TerminalTurnState("completed") || qqonebot.TerminalTurnState("planning") {
		t.Fatal("terminal states")
	}
}

func TestAllowlistNormalizesAndEmptyRejectsAllGroups(t *testing.T) {
	got, err := qqonebot.NormalizeAllowlist([]string{" 00123 ", "456", "123", "000000000000000000000000456"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[123 456]" {
		t.Fatalf("allowlist = %#v", got)
	}
	allowed, err := qqonebot.GroupAllowed(nil, "123")
	if err != nil || allowed {
		t.Fatalf("empty allowlist allowed = (%v, %v)", allowed, err)
	}
}

func TestSendEmptyMessageIDIsFailedNotQueuedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_group_msg" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0,"data":{"message_id":0}}`)
	}))
	t.Cleanup(server.Close)
	host := newQQHost(t, httpHostCall(http.DefaultClient))
	out, err := host.Invoke(t.Context(), sendEnvelope(t, map[string]any{
		"op": "send", "apiBaseURL": server.URL, "groupId": "20001", "text": "真实回复",
	}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "result" {
		t.Fatalf("send = (%#v, %v)", parsed, err)
	}
	var receipt qqonebot.Receipt
	if err := json.Unmarshal(parsed.Payload, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "failed" || receipt.ExternalMessageID != "" {
		t.Fatalf("empty message id queued as success: %#v", receipt)
	}
}

func TestPluginParsesGroupEventAndSendsReceiptThroughHostHTTP(t *testing.T) {
	var captured struct {
		path   string
		auth   string
		body   string
		action map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		captured.body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0,"data":{"message_id":50001}}`)
	}))
	t.Cleanup(server.Close)

	host := newQQHost(t, func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		if capability == "action.complete" {
			if err := json.Unmarshal(payload, &captured.action); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"ok": true})
		}
		return httpHostCall(http.DefaultClient)(ctx, capability, payload)
	})

	parseRaw, err := json.Marshal(map[string]any{
		"op": "parse", "groupAllowlist": []string{"20001"},
		"raw": map[string]any{
			"post_type": "message", "message_type": "group", "time": 1,
			"message_id": 11, "user_id": 40001, "group_id": 20001,
			"message": "你好", "sender": map[string]any{"nickname": "测试成员"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := host.Invoke(t.Context(), handleEnvelope(t, "trace-qq", parseRaw))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "result" {
		t.Fatalf("parse = (%#v, %v)", parsed, err)
	}
	var accepted struct {
		Accepted bool           `json:"accepted"`
		Event    qqonebot.Event `json:"event"`
	}
	if err := json.Unmarshal(parsed.Payload, &accepted); err != nil || !accepted.Accepted || accepted.Event.MessageID != "11" {
		t.Fatalf("accepted = %#v err=%v", accepted, err)
	}

	sendOut, err := host.Invoke(t.Context(), sendEnvelope(t, map[string]any{
		"op": "send", "apiBaseURL": server.URL, "groupId": "20001", "text": "真实回复", "credential": "onebot",
	}))
	if err != nil {
		t.Fatal(err)
	}
	receiptEnv, err := sdk.Decode(sendOut)
	if err != nil || receiptEnv.Kind != "result" {
		t.Fatalf("send = (%#v, %v)", receiptEnv, err)
	}
	var receipt qqonebot.Receipt
	if err := json.Unmarshal(receiptEnv.Payload, &receipt); err != nil || receipt.Status != "succeeded" || receipt.ExternalMessageID != "50001" {
		t.Fatalf("receipt = %#v err=%v", receipt, err)
	}
	if captured.path != "/send_group_msg" || !strings.Contains(captured.body, `"group_id":20001`) || !strings.Contains(captured.body, "真实回复") {
		t.Fatalf("http capture = %#v", captured)
	}
	if captured.action["status"] != "succeeded" || captured.action["externalMessageId"] != "50001" || captured.action["turnId"] != "turn-1" {
		t.Fatalf("action = %#v", captured.action)
	}
}

func TestPluginRejectsGroupOutsideAllowlist(t *testing.T) {
	host := newQQHost(t, httpHostCall(http.DefaultClient))
	payload, err := json.Marshal(map[string]any{
		"op": "parse", "groupAllowlist": []string{"20001"},
		"raw": map[string]any{
			"post_type": "message", "message_type": "group", "time": 1,
			"message_id": 12, "user_id": 40001, "group_id": 99999,
			"message": "你好", "sender": map[string]any{"nickname": "测试成员"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := host.Invoke(t.Context(), handleEnvelope(t, "trace-deny", payload))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "result" {
		t.Fatalf("parse = (%#v, %v)", parsed, err)
	}
	var body struct {
		Accepted bool   `json:"accepted"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(parsed.Payload, &body); err != nil || body.Accepted || body.Reason != "not_whitelisted" {
		t.Fatalf("denied group = %#v err=%v", body, err)
	}
}

func TestPluginDoesNotImportHostNetworkOrCore(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{`"net/http"`, "fairy/app", "fairy/agent", "fairy/transport/session", "fairy/runtime/wasm", "github.com/wdvxdr1123/ZeroBot"} {
			if bytes.Contains(src, []byte(forbidden)) {
				t.Fatalf("%s imports %s", entry.Name(), forbidden)
			}
		}
	}
}

func newQQHost(t *testing.T, call testhost.HostCall) *testhost.Host {
	t.Helper()
	var host *testhost.Host
	host, err := testhost.New(qqonebot.NewHandler(func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return host.Call(ctx, capability, payload)
	}), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request", "http.ingress", "event.emit", "action.complete"},
		HostCall:      call,
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func handleEnvelope(t *testing.T, traceID string, payload json.RawMessage) []byte {
	t.Helper()
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "qq-1", TraceID: traceID, TurnID: "turn-1"},
		Payload:     payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sendEnvelope(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return handleEnvelope(t, "trace-qq", raw)
}

func httpHostCall(client *http.Client) testhost.HostCall {
	return func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		if capability == "action.complete" {
			return json.Marshal(map[string]any{"ok": true})
		}
		if capability != "http.request" {
			return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: capability + ": not granted"}
		}
		var req struct {
			Method     string `json:"method"`
			URL        string `json:"url"`
			Body       string `json:"body"`
			Credential string `json:"credential"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: "http.request: payload is invalid"}
		}
		httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, strings.NewReader(req.Body))
		if err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request: " + err.Error()}
		}
		if req.Credential != "" {
			httpReq.Header.Set("Authorization", "Bearer testhost-does-not-inject-secrets")
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request: " + err.Error()}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request: " + err.Error()}
		}
		inner, err := json.Marshal(map[string]any{
			"status":      resp.StatusCode,
			"contentType": resp.Header.Get("Content-Type"),
			"body":        string(body),
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			OK   bool            `json:"ok"`
			Body json.RawMessage `json:"body"`
		}{OK: true, Body: inner})
	}
}
