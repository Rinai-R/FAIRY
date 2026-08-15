package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"fairy/plugin"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
)

func TestSharedEchoFixtureMatchesTestHostAndRealHost(t *testing.T) {
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion: plugin.ABIVersion, Kind: "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "echo-1", TraceID: "trace-1"},
		Payload:     json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	native, err := testhost.New(func(_ context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
		return envelope, nil
	}, testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	nativeOut, err := native.Invoke(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	nativeEnv, err := sdk.Decode(nativeOut)
	if err != nil {
		t.Fatal(err)
	}

	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	instance, err := host.Load(t.Context(), "echo-1", echoGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	wasmOut, err := instance.Handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	wasmEnv, err := sdk.Decode(wasmOut)
	if err != nil {
		t.Fatal(err)
	}
	if nativeEnv.Kind != wasmEnv.Kind || nativeEnv.Correlation != wasmEnv.Correlation || string(nativeEnv.Payload) != string(wasmEnv.Payload) {
		t.Fatalf("native=%#v wasm=%#v", nativeEnv, wasmEnv)
	}

	oversize := append(raw, make([]byte, DefaultBudget().MaxInputBytes)...)
	_, nativeErr := native.Invoke(t.Context(), oversize)
	_, wasmErr := instance.Handle(t.Context(), oversize)
	if !errors.Is(nativeErr, plugin.ErrBudgetExceeded) || !errors.Is(wasmErr, plugin.ErrBudgetExceeded) {
		t.Fatalf("budget native=%v wasm=%v", nativeErr, wasmErr)
	}
}
