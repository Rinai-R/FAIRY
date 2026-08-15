package testhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"fairy/plugin"
	"fairy/plugin/sdk"
)

var ErrHandlerRequired = errors.New("plugin test host handler is required")
var ErrBudgetRequired = errors.New("plugin test host budget is required")

// Handler is the in-process plugin contract used by the test host. Authors can
// exercise the same envelopes without starting Desktop or compiling WASM.
type Handler func(ctx context.Context, envelope plugin.Envelope) (plugin.Envelope, error)

type HostCall func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error)

type Options struct {
	MaxInputBytes uint32
	MaxCalls      uint32
	Capabilities  []string
	HostCall      HostCall
}

func DefaultOptions() Options {
	return Options{MaxInputBytes: 64 << 10, MaxCalls: 256, Capabilities: []string{}}
}

type Host struct {
	handler Handler
	opts    Options
	calls   uint32
}

func New(handler Handler, opts Options) (*Host, error) {
	if handler == nil {
		return nil, ErrHandlerRequired
	}
	if opts.MaxInputBytes == 0 || opts.MaxCalls == 0 {
		return nil, ErrBudgetRequired
	}
	if opts.Capabilities == nil {
		opts.Capabilities = []string{}
	}
	return &Host{handler: handler, opts: opts}, nil
}

func (h *Host) Invoke(ctx context.Context, raw []byte) ([]byte, error) {
	if h == nil {
		return nil, ErrHandlerRequired
	}
	if ctx == nil {
		return nil, errors.New("plugin test host context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, &plugin.CodedError{Code: plugin.CodeCancelled, Message: err.Error()}
	}
	if uint32(len(raw)) > h.opts.MaxInputBytes {
		return nil, &plugin.CodedError{Code: plugin.CodeBudgetExceeded, Message: "plugin input exceeds budget"}
	}
	if h.calls >= h.opts.MaxCalls {
		return nil, &plugin.CodedError{Code: plugin.CodeBudgetExceeded, Message: "plugin call budget exhausted"}
	}
	h.calls++
	envelope, err := sdk.Decode(raw)
	if err != nil {
		return nil, err
	}
	if err := h.authorize(envelope); err != nil {
		return nil, err
	}
	out, err := h.handler(ctx, envelope)
	if err != nil {
		return nil, err
	}
	return sdk.Encode(out)
}

func (h *Host) Call(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
	if h == nil {
		return nil, ErrHandlerRequired
	}
	if ctx == nil {
		return nil, errors.New("plugin test host context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, &plugin.CodedError{Code: plugin.CodeCancelled, Message: err.Error()}
	}
	if capability == "" {
		return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: "capability is required"}
	}
	if !h.granted(capability) || h.opts.HostCall == nil {
		return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: fmt.Sprintf("%s: not granted", capability)}
	}
	return h.opts.HostCall(ctx, capability, payload)
}

func (h *Host) granted(capability string) bool {
	for _, name := range h.opts.Capabilities {
		if name == capability {
			return true
		}
	}
	return false
}

func (h *Host) authorize(envelope plugin.Envelope) error {
	required := requiredCapability(envelope)
	if required == "" || h.granted(required) {
		return nil
	}
	return &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: fmt.Sprintf("%s: not granted", required)}
}

func requiredCapability(envelope plugin.Envelope) string {
	if envelope.Kind != "handle" || len(envelope.Payload) == 0 {
		return ""
	}
	var payload struct {
		Capability string `json:"capability"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ""
	}
	return payload.Capability
}
