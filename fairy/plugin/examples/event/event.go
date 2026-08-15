package event

import (
	"context"
	"encoding/json"

	"fairy/plugin"
	"fairy/plugin/sdk"
)

type request struct {
	Event eventBody `json:"event"`
}

type eventBody struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type response struct {
	Accepted bool   `json:"accepted"`
	Type     string `json:"type"`
}

// Handle is the minimal event plugin: it accepts a correlated inbound event
// and returns a result envelope. It does not import Core internals.
func Handle(ctx context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return plugin.Envelope{}, &plugin.CodedError{Code: plugin.CodeCancelled, Message: err.Error()}
		}
	}
	if envelope.Kind != "handle" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "event plugin expects handle envelopes")
	}
	var payload request
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.Event.Type == "" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "event payload is invalid")
	}
	body, err := json.Marshal(response{Accepted: true, Type: payload.Event.Type})
	if err != nil {
		return plugin.Envelope{}, err
	}
	return sdk.Result(envelope.Correlation, body)
}
