package tool

import (
	"encoding/json"
	"errors"
	"strings"

	"fairy/runtime/model"
	"fairy/transport/session"
)

// RuntimeAvailability is computed by Turn before a model call. RouteCall uses
// it to make tool authorization and failure disposition deterministic without
// reading Turn state.
type RuntimeAvailability struct {
	WebSearch bool
	Desktop   bool
	Sticker   bool
}

type CallDisposition string

const (
	CallReady  CallDisposition = "ready"
	CallResult CallDisposition = "result"
	CallReject CallDisposition = "reject"
)

type RoutedCall struct {
	Call        model.FunctionCall
	Query       string
	Disposition CallDisposition
	Status      string
	Err         error
}

// SpecsForRuntime is the single catalog entry used by the response loop.
func SpecsForRuntime(availability RuntimeAvailability, resolved session.Resolved) []model.ToolSpec {
	tools := SpecsForInteraction(availability.WebSearch, resolved)
	if availability.Desktop {
		tools = append(tools, model.ToolSpec{
			Name:        DesktopObserve,
			Description: "Capture the current main desktop display once when the user's request cannot be answered reliably without seeing what is visibly on screen. Call at most once in a turn. Do not use for general curiosity.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		})
	}
	if availability.Sticker {
		tools = append(tools, model.ToolSpec{
			Name:        StickerSearch,
			Description: "Search the managed sticker library by intended emotion or conversational meaning. Results contain only human-authored descriptions and tags. A returned ID is selectable only in this turn's final sticker chain; never invent or reuse another ID.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Short intended emotion, reaction, or conversational meaning"}},"required":["query"],"additionalProperties":false}`),
		})
	}
	return tools
}

// RouteCall validates arguments, enforces interaction scope, and tells Turn
// whether a failure becomes a model-visible result or rejects the Turn.
func RouteCall(call model.FunctionCall, availability RuntimeAvailability, resolved session.Resolved) RoutedCall {
	routed := RoutedCall{Call: call, Disposition: CallReady, Status: "ok"}
	if call.Name == DesktopObserve {
		if err := validateDesktopArguments(call.Arguments); err != nil {
			return resultFailure(routed, "args_invalid", err)
		}
		if !availability.Desktop {
			return rejected(routed, errors.New("desktop_observe is unavailable for this interaction"))
		}
		return routed
	}

	query, err := ParseQuery(call.Arguments)
	if err != nil {
		return resultFailure(routed, "args_invalid", err)
	}
	routed.Query = query
	switch call.Name {
	case MemorySearch:
		if !resolved.AllowsPersonalMemory() {
			return rejected(routed, errors.New("memory_search is unavailable for public interactions"))
		}
	case PublicMemorySearch:
		if resolved.AllowsPersonalMemory() {
			return rejected(routed, errors.New("public_memory_search is available only for public interactions"))
		}
	case SocialContextSearch, SocialExpressionSelect:
		if resolved.AllowsPersonalMemory() || !resolved.AllowsAmbientParticipation() {
			return rejected(routed, errors.New(call.Name+" is available only for public ambient interactions"))
		}
	case WebSearch:
		if !availability.WebSearch {
			return resultFailure(routed, "disabled", errors.New("web search is disabled"))
		}
	case StickerSearch:
		if !availability.Sticker {
			return rejected(routed, errors.New("sticker_search is unavailable for this session"))
		}
	default:
		return resultFailure(routed, "not_whitelisted", errors.New("tool is not whitelisted"))
	}
	return routed
}

func validateDesktopArguments(arguments string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &value); err != nil {
		return errors.New("desktop observation arguments must be an empty JSON object")
	}
	if len(value) != 0 {
		return errors.New("desktop observation arguments must be empty")
	}
	return nil
}

func resultFailure(routed RoutedCall, status string, err error) RoutedCall {
	routed.Disposition = CallResult
	routed.Status = status
	routed.Err = err
	return routed
}

func rejected(routed RoutedCall, err error) RoutedCall {
	routed.Disposition = CallReject
	routed.Status = "rejected"
	routed.Err = err
	return routed
}
