package session

import (
	"encoding/json"
	"time"

	"fairy/runtime/observability"
)

type Status struct {
	Bootstrap            json.RawMessage  `json:"bootstrap"`
	ConfigRoot           string           `json:"configRoot"`
	WebSearch            json.RawMessage  `json:"webSearch"`
	SemanticEmbedding    json.RawMessage  `json:"semanticEmbedding"`
	ActiveBackgroundJobs int64            `json:"activeBackgroundJobs"`
	Model                json.RawMessage  `json:"model,omitempty"`
	ModelError           string           `json:"modelError,omitempty"`
	Database             DependencyStatus `json:"database"`
	SecretKey            DependencyStatus `json:"secretKey"`
}

type DependencyStatus struct {
	Ready bool   `json:"ready"`
	Mode  string `json:"mode"`
	Error string `json:"error,omitempty"`
}

type (
	OpenSessionRequest  = OpenRequest
	OpenSessionResponse = OpenResponse
	SubmitTurnRequest   = SubmitRequest
	TurnOutcome         = Outcome
	SubmitTurnResponse  = SubmitResponse
	TurnEvent           = Event
)

type DesktopObservationResponse struct {
	Action      string                   `json:"action"`
	Nodes       []DesktopObservationStep `json:"nodes"`
	OmitReasons []string                 `json:"omitReasons,omitempty"`
}

// DesktopObservationStep preserves the existing Session response shape. It is
// diagnostic projection data, not an executable graph node.
type DesktopObservationStep struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Depends  []string `json:"dependsOn,omitempty"`
	OmitCode string   `json:"omitCode,omitempty"`
}

// Keep the public interaction kinds reachable beside the typed client contracts.
var (
	_ = EndpointDesktop
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

type StickerRecord struct {
	ID              string   `json:"id"`
	ContentSHA256   string   `json:"contentSha256"`
	MIMEType        string   `json:"mimeType"`
	ByteCount       int64    `json:"byteCount"`
	Description     string   `json:"description"`
	Tags            []string `json:"tags"`
	Status          string   `json:"status"`
	CreatedAtUnixMS int64    `json:"createdAtUnixMs"`
	UpdatedAtUnixMS int64    `json:"updatedAtUnixMs"`
}

type StickerPage struct {
	Items      []StickerRecord `json:"items"`
	Offset     int             `json:"offset"`
	Limit      int             `json:"limit"`
	Total      int64           `json:"total"`
	NextOffset *int            `json:"nextOffset,omitempty"`
}

type StickerContent struct {
	MIMEType      string
	ContentSHA256 string
	Bytes         []byte
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
	ProviderFirstByte LatencyMetrics    `json:"providerFirstByte"`
	ReplyPreview      LatencyMetrics    `json:"replyPreview"`
	FirstBeat         LatencyMetrics    `json:"firstBeat"`
	Completed         LatencyMetrics    `json:"completed"`
	Compaction        CompactionMetrics `json:"compaction"`
}

type CompactionMetrics struct {
	L1Applied uint64 `json:"l1Applied"`
	L2Applied uint64 `json:"l2Applied"`
	L3Applied uint64 `json:"l3Applied"`
	Failed    uint64 `json:"failed"`
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
}

type LogQuery struct {
	Level         string
	LoggerPrefix  string
	AfterSequence uint64
	Limit         int
}

type LogResponse = observability.LogSnapshot
