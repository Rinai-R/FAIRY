package conversation

import (
	"errors"
	"strings"

	"fairy/agent/reply"
	"fairy/runtime/observability"
	"fairy/transport/session"
)

var ErrTurnRuntimeUnavailable = errors.New("companion turn runtime is unavailable")

type SubmitTurnRequest struct {
	ConversationID       string                     `json:"conversationId"`
	Input                string                     `json:"input"`
	TraceID              string                     `json:"-"`
	MessageID            string                     `json:"-"`
	MessageSource        string                     `json:"-"`
	ReplyTargetMessageID string                     `json:"-"`
	ReplyIntent          *ReplyIntent               `json:"-"`
	RecentTargetReply    string                     `json:"-"`
	PersonNoteSenderIDs  []string                   `json:"-"`
	OutputCapabilities   session.OutputCapabilities `json:"-"`
}

type SubmitCompiledTurnRequest struct {
	ConversationID        string                     `json:"conversationId"`
	Input                 string                     `json:"input"`
	MaxOutputTokens       uint32                     `json:"maxOutputTokens"`
	AvailableVisualStates []reply.VisualState        `json:"availableVisualStates"`
	TraceID               string                     `json:"-"`
	MessageID             string                     `json:"-"`
	MessageSource         string                     `json:"-"`
	ReplyTargetMessageID  string                     `json:"-"`
	ReplyIntent           *ReplyIntent               `json:"-"`
	RecentTargetReply     string                     `json:"-"`
	PersonNoteSenderIDs   []string                   `json:"-"`
	Initiation            *DesktopInitiationContext  `json:"-"`
	OutputCapabilities    session.OutputCapabilities `json:"-"`
}

// ReplyIntent is ephemeral Companion control data supplied by an initiating
// workflow. It is never serialized to a Surface or persisted in transcript.
type ReplyIntent struct {
	ReplyAct           string
	Tone               string
	RelationshipSignal string
	ReplyMode          string
	Focus              string
	Avoid              []string
	ReferenceInfo      string
	MemoryQuery        string
	ExpressionQuery    string
	DriftLevel         string
	AnchorPolicy       string
}

type DesktopInitiationContext struct {
	ObservationEvidenceIDs []string
	Trigger                string
	Activity               string
	Lifecycle              string
	VisionRequested        bool
}

// DesktopInitiationRequest is Core-owned. It intentionally carries no user
// dialogue text: the eventual persistence path must not fabricate a user turn.
type DesktopInitiationRequest struct {
	ConversationID         string   `json:"conversationId"`
	ObservationEvidenceIDs []string `json:"-"`
}

type DesktopVisionInitiationRequest struct {
	ConversationID string `json:"conversationId"`
}

type TurnOutcome struct {
	ConversationID   string             `json:"conversationId"`
	TurnID           string             `json:"turnId"`
	ResponseText     string             `json:"responseText"`
	VisualState      string             `json:"visualState"`
	Chains           []reply.ReplyChain `json:"chains"`
	RespondMigrated  bool               `json:"respondMigrated"`
	MigrationMessage string             `json:"migrationMessage"`
}

func ValidateSubmitTurnRequest(request SubmitTurnRequest) error {
	if strings.TrimSpace(request.ConversationID) == "" {
		return errors.New("conversation_id is required")
	}
	if strings.TrimSpace(request.Input) == "" {
		return errors.New("companion input is required")
	}
	if err := validateOptionalCorrelationID("message_id", request.MessageID); err != nil {
		return err
	}
	return validateOptionalCorrelationID("reply_target_message_id", request.ReplyTargetMessageID)
}

func validateOptionalMessageID(value string) error {
	return validateOptionalCorrelationID("message_id", value)
}

func validateOptionalCorrelationID(name, value string) error {
	if value == "" {
		return nil
	}
	if !observability.ValidCorrelationID(value) {
		return errors.New(name + " is invalid")
	}
	return nil
}

func validateDesktopInitiationContext(context DesktopInitiationContext) error {
	if strings.TrimSpace(context.Trigger) == "" {
		return errors.New("desktop initiation trigger is required")
	}
	if len(context.ObservationEvidenceIDs) == 0 || len(context.ObservationEvidenceIDs) > 8 {
		return errors.New("desktop initiation evidence count is invalid")
	}
	for _, id := range context.ObservationEvidenceIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("desktop initiation evidence id is invalid")
		}
	}
	return nil
}

func ValidateSubmitCompiledTurnRequest(request SubmitCompiledTurnRequest) error {
	if strings.TrimSpace(request.ConversationID) == "" {
		return errors.New("conversation_id is required")
	}
	hasInput := strings.TrimSpace(request.Input) != ""
	hasInitiation := request.Initiation != nil
	if hasInput == hasInitiation {
		return errors.New("compiled turn requires exactly one of input or initiation context")
	}
	if hasInitiation {
		if err := validateDesktopInitiationContext(*request.Initiation); err != nil {
			return err
		}
	}
	if request.MaxOutputTokens == 0 {
		return errors.New("max_output_tokens is required")
	}
	if err := validateOptionalMessageID(request.MessageID); err != nil {
		return err
	}
	if err := validateOptionalCorrelationID("reply_target_message_id", request.ReplyTargetMessageID); err != nil {
		return err
	}
	return reply.ValidateAvailableVisualStates(request.AvailableVisualStates)
}

func ValidateDesktopInitiationRequest(request DesktopInitiationRequest) error {
	if strings.TrimSpace(request.ConversationID) == "" {
		return errors.New("conversation_id is required")
	}
	if len(request.ObservationEvidenceIDs) == 0 || len(request.ObservationEvidenceIDs) > 8 {
		return errors.New("desktop initiation evidence count is invalid")
	}
	for _, id := range request.ObservationEvidenceIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("desktop initiation evidence id is invalid")
		}
	}
	return nil
}
