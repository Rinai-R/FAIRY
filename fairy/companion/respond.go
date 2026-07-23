package companion

import (
	"errors"
	"fmt"
	"strings"
)

var ErrRespondRuntimeNotMigrated = errors.New("companion respond runtime is not migrated to Go")

type ReplyChain struct {
	Text        string `json:"text"`
	SpeechText  string `json:"speechText"`
	VisualState string `json:"visualState"`
}

type SubmitTurnRequest struct {
	ConversationID      string       `json:"conversationId"`
	Input               string       `json:"input"`
	SpeechEnabled       bool         `json:"speechEnabled"`
	TraceID             string       `json:"-"`
	MessageSource       string       `json:"-"`
	ReplyIntent         *ReplyIntent `json:"-"`
	RecentTargetReply   string       `json:"-"`
	PersonNoteSenderIDs []string     `json:"-"`
}

type SubmitCompiledTurnRequest struct {
	ConversationID        string        `json:"conversationId"`
	Input                 string        `json:"input"`
	SpeechEnabled         bool          `json:"speechEnabled"`
	MaxOutputTokens       uint32        `json:"maxOutputTokens"`
	AvailableVisualStates []VisualState `json:"availableVisualStates"`
	TraceID               string        `json:"-"`
	MessageSource         string        `json:"-"`
	ReplyIntent           *ReplyIntent  `json:"-"`
	RecentTargetReply     string        `json:"-"`
	PersonNoteSenderIDs   []string      `json:"-"`
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

type SpeechSynthesisRequest struct {
	Text      string
	SpeakerID string
}

type SpeechSynthesisResult struct {
	SpeakerID string
	MimeType  string
	Format    string
	DataURL   string
}

type SpeechSynthesizer interface {
	SynthesizeSpeech(request SpeechSynthesisRequest) (SpeechSynthesisResult, error)
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

func ValidateSubmitCompiledTurnRequest(request SubmitCompiledTurnRequest) error {
	if strings.TrimSpace(request.ConversationID) == "" {
		return errors.New("conversation_id is required")
	}
	if strings.TrimSpace(request.Input) == "" {
		return errors.New("companion input is required")
	}
	if request.MaxOutputTokens == 0 {
		return errors.New("max_output_tokens is required")
	}
	return validateAvailableVisualStates(request.AvailableVisualStates)
}

func ValidateReplyChains(chains []ReplyChain) error {
	if len(chains) == 0 {
		return errors.New("reply chains must contain at least one chain")
	}
	if len(chains) > maxReplyChains {
		return fmt.Errorf("reply chains must contain at most %d chains", maxReplyChains)
	}
	for i, chain := range chains {
		if strings.TrimSpace(chain.Text) == "" {
			return fmt.Errorf("reply chain %d text is required", i)
		}
		if strings.TrimSpace(chain.VisualState) == "" {
			return fmt.Errorf("reply chain %d visual_state is required", i)
		}
	}
	return nil
}
