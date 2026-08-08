package web

import (
	"context"
	"errors"
	"time"

	"fairy/agent/sticker"
	"fairy/context/character"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/identity"
	"fairy/context/knowledge"
	memoryadmin "fairy/context/memory/admin"
	"fairy/context/memory/personal"
	"fairy/runtime/config"
	coredb "fairy/runtime/database"
	"fairy/runtime/ledger"
	"fairy/runtime/model"
	"fairy/runtime/observability"
	"fairy/transport/desktopcapture"
	"fairy/transport/session"

	"go.uber.org/zap"
)

var (
	ErrEventSubscriberOverflow         = errors.New("event subscriber overflow")
	ErrParticipationSubscriberOverflow = errors.New("participation subscriber overflow")
	ErrEventSubscriberCapacity         = errors.New("event subscriber capacity reached")
	ErrParticipationSubscriberCapacity = errors.New("participation subscriber capacity reached")
)

type EventSubscription struct {
	Events   <-chan session.Event
	Failures <-chan error
	Cancel   func()
}

func (s EventSubscription) Unsubscribe() {
	if s.Cancel != nil {
		s.Cancel()
	}
}

type ParticipationSubscription struct {
	Events   <-chan ParticipationEvent
	Failures <-chan error
	Cancel   func()
}

// TurnRuntime is the transport layer's consumption-side view of reactive
// conversation orchestration. Core adapts the concrete turn service to it.
type TurnRuntime interface {
	OutputCapabilities(string) session.OutputCapabilities
	ReportExpressionDelivery(session.ExpressionDeliveryResult) error
	BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error
	UnbindOutputCapabilities(ownerID, conversationID string)
	SubmitTurn(TurnSubmission) (any, error)
	CancelTurn(conversationID, turnID string) error
	BindInteraction(conversationID string, binding session.Binding) error
	ActiveBackgroundJobs() int64
	AgentLoopMetrics() AgentLoopMetrics
}

type TurnSubmission struct {
	ConversationID string
	Input          string
	MessageID      string
}

// InitiativeRuntime is the Web-facing port for the Presence domain.
// Transport DTOs stay in api/session; initiative-owned control data does not
// cross this boundary.
type InitiativeRuntime interface {
	ObserveAmbient(conversationID string, observation session.AmbientObservation) error
	ObserveDesktop(conversationID string, observation session.DesktopObservation) (DesktopObservationResult, error)
	DecideParticipation(context.Context, string, session.ParticipationRequest) (session.ParticipationResponse, error)
	ExperienceStats() ExperienceStats
}

type ParticipationEvent struct {
	ConversationID   string                 `json:"conversationId"`
	Generation       uint64                 `json:"generation"`
	EvaluationReason string                 `json:"evaluationReason"`
	Action           string                 `json:"action"`
	TargetMessageID  string                 `json:"targetMessageId,omitempty"`
	WaitSeconds      int                    `json:"waitSeconds,omitempty"`
	Usage            []model.LaneModelUsage `json:"usage,omitempty"`
	ObservedAt       time.Time              `json:"observedAt"`
}

type DesktopObservationStep struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Depends  []string `json:"dependsOn,omitempty"`
	OmitCode string   `json:"omitCode,omitempty"`
}

type DesktopObservationDiagnostic struct {
	Node   string `json:"node"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type DesktopObservationResult struct {
	Nodes       []DesktopObservationStep       `json:"nodes"`
	Action      string                         `json:"action"`
	OmitReasons []string                       `json:"omitReasons,omitempty"`
	Diagnostics []DesktopObservationDiagnostic `json:"diagnostics,omitempty"`
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

type LearningQueueStats struct {
	Enqueued  int64 `json:"enqueued"`
	Dropped   int64 `json:"dropped"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
}

type FeedbackQueueStats struct {
	Registered int64 `json:"registered"`
	Dropped    int64 `json:"dropped"`
	Succeeded  int64 `json:"succeeded"`
	Failed     int64 `json:"failed"`
}

type ExperienceStats struct {
	Learning             LearningQueueStats `json:"learning"`
	Feedback             FeedbackQueueStats `json:"feedback"`
	CacheIdentityVersion string             `json:"cacheIdentityVersion"`
}

// ObservabilityHistory is the Web layer's read/write view of persisted
// observability projections. The concrete PostgreSQL lifecycle remains owned
// by the Core composition root.
type ObservabilityHistory interface {
	RecentLogs(context.Context, int) ([]observability.LogEntry, error)
	RecentTraces(context.Context, int) ([]observability.MessageTraceDetail, error)
	Trace(context.Context, string) (observability.MessageTraceDetail, bool, error)
	TracesByMessageID(context.Context, string, int) ([]observability.MessageTraceDetail, error)
	RecentMetrics(context.Context, int) ([]observability.MetricHistoryPoint, error)
	EnqueueMetric(observability.MetricHistoryPoint) bool
	Stats() observability.HistoryStats
}

func (s ParticipationSubscription) Unsubscribe() {
	if s.Cancel != nil {
		s.Cancel()
	}
}

// Dependencies is API's consumption-side view of the Core composition root.
// API owns no construction or shutdown of these process-scoped services.
type Dependencies struct {
	ConfigRoot string
	Logger     *zap.Logger
	StartedAt  time.Time

	Database           *coredb.Pool
	TranscriptStore    *history.Store
	RuntimeStore       *historyruntime.Store
	KnowledgeStore     *knowledge.Store
	MemoryStore        *personal.Store
	ObservabilityStore *ledger.Store
	Identity           *identity.Store
	Memory             *memoryadmin.Service
	Secret             *config.SecretStore
	Turns              TurnRuntime
	Initiative         InitiativeRuntime
	Character          *character.CharacterService
	Config             *config.ConfigService
	Profile            *config.ProfileService
	Stickers           *sticker.Store
	Captures           *desktopcapture.CaptureHub
	Logs               *observability.LogStore
	HTTPMetrics        *observability.HTTPMetrics
	Messages           *observability.MessageMetrics
	History            ObservabilityHistory

	BootstrapStatus          func() (any, error)
	SubscribeTurnEvents      func(conversationID string) (EventSubscription, error)
	SubscribeParticipation   func(conversationID string) (ParticipationSubscription, error)
	TurnEventSubscriberCount func() uint64
}
