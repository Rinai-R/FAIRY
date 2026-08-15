package plugin

import "errors"

const (
	ABIVersion     = 1
	HostNamespace  = "fairy_host_v1"
	EntryModule    = "module.wasm"
	ManifestSchema = 1
)

const (
	ExportAlloc    = "fairy_alloc"
	ExportFree     = "fairy_free"
	ExportInit     = "fairy_init"
	ExportHandle   = "fairy_handle"
	ExportShutdown = "fairy_shutdown"
)

const (
	CodeABIIncompatible  = "ABI_INCOMPATIBLE"
	CodeManifestInvalid  = "MANIFEST_INVALID"
	CodeCapabilityDenied = "CAPABILITY_DENIED"
	CodeBudgetExceeded   = "BUDGET_EXCEEDED"
	CodeModuleTrap       = "MODULE_TRAP"
	CodeCancelled        = "CANCELLED"
)

var (
	ErrABIIncompatible  = errors.New("plugin ABI is incompatible with this host")
	ErrManifestInvalid  = errors.New("plugin manifest is invalid")
	ErrEnvelopeInvalid  = errors.New("plugin envelope is invalid")
	ErrCapabilityDenied = errors.New("plugin capability is denied")
	ErrBudgetExceeded   = errors.New("plugin execution budget exceeded")
	ErrCancelled        = errors.New("plugin execution cancelled")
	ErrModuleTrap       = errors.New("plugin module trapped")
)

func RequiredExports() []string {
	return []string{ExportAlloc, ExportFree, ExportInit, ExportHandle, ExportShutdown}
}

func KnownCapabilities() []string {
	return []string{
		"http.request",
		"http.ingress",
		"state.read",
		"state.write",
		"timer.poll",
		"event.emit",
		"action.complete",
		"tool.result",
	}
}

type CodedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CodedError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *CodedError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case CodeABIIncompatible:
		return ErrABIIncompatible
	case CodeManifestInvalid:
		return ErrManifestInvalid
	case CodeCapabilityDenied:
		return ErrCapabilityDenied
	case CodeBudgetExceeded:
		return ErrBudgetExceeded
	case CodeModuleTrap:
		return ErrModuleTrap
	case CodeCancelled:
		return ErrCancelled
	default:
		return nil
	}
}
