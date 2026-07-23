package coreclient

import (
	"encoding/json"
	"time"

	"fairy/interaction"
	"fairy/observability"
)

type Status struct {
	Bootstrap            json.RawMessage  `json:"bootstrap"`
	ConfigRoot           string           `json:"configRoot"`
	WebSearch            json.RawMessage  `json:"webSearch"`
	SemanticEmbedding    json.RawMessage  `json:"semanticEmbedding"`
	ActiveBackgroundJobs int64            `json:"activeBackgroundJobs"`
	Model                json.RawMessage  `json:"model,omitempty"`
	ModelError           string           `json:"modelError,omitempty"`
	Speech               json.RawMessage  `json:"speech,omitempty"`
	SpeechError          string           `json:"speechError,omitempty"`
	Database             DependencyStatus `json:"database"`
	Qdrant               DependencyStatus `json:"qdrant"`
	SecretKey            DependencyStatus `json:"secretKey"`
}

type DependencyStatus struct {
	Ready bool   `json:"ready"`
	Mode  string `json:"mode"`
	Error string `json:"error,omitempty"`
}

type OpenSessionRequest struct {
	Endpoint    interaction.EndpointKind `json:"endpoint"`
	EndpointKey string                   `json:"endpointKey"`
	Interaction interaction.Context      `json:"interaction"`
}

type OpenSessionResponse struct {
	ConversationID string                   `json:"conversationId"`
	CharacterID    string                   `json:"characterId"`
	MessageCount   int                      `json:"messageCount"`
	Endpoint       interaction.EndpointKind `json:"endpoint"`
}

type MessageRecord struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	TurnID          string `json:"turnId"`
	Sequence        uint64 `json:"sequence"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type MessagePage struct {
	Messages           []MessageRecord `json:"messages"`
	NextBeforeSequence *uint64         `json:"nextBeforeSequence,omitempty"`
}

type SubmitTurnRequest struct {
	Input         string `json:"input"`
	SpeechEnabled bool   `json:"speechEnabled"`
}

type TurnOutcome struct {
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
	ResponseText   string `json:"responseText"`
}

type SubmitTurnResponse struct {
	Outcome TurnOutcome `json:"outcome"`
}

type AmbientObservation struct {
	MessageID       string `json:"messageId"`
	SenderID        string `json:"senderId"`
	SenderName      string `json:"senderName"`
	Text            string `json:"text"`
	DirectedToBot   bool   `json:"directedToBot"`
	IsNew           bool   `json:"isNew"`
	TimestampUnixMS int64  `json:"timestampUnixMs"`
}

type ParticipationRequest struct {
	EvaluationReason string               `json:"evaluationReason"`
	Messages         []AmbientObservation `json:"messages"`
}

type ParticipationResponse struct {
	Action          string  `json:"action"`
	TargetMessageID *string `json:"targetMessageId,omitempty"`
	WaitSeconds     *int    `json:"waitSeconds,omitempty"`
}

type TurnEvent struct {
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	Sequence       uint64          `json:"sequence"`
	State          string          `json:"state"`
	Payload        json.RawMessage `json:"payload"`
}

type ParticipationEvent struct {
	ConversationID   string           `json:"conversationId"`
	Generation       uint64           `json:"generation"`
	EvaluationReason string           `json:"evaluationReason"`
	Action           string           `json:"action"`
	TargetMessageID  string           `json:"targetMessageId,omitempty"`
	WaitSeconds      int              `json:"waitSeconds,omitempty"`
	Usage            []LaneModelUsage `json:"usage,omitempty"`
	ObservedAt       time.Time        `json:"observedAt"`
}

type CachedTokenObservation struct {
	Status string  `json:"status"`
	Tokens *uint64 `json:"tokens,omitempty"`
}

type LaneUsage struct {
	InputTokens       *uint64                `json:"inputTokens"`
	OutputTokens      *uint64                `json:"outputTokens"`
	CachedInputTokens CachedTokenObservation `json:"cachedInputTokens"`
	CacheWriteTokens  CachedTokenObservation `json:"cacheWriteTokens"`
}

type LaneModelUsage struct {
	Lane          string    `json:"lane"`
	HistoryWindow uint64    `json:"historyWindow"`
	Usage         LaneUsage `json:"usage"`
}

type CharacterRecord struct {
	CharacterID string              `json:"characterId"`
	Revision    uint64              `json:"revision"`
	Name        string              `json:"name"`
	Appearance  CharacterAppearance `json:"appearance"`
}

type CharacterAppearance struct {
	Status string          `json:"status"`
	Visual *VisualManifest `json:"visual,omitempty"`
}

type VisualManifest struct {
	SchemaVersion uint64        `json:"schemaVersion"`
	PackID        string        `json:"packId"`
	DisplayName   string        `json:"displayName"`
	Renderer      string        `json:"renderer"`
	Frame         VisualFrame   `json:"frame"`
	Scale         float64       `json:"scale"`
	Anchor        VisualAnchor  `json:"anchor"`
	States        []VisualState `json:"states"`
}

type VisualFrame struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type VisualAnchor struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type VisualState struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	ImagePath   string `json:"imagePath"`
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
	ActiveBackgroundJobs uint64           `json:"activeBackgroundJobs"`
	EventSubscribers     uint64           `json:"eventSubscribers"`
	AgentLoop            AgentLoopMetrics `json:"agentLoop"`
}

type LatencyMetrics struct {
	Observations    uint64 `json:"observations"`
	TotalDurationMS uint64 `json:"totalDurationMs"`
	MaxDurationMS   uint64 `json:"maxDurationMs"`
}

type AgentLoopMetrics struct {
	ProviderFirstByte LatencyMetrics `json:"providerFirstByte"`
	ReplyPreview      LatencyMetrics `json:"replyPreview"`
	FirstBeat         LatencyMetrics `json:"firstBeat"`
	Completed         LatencyMetrics `json:"completed"`
}

type Metrics struct {
	GeneratedAtUnixMS int64                                `json:"generatedAtUnixMs"`
	Process           observability.ProcessMetrics         `json:"process"`
	HTTP              observability.HTTPMetricsSnapshot    `json:"http"`
	Logs              observability.LogStats               `json:"logs"`
	Messages          observability.MessageMetricsSnapshot `json:"messages"`
	Runtime           RuntimeMetrics                       `json:"runtime"`
	Usage             UsageReport                          `json:"usage"`
	Database          json.RawMessage                      `json:"database"`
	Qdrant            json.RawMessage                      `json:"qdrant"`
}

type LogQuery struct {
	Level         string
	LoggerPrefix  string
	AfterSequence uint64
	Limit         int
}

type LogResponse = observability.LogSnapshot
