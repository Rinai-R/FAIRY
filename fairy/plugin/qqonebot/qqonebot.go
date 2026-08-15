package qqonebot

import (
	"context"
	"encoding/json"
	"errors"

	"fairy/plugin"
	"fairy/plugin/sdk"
)

const (
	PluginID = "fairy.plugin.qq-onebot"
	MaxHits  = 256
)

func Manifest() plugin.Manifest {
	return plugin.Manifest{
		SchemaVersion:       plugin.ManifestSchema,
		ID:                  PluginID,
		Version:             "1.0.0",
		ABI:                 plugin.ABIRange{Min: plugin.ABIVersion, Max: plugin.ABIVersion},
		Entry:               plugin.EntryModule,
		Exports:             plugin.RequiredExports(),
		Capabilities:        []string{"http.request", "http.ingress", "event.emit", "action.complete"},
		ConfigSchemaVersion: 1,
		DataSchemaVersion:   1,
	}
}

func Discover(instances []plugin.InstanceRecord) (instanceID string, ok bool) {
	for _, instance := range instances {
		if instance.PluginID != PluginID || !instance.Enabled || instance.Lifecycle != "ready" {
			continue
		}
		if !hasAll(instance.CapabilityGrants, "http.request", "event.emit", "action.complete") {
			continue
		}
		return instance.ID, true
	}
	return "", false
}

func NewHandler(call func(context.Context, string, json.RawMessage) ([]byte, error)) func(context.Context, plugin.Envelope) (plugin.Envelope, error) {
	return func(ctx context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
		return Handle(ctx, envelope, call)
	}
}

func Handle(ctx context.Context, envelope plugin.Envelope, call func(context.Context, string, json.RawMessage) ([]byte, error)) (plugin.Envelope, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return plugin.Envelope{}, &plugin.CodedError{Code: plugin.CodeCancelled, Message: err.Error()}
		}
	}
	if envelope.Kind != "handle" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "qq-onebot plugin expects handle envelopes")
	}
	var payload struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.Op == "" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "qq-onebot payload is invalid")
	}
	switch payload.Op {
	case "parse":
		return handleParse(envelope)
	case "send":
		return handleSend(ctx, envelope, call)
	default:
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "qq-onebot op is unknown")
	}
}

func handleParse(envelope plugin.Envelope) (plugin.Envelope, error) {
	var payload struct {
		Op     string          `json:"op"`
		Raw    json.RawMessage `json:"raw"`
		SelfID string          `json:"selfId"`
		Allow  []string        `json:"groupAllowlist"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "qq-onebot parse payload is invalid")
	}
	event, err := ParseEvent(payload.Raw, payload.SelfID)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	if event.Kind == "group" {
		allowed, err := GroupAllowed(payload.Allow, event.GroupID)
		if err != nil {
			return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
		}
		if !allowed {
			body, err := json.Marshal(map[string]any{"accepted": false, "reason": "not_whitelisted", "event": event})
			if err != nil {
				return plugin.Envelope{}, err
			}
			return sdk.Result(envelope.Correlation, body)
		}
	}
	body, err := json.Marshal(map[string]any{"accepted": true, "event": event})
	if err != nil {
		return plugin.Envelope{}, err
	}
	return sdk.Result(envelope.Correlation, body)
}

func handleSend(ctx context.Context, envelope plugin.Envelope, call func(context.Context, string, json.RawMessage) ([]byte, error)) (plugin.Envelope, error) {
	if call == nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeCapabilityDenied, "http.request: not granted")
	}
	var payload SendRequest
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "qq-onebot send payload is invalid")
	}
	receipt, err := Send(ctx, call, payload)
	if err != nil {
		var coded *plugin.CodedError
		if errors.As(err, &coded) {
			return sdk.Fail(envelope.Correlation, coded.Code, coded.Message)
		}
		return plugin.Envelope{}, err
	}
	if call != nil && envelope.Correlation.PluginInstanceID != "" {
		action, err := json.Marshal(map[string]any{
			"pluginInstanceId":  envelope.Correlation.PluginInstanceID,
			"traceId":           envelope.Correlation.TraceID,
			"turnId":            envelope.Correlation.TurnID,
			"externalMessageId": receipt.ExternalMessageID,
			"status":            receipt.Status,
		})
		if err != nil {
			return plugin.Envelope{}, err
		}
		if _, err := call(ctx, "action.complete", action); err != nil {
			var coded *plugin.CodedError
			if errors.As(err, &coded) {
				return sdk.Fail(envelope.Correlation, coded.Code, coded.Message)
			}
			return plugin.Envelope{}, err
		}
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return plugin.Envelope{}, err
	}
	return sdk.Result(envelope.Correlation, body)
}

func hasAll(grants []string, names ...string) bool {
	have := map[string]struct{}{}
	for _, grant := range grants {
		have[grant] = struct{}{}
	}
	for _, name := range names {
		if _, ok := have[name]; !ok {
			return false
		}
	}
	return true
}
