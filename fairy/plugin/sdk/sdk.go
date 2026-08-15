package sdk

import (
	"bytes"
	"encoding/json"

	"fairy/plugin"
)

// Decode parses a Host ABI v1 envelope. This package is the Go/TinyGo SDK:
// it depends only on fairy/plugin and never imports Core or the WASM runtime.
func Decode(raw []byte) (plugin.Envelope, error) {
	return plugin.ParseEnvelope(bytes.NewReader(raw))
}

func Encode(envelope plugin.Envelope) ([]byte, error) {
	return plugin.EncodeEnvelope(envelope)
}

func Result(correlation plugin.Correlation, payload json.RawMessage) (plugin.Envelope, error) {
	envelope := plugin.NewResult(correlation, payload)
	_, err := plugin.EncodeEnvelope(envelope)
	return envelope, err
}

func Fail(correlation plugin.Correlation, code, message string) (plugin.Envelope, error) {
	envelope := plugin.NewError(correlation, code, message)
	_, err := plugin.EncodeEnvelope(envelope)
	return envelope, err
}

func EncodeResult(correlation plugin.Correlation, payload json.RawMessage) ([]byte, error) {
	return plugin.EncodeEnvelope(plugin.NewResult(correlation, payload))
}

func EncodeError(correlation plugin.Correlation, code, message string) ([]byte, error) {
	return plugin.EncodeEnvelope(plugin.NewError(correlation, code, message))
}

type HostRequest struct {
	Capability string          `json:"capability"`
	Payload    json.RawMessage `json:"payload"`
}

func EncodeHostRequest(capability string, payload json.RawMessage) ([]byte, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return json.Marshal(HostRequest{Capability: capability, Payload: payload})
}
