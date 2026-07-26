package coreclient

import (
	"encoding/json"
	"time"

	"fairy/contracts/interaction"
	"fairy/contracts/observation"
	"fairy/contracts/session"
	"fairy/contracts/turn"
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

type (
	OpenSessionRequest    = session.OpenRequest
	OpenSessionResponse   = session.OpenResponse
	MessageRecord         = session.MessageRecord
	MessagePage           = session.MessagePage
	AmbientObservation    = session.AmbientObservation
	ParticipationRequest  = session.ParticipationRequest
	ParticipationResponse = session.ParticipationResponse
	DesktopCaptureRequest = session.DesktopCaptureRequest
	DesktopCaptureResult  = session.DesktopCaptureResult
	SubmitTurnRequest     = turn.SubmitRequest
	TurnOutcome           = turn.Outcome
	SubmitTurnResponse    = turn.SubmitResponse
	TurnEvent             = turn.Event
	DesktopObservation    = observation.DesktopObservation
)

type DesktopObservationResponse struct {
	Action      string             `json:"action"`
	Nodes       []DesktopGraphNode `json:"nodes"`
	OmitReasons []string           `json:"omitReasons,omitempty"`
}

type DesktopGraphNode struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Depends  []string `json:"dependsOn,omitempty"`
	OmitCode string   `json:"omitCode,omitempty"`
}

// Keep interaction kinds reachable through coreclient for older Surface call sites
// that construct OpenSessionRequest inline with interaction constants via this package.
var (
	_ = interaction.EndpointDesktop
)

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
