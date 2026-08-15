package testhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"fairy/plugin"
	"fairy/plugin/examples/event"
	"fairy/plugin/examples/tool"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
)

func TestTestHostEchoesAndEnforcesBudgetAndCapabilities(t *testing.T) {
	host, err := testhost.New(echo, testhost.Options{MaxInputBytes: 1024, MaxCalls: 1, Capabilities: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion: plugin.ABIVersion, Kind: "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "echo-1"},
		Payload:     json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := host.Invoke(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sdk.Decode(out)
	if err != nil || got.Kind != "handle" || got.Correlation.PluginInstanceID != "echo-1" {
		t.Fatalf("echo = (%#v, %v)", got, err)
	}
	_, err = host.Invoke(t.Context(), raw)
	if !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("call budget = %v", err)
	}
	denied, err := testhost.New(echo, testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	deniedRaw, err := sdk.Encode(plugin.Envelope{
		ABIVersion: plugin.ABIVersion, Kind: "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "echo-1"},
		Payload:     json.RawMessage(`{"capability":"http.request"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = denied.Invoke(t.Context(), deniedRaw)
	if !errors.Is(err, plugin.ErrCapabilityDenied) {
		t.Fatalf("capability = %v", err)
	}
	tiny, err := testhost.New(echo, testhost.Options{MaxInputBytes: 8, MaxCalls: 8, Capabilities: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tiny.Invoke(t.Context(), bytes.Repeat([]byte("n"), 9))
	if !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("input budget = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = denied.Invoke(ctx, raw)
	if !errors.Is(err, plugin.ErrCancelled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestMinimalEventAndToolPluginsPreserveCorrelation(t *testing.T) {
	eventHost, err := testhost.New(event.Handle, testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	eventIn, err := sdk.Encode(plugin.Envelope{
		ABIVersion: plugin.ABIVersion, Kind: "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "event-1", TraceID: "trace-1", ExternalMessageID: "ext-1"},
		Payload:     json.RawMessage(`{"event":{"type":"message","text":"hi"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	eventOut, err := eventHost.Invoke(t.Context(), eventIn)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(eventOut)
	if err != nil || parsed.Kind != "result" || parsed.Correlation.TraceID != "trace-1" || parsed.Correlation.ExternalMessageID != "ext-1" {
		t.Fatalf("event = (%#v, %v)", parsed, err)
	}
	if !bytes.Contains(parsed.Payload, []byte(`"accepted":true`)) {
		t.Fatalf("event payload = %s", parsed.Payload)
	}

	toolHost, err := testhost.New(tool.Handle, testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	toolIn, err := sdk.Encode(plugin.Envelope{
		ABIVersion: plugin.ABIVersion, Kind: "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "tool-1", TraceID: "trace-2"},
		Payload:     json.RawMessage(`{"tool":"echo","arguments":{"q":"x"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	toolOut, err := toolHost.Invoke(t.Context(), toolIn)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = sdk.Decode(toolOut)
	if err != nil || parsed.Kind != "result" || parsed.Correlation.TraceID != "trace-2" {
		t.Fatalf("tool = (%#v, %v)", parsed, err)
	}
	if !bytes.Contains(parsed.Payload, []byte(`"source":"plugin"`)) || !bytes.Contains(parsed.Payload, []byte(`"q":"x"`)) {
		t.Fatalf("tool payload = %s", parsed.Payload)
	}
}

func TestTestHostCallDeniesUngrantedHTTP(t *testing.T) {
	host, err := testhost.New(echo, testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Call(t.Context(), "http.request", json.RawMessage(`{"method":"GET","url":"https://example.invalid"}`))
	if !errors.Is(err, plugin.ErrCapabilityDenied) {
		t.Fatalf("Call() = %v", err)
	}
	granted, err := testhost.New(echo, testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = granted.Call(t.Context(), "http.request", json.RawMessage(`{}`))
	if !errors.Is(err, plugin.ErrCapabilityDenied) {
		t.Fatalf("Call() without HostCall = %v", err)
	}
}

func echo(_ context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
	return envelope, nil
}
