package wasm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"fairy/plugin"
)

const pluginSecret = "sk-live-plugin-secret-9f3a"

func TestHostCallDeniesUngrantedCapabilities(t *testing.T) {
	instance := loadProxy(t, Grant{})
	for _, capability := range plugin.KnownCapabilities() {
		result := callHost(t, instance, capability, map[string]any{})
		if result.OK || result.Code != plugin.CodeCapabilityDenied {
			t.Fatalf("%s = %+v, want CAPABILITY_DENIED", capability, result)
		}
		assertNoSecret(t, result.Message)
	}
}

func TestHTTPRequestGrantAllowsAuthorizedTargetAndInjectsCredential(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	grant := Grant{HTTPRequest: mustHTTPGrant(t, server.URL, 64)}
	if err := grant.HTTPRequest.SetCredential("search", pluginSecret); err != nil {
		t.Fatal(err)
	}
	instance := loadProxy(t, grant)
	result := callHost(t, instance, "http.request", map[string]any{
		"method":     "GET",
		"url":        server.URL + "/v1/search",
		"credential": "search",
	})
	if !result.OK {
		t.Fatalf("http.request = %+v", result)
	}
	if gotAuth != "Bearer "+pluginSecret {
		t.Fatalf("Authorization = %q, want injected bearer", gotAuth)
	}
	assertNoSecret(t, string(result.Body), result.Message)
	var body httpResponseBody
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != 200 || body.Body != `{"ok":true}` {
		t.Fatalf("response = %+v", body)
	}
}

func TestHTTPRequestGrantRejectsUnauthorizedHost(t *testing.T) {
	allowed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("authorized server should not be called")
	}))
	t.Cleanup(allowed.Close)
	blocked := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("blocked server should not be called")
	}))
	t.Cleanup(blocked.Close)
	grant := Grant{HTTPRequest: mustHTTPGrant(t, allowed.URL, 64)}
	instance := loadProxy(t, grant)
	result := callHost(t, instance, "http.request", map[string]any{
		"method": "GET",
		"url":    blocked.URL + "/secret",
	})
	if result.OK || result.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("http.request = %+v, want CAPABILITY_DENIED", result)
	}
	assertNoSecret(t, result.Message, string(result.Body))
}

func TestHTTPRequestRejectsUnknownCredentialHandle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be called")
	}))
	t.Cleanup(server.Close)
	grant := Grant{HTTPRequest: mustHTTPGrant(t, server.URL, 64)}
	if err := grant.HTTPRequest.SetCredential("search", pluginSecret); err != nil {
		t.Fatal(err)
	}
	instance := loadProxy(t, grant)
	result := callHost(t, instance, "http.request", map[string]any{
		"method":     "GET",
		"url":        server.URL,
		"credential": "other",
	})
	if result.OK || result.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("http.request = %+v, want CAPABILITY_DENIED", result)
	}
	assertNoSecret(t, result.Message)
}

func TestHTTPIngressIsHostStagedAndBodyBounded(t *testing.T) {
	instance := loadProxy(t, Grant{HTTPIngress: &HTTPIngressGrant{MaxBodyBytes: 8}})
	if err := instance.StageIngress(IngressRequest{Method: "POST", Path: "/hook", Body: "abcdefghij"}); !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("StageIngress(oversize) = %v", err)
	}
	if err := instance.StageIngress(IngressRequest{Method: "POST", Path: "/hook", Body: "ok"}); err != nil {
		t.Fatal(err)
	}
	result := callHost(t, instance, "http.ingress", map[string]any{})
	if !result.OK {
		t.Fatalf("http.ingress = %+v", result)
	}
	var pending ingressRequest
	if err := json.Unmarshal(result.Body, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Method != "POST" || pending.Path != "/hook" || pending.Body != "ok" {
		t.Fatalf("ingress = %+v", pending)
	}
}

func TestStateIsInstanceNamespaced(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	a, err := host.LoadGranted(t.Context(), "state-a", hostProxyGuestWASM(), DefaultBudget(), Grant{State: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(t.Context()) })
	b, err := host.LoadGranted(t.Context(), "state-b", hostProxyGuestWASM(), DefaultBudget(), Grant{State: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(t.Context()) })
	if result := callHost(t, a, "state.write", map[string]any{"key": "token", "value": "alpha"}); !result.OK {
		t.Fatalf("write a = %+v", result)
	}
	readA := callHost(t, a, "state.read", map[string]any{"key": "token"})
	readB := callHost(t, b, "state.read", map[string]any{"key": "token"})
	if !readA.OK || !strings.Contains(string(readA.Body), `"value":"alpha"`) {
		t.Fatalf("read a = %+v", readA)
	}
	if !readB.OK || !strings.Contains(string(readB.Body), `"found":false`) {
		t.Fatalf("read b = %+v, want isolated miss", readB)
	}
}

func TestTimerPollRequiresHostTick(t *testing.T) {
	instance := loadProxy(t, Grant{Timer: true})
	idle := callHost(t, instance, "timer.poll", map[string]any{})
	if !idle.OK || !strings.Contains(string(idle.Body), `"due":false`) {
		t.Fatalf("idle poll = %+v", idle)
	}
	if err := instance.NoteTick(); err != nil {
		t.Fatal(err)
	}
	due := callHost(t, instance, "timer.poll", map[string]any{})
	if !due.OK || !strings.Contains(string(due.Body), `"due":true`) {
		t.Fatalf("due poll = %+v", due)
	}
}

func TestEventActionToolRequireCorrelationAndStableActionStatus(t *testing.T) {
	instance := loadProxy(t, Grant{Event: true, Action: true, Tool: true})
	if result := callHost(t, instance, "event.emit", map[string]any{"event": map[string]any{"t": "x"}}); result.OK {
		t.Fatalf("event without pluginInstanceId = %+v", result)
	}
	if result := callHost(t, instance, "event.emit", map[string]any{
		"pluginInstanceId": "evt-1",
		"traceId":          "tr-1",
		"event":            map[string]any{"t": "x"},
	}); !result.OK {
		t.Fatalf("event.emit = %+v", result)
	}
	if result := callHost(t, instance, "action.complete", map[string]any{
		"pluginInstanceId": "evt-1",
		"status":           "queued",
	}); result.OK || result.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("queued action = %+v, want denied", result)
	}
	if result := callHost(t, instance, "action.complete", map[string]any{
		"pluginInstanceId": "evt-1",
		"status":           "succeeded",
	}); !result.OK {
		t.Fatalf("action.complete = %+v", result)
	}
	if result := callHost(t, instance, "tool.result", map[string]any{
		"pluginInstanceId": "evt-1",
		"result":           map[string]any{"n": 1},
	}); !result.OK {
		t.Fatalf("tool.result = %+v", result)
	}
	if len(instance.LastEvent()) == 0 || len(instance.LastAction()) == 0 || len(instance.LastTool()) == 0 {
		t.Fatal("host did not retain correlated plugin results")
	}
	queued := instance.PollEvents(0, 8)
	if len(queued) != 1 || queued[0].Sequence != 1 {
		t.Fatalf("event queue = %#v", queued)
	}
}

func TestHostCallBudgetIsEnforcedWithoutPoison(t *testing.T) {
	budget := DefaultBudget()
	budget.MaxHostCalls = 1
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	instance, err := host.LoadGranted(t.Context(), "budget", hostProxyGuestWASM(), budget, Grant{Timer: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	if result := callHost(t, instance, "timer.poll", map[string]any{}); !result.OK {
		t.Fatalf("first poll = %+v", result)
	}
	second := callHost(t, instance, "timer.poll", map[string]any{})
	if second.OK || second.Code != plugin.CodeBudgetExceeded {
		t.Fatalf("second poll = %+v, want BUDGET_EXCEEDED", second)
	}
	if _, err := instance.Handle(t.Context(), []byte(`{"capability":"timer.poll","payload":{}}`)); err != nil && errors.Is(err, plugin.ErrModuleTrap) {
		t.Fatalf("host call budget poisoned the instance: %v", err)
	}
}

func TestDeniedNoteTickAndStageIngressFailClosed(t *testing.T) {
	instance := loadProxy(t, Grant{})
	if err := instance.NoteTick(); !errors.Is(err, plugin.ErrCapabilityDenied) {
		t.Fatalf("NoteTick() = %v", err)
	}
	if err := instance.StageIngress(IngressRequest{Method: "GET", Path: "/x", Body: "y"}); !errors.Is(err, plugin.ErrCapabilityDenied) {
		t.Fatalf("StageIngress() = %v", err)
	}
}

func loadProxy(t *testing.T, grant Grant) *Instance {
	t.Helper()
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	instance, err := host.LoadGranted(t.Context(), "proxy", hostProxyGuestWASM(), DefaultBudget(), grant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	return instance
}

func callHost(t *testing.T, instance *Instance, capability string, payload any) hostResult {
	t.Helper()
	raw, err := json.Marshal(hostRequest{
		Capability: capability,
		Payload:    jsonRaw(t, payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := instance.Handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var result hostResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("host result %q: %v", out, err)
	}
	assertNoSecret(t, string(out), result.Message)
	return result
}

func jsonRaw(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustHTTPGrant(t *testing.T, rawURL string, max uint32) *HTTPRequestGrant {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return &HTTPRequestGrant{
		Scheme:           parsed.Scheme,
		Host:             parsed.Hostname(),
		Port:             uint16(port),
		Methods:          []string{"GET", "POST"},
		MaxResponseBytes: max,
	}
}

func assertNoSecret(t *testing.T, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if strings.Contains(part, pluginSecret) {
			t.Fatalf("secret leaked in %q", part)
		}
	}
}
