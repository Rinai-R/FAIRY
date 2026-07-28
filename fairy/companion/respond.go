package companion

import (
	"errors"
	"strings"

	"fairy/reply"
	"fairy/session"
)

type (
	VisualState            = reply.VisualState
	ReplyChain             = reply.ReplyChain
	CompiledReply          = reply.CompiledReply
	SpeechSynthesisRequest = reply.SpeechSynthesisRequest
	SpeechSynthesisResult  = reply.SpeechSynthesisResult
	SpeechSynthesizer      = reply.SpeechSynthesizer
	BeatReadyCompletion    = reply.BeatReadyCompletion
)

func validateAvailableVisualStates(states []VisualState) error {
	return reply.ValidateAvailableVisualStates(states)
}

func compiledReplyFromChains(chains []ReplyChain) (CompiledReply, error) {
	return reply.CompiledReplyFromChains(chains)
}

func fillSameLanguageSpeech(compiled CompiledReply) (CompiledReply, error) {
	return reply.FillSameLanguageSpeech(compiled)
}

func sanitizeDisplayText(value string) string { return reply.SanitizeDisplayText(value) }
func sanitizeSpeechText(value string) string  { return reply.SanitizeSpeechText(value) }
func validateSpeech(value string) error       { return reply.ValidateSpeech(value) }

func speechExceedsSoftLimit(value string) bool {
	return reply.SpeechExceedsSoftLimit(value)
}

var ErrTurnRuntimeUnavailable = errors.New("companion turn runtime is unavailable")

type SubmitTurnRequest struct {
	ConversationID      string                     `json:"conversationId"`
	Input               string                     `json:"input"`
	SpeechEnabled       bool                       `json:"speechEnabled"`
	TraceID             string                     `json:"-"`
	MessageSource       string                     `json:"-"`
	ReplyIntent         *ReplyIntent               `json:"-"`
	RecentTargetReply   string                     `json:"-"`
	PersonNoteSenderIDs []string                   `json:"-"`
	OutputCapabilities  session.OutputCapabilities `json:"-"`
}

type SubmitCompiledTurnRequest struct {
	ConversationID        string                     `json:"conversationId"`
	Input                 string                     `json:"input"`
	SpeechEnabled         bool                       `json:"speechEnabled"`
	MaxOutputTokens       uint32                     `json:"maxOutputTokens"`
	AvailableVisualStates []VisualState              `json:"availableVisualStates"`
	TraceID               string                     `json:"-"`
	MessageSource         string                     `json:"-"`
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
	SpeechEnabled          bool     `json:"speechEnabled"`
}

type DesktopVisionInitiationRequest struct {
	ConversationID string `json:"conversationId"`
	SpeechEnabled  bool   `json:"speechEnabled"`
}

type TurnOutcome struct {
	ConversationID   string       `json:"conversationId"`
	TurnID           string       `json:"turnId"`
	ResponseText     string       `json:"responseText"`
	SpeechText       string       `json:"speechText"`
	SpeechRequested  bool         `json:"speechRequested"`
	VisualState      string       `json:"visualState"`
	Chains           []ReplyChain `json:"chains"`
	RespondMigrated  bool         `json:"respondMigrated"`
	MigrationMessage string       `json:"migrationMessage"`
}

func ValidateSubmitTurnRequest(request SubmitTurnRequest) error {
	if strings.TrimSpace(request.ConversationID) == "" {
		return errors.New("conversation_id is required")
	}
	if strings.TrimSpace(request.Input) == "" {
		return errors.New("companion input is required")
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
	return validateAvailableVisualStates(request.AvailableVisualStates)
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
