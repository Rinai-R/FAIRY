package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tetratelabs/wazero/api"

	"fairy/plugin"
)

type hostRequest struct {
	Capability string          `json:"capability"`
	Payload    json.RawMessage `json:"payload"`
}

type hostResult struct {
	OK      bool            `json:"ok"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

type kvPayload struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type correlatedPayload struct {
	PluginInstanceID  string          `json:"pluginInstanceId"`
	TraceID           string          `json:"traceId"`
	TurnID            string          `json:"turnId"`
	ExternalMessageID string          `json:"externalMessageId"`
	Status            string          `json:"status,omitempty"`
	Event             json.RawMessage `json:"event,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
}

type IngressRequest struct {
	Method string
	Path   string
	Body   string
}

type ingressRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body"`
}

func (h *Host) hostCall(ctx context.Context, mod api.Module, ptr, size uint32) uint64 {
	h.mu.Lock()
	instance := h.instances[mod.Name()]
	h.mu.Unlock()
	if instance == nil {
		return 0
	}
	packed, err := instance.dispatchHost(ctx, mod, ptr, size)
	if err == nil {
		return packed
	}
	packed, writeErr := instance.writeHostJSON(ctx, mod, resultFromErr(instance.scrub(err.Error()), err))
	if writeErr != nil {
		return 0
	}
	return packed
}

func (i *Instance) dispatchHost(ctx context.Context, mod api.Module, ptr, size uint32) (uint64, error) {
	if ctx == nil {
		return 0, errors.New("plugin wasm host context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, cancelErr(err)
	}
	if size > i.budget.MaxInputBytes {
		return 0, coded(plugin.CodeBudgetExceeded, "plugin host input exceeds budget")
	}

	i.mu.Lock()
	if i.closed || i.poison != nil {
		err := i.poison
		i.mu.Unlock()
		if err == nil {
			err = ErrHostClosed
		}
		return 0, err
	}
	if i.hostCalls >= i.budget.MaxHostCalls {
		i.mu.Unlock()
		return 0, coded(plugin.CodeBudgetExceeded, "plugin host call budget exhausted")
	}
	i.hostCalls++
	grant := i.grant
	i.mu.Unlock()

	memory := mod.Memory()
	if memory == nil {
		return 0, coded(plugin.CodeManifestInvalid, "plugin module memory is not exported")
	}
	raw, ok := memory.Read(ptr, size)
	if !ok {
		return 0, coded(plugin.CodeModuleTrap, "plugin host call pointer is outside module memory")
	}
	request, err := parseHostRequest(raw)
	if err != nil {
		return 0, err
	}
	body, err := i.serveCapability(ctx, grant, request)
	if err != nil {
		return 0, err
	}
	if uint32(len(body)) > i.budget.MaxOutputBytes {
		return 0, coded(plugin.CodeBudgetExceeded, "plugin host output exceeds budget")
	}
	return i.writeHostJSON(ctx, mod, body)
}

func (i *Instance) serveCapability(ctx context.Context, grant Grant, request hostRequest) ([]byte, error) {
	switch request.Capability {
	case "http.request":
		return i.httpRequest(ctx, grant, request.Payload)
	case "http.ingress":
		return i.takeIngress(grant)
	case "state.read":
		return i.stateRead(grant, request.Payload)
	case "state.write":
		return i.stateWrite(grant, request.Payload)
	case "timer.poll":
		return i.timerPoll(grant)
	case "event.emit":
		return i.recordCorrelated(grant.Event, "event.emit", request.Payload, "event")
	case "action.complete":
		return i.recordCorrelated(grant.Action, "action.complete", request.Payload, "action")
	case "tool.result":
		return i.recordCorrelated(grant.Tool, "tool.result", request.Payload, "tool")
	default:
		return nil, capabilityDenied(request.Capability, "unknown capability")
	}
}

func parseHostRequest(raw []byte) (hostRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request hostRequest
	if err := decoder.Decode(&request); err != nil {
		return hostRequest{}, coded(plugin.CodeManifestInvalid, "plugin host call is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return hostRequest{}, coded(plugin.CodeManifestInvalid, "plugin host call must contain a single JSON value")
	}
	if request.Capability == "" {
		return hostRequest{}, capabilityDenied("", "capability is required")
	}
	if len(request.Payload) == 0 {
		request.Payload = json.RawMessage(`{}`)
	}
	return request, nil
}

func (i *Instance) takeIngress(grant Grant) ([]byte, error) {
	if grant.HTTPIngress == nil {
		return nil, capabilityDenied("http.ingress", "not granted")
	}
	i.mu.Lock()
	pending := i.pendingIngress
	i.pendingIngress = nil
	i.mu.Unlock()
	if pending == nil {
		body, err := json.Marshal(map[string]any{"pending": false})
		if err != nil {
			return nil, fmt.Errorf("encoding http.ingress result: %w", err)
		}
		return marshalHostResult(hostResult{OK: true, Body: body})
	}
	body, err := json.Marshal(pending)
	if err != nil {
		return nil, fmt.Errorf("encoding http.ingress request: %w", err)
	}
	return marshalHostResult(hostResult{OK: true, Body: body})
}

func (i *Instance) StageIngress(req IngressRequest) error {
	if i == nil {
		return ErrHostClosed
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.poison != nil {
		if i.poison != nil {
			return i.poison
		}
		return ErrHostClosed
	}
	if i.grant.HTTPIngress == nil {
		return capabilityDenied("http.ingress", "not granted")
	}
	if uint32(len(req.Body)) > i.grant.HTTPIngress.MaxBodyBytes {
		return coded(plugin.CodeBudgetExceeded, "http.ingress body exceeds budget")
	}
	if req.Method == "" || req.Path == "" {
		return capabilityDenied("http.ingress", "method and path are required")
	}
	if i.pendingIngress != nil {
		return capabilityDenied("http.ingress", "a request is already staged")
	}
	i.pendingIngress = &ingressRequest{Method: req.Method, Path: req.Path, Body: req.Body}
	return nil
}

func (i *Instance) stateRead(grant Grant, payload json.RawMessage) ([]byte, error) {
	if !grant.State {
		return nil, capabilityDenied("state.read", "not granted")
	}
	kv, err := parseKV(payload, false)
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	value, found := i.state[kv.Key]
	i.mu.Unlock()
	result := map[string]any{"found": found, "key": kv.Key}
	if found {
		result["value"] = value
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding state.read result: %w", err)
	}
	return marshalHostResult(hostResult{OK: true, Body: body})
}

func (i *Instance) stateWrite(grant Grant, payload json.RawMessage) ([]byte, error) {
	if !grant.State {
		return nil, capabilityDenied("state.write", "not granted")
	}
	kv, err := parseKV(payload, true)
	if err != nil {
		return nil, err
	}
	if uint32(len(kv.Value)) > i.budget.MaxOutputBytes {
		return nil, coded(plugin.CodeBudgetExceeded, "state.write value exceeds budget")
	}
	i.mu.Lock()
	i.state[kv.Key] = kv.Value
	i.mu.Unlock()
	return marshalHostResult(hostResult{OK: true})
}

func (i *Instance) timerPoll(grant Grant) ([]byte, error) {
	if !grant.Timer {
		return nil, capabilityDenied("timer.poll", "not granted")
	}
	i.mu.Lock()
	due := i.due
	tick := i.tick
	i.due = false
	i.mu.Unlock()
	body, err := json.Marshal(map[string]any{"due": due, "tick": tick})
	if err != nil {
		return nil, fmt.Errorf("encoding timer.poll result: %w", err)
	}
	return marshalHostResult(hostResult{OK: true, Body: body})
}

func (i *Instance) NoteTick() error {
	if i == nil {
		return ErrHostClosed
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.poison != nil {
		if i.poison != nil {
			return i.poison
		}
		return ErrHostClosed
	}
	if !i.grant.Timer {
		return capabilityDenied("timer.poll", "not granted")
	}
	i.tick++
	i.due = true
	return nil
}

func (i *Instance) recordCorrelated(granted bool, name string, payload json.RawMessage, kind string) ([]byte, error) {
	if !granted {
		return nil, capabilityDenied(name, "not granted")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var body correlatedPayload
	if err := decoder.Decode(&body); err != nil {
		return nil, capabilityDenied(name, "payload is invalid")
	}
	if body.PluginInstanceID == "" || len(body.PluginInstanceID) > 128 {
		return nil, capabilityDenied(name, "pluginInstanceId is required")
	}
	switch kind {
	case "event":
		if len(body.Event) == 0 {
			return nil, capabilityDenied(name, "event is required")
		}
	case "action":
		if body.Status != "succeeded" && body.Status != "failed" {
			return nil, capabilityDenied(name, "status must be succeeded or failed")
		}
	case "tool":
		if len(body.Result) == 0 {
			return nil, capabilityDenied(name, "result is required")
		}
	default:
		return nil, capabilityDenied(name, "unknown correlated kind")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", name, err)
	}
	i.mu.Lock()
	switch kind {
	case "event":
		i.lastEvent = raw
	case "action":
		i.lastAction = raw
	case "tool":
		i.lastTool = raw
	}
	i.mu.Unlock()
	return marshalHostResult(hostResult{OK: true})
}

func (i *Instance) LastEvent() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]byte(nil), i.lastEvent...)
}

func (i *Instance) LastAction() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]byte(nil), i.lastAction...)
}

func (i *Instance) LastTool() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]byte(nil), i.lastTool...)
}

func parseKV(payload json.RawMessage, requireValue bool) (kvPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var kv kvPayload
	if err := decoder.Decode(&kv); err != nil {
		return kvPayload{}, capabilityDenied("state", "payload is invalid")
	}
	if kv.Key == "" || len(kv.Key) > 128 || strings.ContainsAny(kv.Key, "/\\") || strings.Contains(kv.Key, "..") {
		return kvPayload{}, capabilityDenied("state", "key is invalid")
	}
	if requireValue && kv.Value == "" {
		return kvPayload{}, capabilityDenied("state.write", "value is required")
	}
	return kv, nil
}

func (i *Instance) writeHostJSON(ctx context.Context, mod api.Module, payload []byte) (uint64, error) {
	alloc := mod.ExportedFunction(plugin.ExportAlloc)
	if alloc == nil {
		return 0, coded(plugin.CodeManifestInvalid, "missing plugin export "+plugin.ExportAlloc)
	}
	results, err := alloc.Call(ctx, uint64(len(payload)))
	if err != nil {
		return 0, guestCallError(err)
	}
	if len(results) != 1 || results[0] == 0 {
		return 0, coded(plugin.CodeBudgetExceeded, "plugin allocator refused host output")
	}
	ptr := uint32(results[0])
	if !mod.Memory().Write(ptr, payload) {
		return 0, coded(plugin.CodeModuleTrap, "plugin host output pointer is outside module memory")
	}
	return uint64(ptr)<<32 | uint64(len(payload)), nil
}

func (i *Instance) scrub(text string) string {
	for _, secret := range i.grant.secrets() {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}

func marshalHostResult(result hostResult) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding plugin host result: %w", err)
	}
	return raw, nil
}

func resultFromErr(message string, err error) []byte {
	code := plugin.CodeModuleTrap
	var codedErr *plugin.CodedError
	if errors.As(err, &codedErr) {
		code = codedErr.Code
	}
	return []byte(`{"ok":false,"code":` + strconv.Quote(code) + `,"message":` + strconv.Quote(message) + `}`)
}
