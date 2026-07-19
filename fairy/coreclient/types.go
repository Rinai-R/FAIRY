package coreclient

import (
	"encoding/json"

	"fairy/observability"
)

type Status struct {
	Bootstrap            json.RawMessage `json:"bootstrap"`
	ConfigRoot           string          `json:"configRoot"`
	WebSearch            json.RawMessage `json:"webSearch"`
	SemanticEmbedding    json.RawMessage `json:"semanticEmbedding"`
	ActiveBackgroundJobs int64           `json:"activeBackgroundJobs"`
	Model                json.RawMessage `json:"model,omitempty"`
	ModelError           string          `json:"modelError,omitempty"`
	Speech               json.RawMessage `json:"speech,omitempty"`
	SpeechError          string          `json:"speechError,omitempty"`
}

type OpenSessionRequest struct {
	Surface string `json:"surface,omitempty"`
}

type OpenSessionResponse struct {
	ConversationID string `json:"conversationId"`
	CharacterID    string `json:"characterId"`
	MessageCount   int    `json:"messageCount"`
	Surface        string `json:"surface"`
}

type SubmitTurnRequest struct {
	Input         string `json:"input"`
	SpeechEnabled bool   `json:"speechEnabled"`
	Surface       string `json:"surface,omitempty"`
}

type TurnOutcome struct {
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
	ResponseText   string `json:"responseText"`
}

type SubmitTurnResponse struct {
	Outcome TurnOutcome `json:"outcome"`
	Surface string      `json:"surface"`
}

type HarnessEvent struct {
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	Sequence       uint64          `json:"sequence"`
	State          string          `json:"state"`
	Payload        json.RawMessage `json:"payload"`
}

type CharacterRecord struct {
	CharacterID string `json:"characterId"`
	Revision    uint64 `json:"revision"`
	Name        string `json:"name"`
}

type CharacterCatalog struct {
	Characters []CharacterRecord `json:"characters"`
	Active     *CharacterRecord  `json:"active"`
}

type UsageReport struct {
	Overall   json.RawMessage `json:"overall"`
	Turns     json.RawMessage `json:"turns"`
	TurnCount uint64          `json:"turnCount"`
	Truncated bool            `json:"truncated"`
}

type RuntimeMetrics struct {
	ActiveBackgroundJobs uint64 `json:"activeBackgroundJobs"`
	EventSubscribers     uint64 `json:"eventSubscribers"`
}

type Metrics struct {
	GeneratedAtUnixMS int64                             `json:"generatedAtUnixMs"`
	Process           observability.ProcessMetrics      `json:"process"`
	HTTP              observability.HTTPMetricsSnapshot `json:"http"`
	Logs              observability.LogStats            `json:"logs"`
	Runtime           RuntimeMetrics                    `json:"runtime"`
	Usage             UsageReport                       `json:"usage"`
}

type LogQuery struct {
	Level         string
	LoggerPrefix  string
	AfterSequence uint64
	Limit         int
}

type LogResponse = observability.LogSnapshot
