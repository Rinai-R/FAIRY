package tool

import (
	"context"
	"encoding/json"

	"fairy/plugin"
	"fairy/plugin/sdk"
)

type request struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type response struct {
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
	Source string          `json:"source"`
}

// Handle is the minimal tool plugin: it echoes validated arguments as a
// structured tool result. It does not import Core internals.
func Handle(ctx context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return plugin.Envelope{}, &plugin.CodedError{Code: plugin.CodeCancelled, Message: err.Error()}
		}
	}
	if envelope.Kind != "handle" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "tool plugin expects handle envelopes")
	}
	var payload request
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.Tool == "" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "tool payload is invalid")
	}
	if len(payload.Arguments) == 0 {
		payload.Arguments = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(response{Tool: payload.Tool, Result: payload.Arguments, Source: "plugin"})
	if err != nil {
		return plugin.Envelope{}, err
	}
	return sdk.Result(envelope.Correlation, body)
}
