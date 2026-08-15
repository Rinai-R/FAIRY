package sdk_test

import (
	"encoding/json"
	"strings"
	"testing"

	"fairy/plugin"
	"fairy/plugin/sdk"
)

func TestSDKEncodesResultAndErrorWithoutSecrets(t *testing.T) {
	correlation := plugin.Correlation{PluginInstanceID: "echo-1", TraceID: "trace-1"}
	raw, err := sdk.EncodeResult(correlation, json.RawMessage(`{"accepted":true}`))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sdk.Decode(raw)
	if err != nil || envelope.Kind != "result" || envelope.Correlation.TraceID != "trace-1" {
		t.Fatalf("result = (%#v, %v)", envelope, err)
	}
	fail, err := sdk.EncodeError(correlation, plugin.CodeCapabilityDenied, "http.request: not granted")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(fail)
	if err != nil || parsed.Error == nil || parsed.Error.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("error = (%#v, %v)", parsed, err)
	}
	host, err := sdk.EncodeHostRequest("http.request", json.RawMessage(`{"url":"https://example.invalid"}`))
	if err != nil || !strings.Contains(string(host), `"capability":"http.request"`) {
		t.Fatalf("host request = %s, %v", host, err)
	}
	if strings.Contains(string(raw)+string(fail)+string(host), "sk-live") {
		t.Fatal("sdk output contained a credential token")
	}
}
