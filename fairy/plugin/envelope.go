package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type Correlation struct {
	PluginInstanceID  string `json:"pluginInstanceId"`
	TraceID           string `json:"traceId,omitempty"`
	TurnID            string `json:"turnId,omitempty"`
	ExternalMessageID string `json:"externalMessageId,omitempty"`
}

type Envelope struct {
	ABIVersion  uint32          `json:"abiVersion"`
	Kind        string          `json:"kind"`
	Correlation Correlation     `json:"correlation"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Error       *CodedError     `json:"error,omitempty"`
}

func ParseEnvelope(r io.Reader) (Envelope, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrEnvelopeInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Envelope{}, fmt.Errorf("%w: envelope must contain a single JSON value", ErrEnvelopeInvalid)
	}
	if err := validateEnvelope(envelope); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func validateEnvelope(envelope Envelope) error {
	if envelope.ABIVersion != ABIVersion {
		return coded(CodeABIIncompatible, fmt.Sprintf("envelope abiVersion = %d, want %d", envelope.ABIVersion, ABIVersion))
	}
	switch envelope.Kind {
	case "init", "handle", "shutdown", "result", "error":
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrEnvelopeInvalid, envelope.Kind)
	}
	if envelope.Correlation.PluginInstanceID == "" || len(envelope.Correlation.PluginInstanceID) > 128 {
		return fmt.Errorf("%w: pluginInstanceId is required", ErrEnvelopeInvalid)
	}
	if envelope.Kind == "error" {
		if envelope.Error == nil || envelope.Error.Code == "" || envelope.Error.Message == "" {
			return fmt.Errorf("%w: error envelope requires a stable code and message", ErrEnvelopeInvalid)
		}
		if !knownErrorCode(envelope.Error.Code) {
			return fmt.Errorf("%w: unknown error code %q", ErrEnvelopeInvalid, envelope.Error.Code)
		}
	}
	if len(envelope.Payload) > 0 && !json.Valid(envelope.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrEnvelopeInvalid)
	}
	return nil
}

func knownErrorCode(code string) bool {
	switch code {
	case CodeABIIncompatible, CodeManifestInvalid, CodeCapabilityDenied, CodeBudgetExceeded, CodeModuleTrap, CodeCancelled:
		return true
	default:
		return false
	}
}

func EncodeEnvelope(envelope Envelope) ([]byte, error) {
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(envelope); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func NewResult(correlation Correlation, payload json.RawMessage) Envelope {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return Envelope{ABIVersion: ABIVersion, Kind: "result", Correlation: correlation, Payload: payload}
}

func NewError(correlation Correlation, code, message string) Envelope {
	return Envelope{
		ABIVersion: ABIVersion, Kind: "error", Correlation: correlation,
		Error: &CodedError{Code: code, Message: message},
	}
}
