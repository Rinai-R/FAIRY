package wasm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fairy/plugin"
	"fairy/plugin/qqonebot"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
)

func TestQQOneBotPluginSendUsesHostHTTPAndCompletesAction(t *testing.T) {
	const secret = "onebot-token-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_group_msg" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"group_id":20001`) || !strings.Contains(string(body), "真实回复") {
			t.Fatalf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0,"data":{"message_id":50001}}`)
	}))
	t.Cleanup(server.Close)

	grant, err := HTTPRequestGrantFromURLMethods(server.URL, 64<<10, http.MethodPost)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.SetCredential("onebot", secret); err != nil {
		t.Fatal(err)
	}
	instance := loadProxy(t, Grant{HTTPRequest: grant, Action: true})
	host := newQQTestHost(t, instance)
	out, err := host.Invoke(t.Context(), qqSendEnvelope(t, server.URL, "20001", "真实回复"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "result" {
		t.Fatalf("result = (%#v, %v)", parsed, err)
	}
	var receipt qqonebot.Receipt
	if err := json.Unmarshal(parsed.Payload, &receipt); err != nil || receipt.Status != "succeeded" || receipt.ExternalMessageID != "50001" {
		t.Fatalf("receipt = %#v err=%v", receipt, err)
	}
	if !strings.Contains(string(instance.LastAction()), `"status":"succeeded"`) {
		t.Fatalf("last action = %s", instance.LastAction())
	}
	if strings.Contains(string(out), secret) || strings.Contains(string(instance.LastAction()), secret) {
		t.Fatal("credential leaked through plugin result or action")
	}
	assertNoSecret(t, string(out), string(instance.LastAction()))
}

func TestQQOneBotPluginDeniedHTTPDoesNotQueueSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("plugin reached network without grant: %s %s", r.Method, r.URL)
	}))
	t.Cleanup(server.Close)
	instance := loadProxy(t, Grant{})
	host := newQQTestHost(t, instance)
	out, err := host.Invoke(t.Context(), qqSendEnvelope(t, server.URL, "20001", "真实回复"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("denied = (%#v, %v)", parsed, err)
	}
}

func newQQTestHost(t *testing.T, instance *Instance) *testhost.Host {
	t.Helper()
	var host *testhost.Host
	host, err := testhost.New(qqonebot.NewHandler(func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return host.Call(ctx, capability, payload)
	}), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request", "action.complete"},
		HostCall: func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
			raw, err := sdk.EncodeHostRequest(capability, payload)
			if err != nil {
				return nil, err
			}
			return instance.Handle(ctx, raw)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func qqSendEnvelope(t *testing.T, baseURL, groupID, text string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"op": "send", "apiBaseURL": baseURL, "groupId": groupID, "text": text, "credential": "onebot",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "qq-1", TraceID: "trace-qq", TurnID: "turn-1"},
		Payload:     payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
