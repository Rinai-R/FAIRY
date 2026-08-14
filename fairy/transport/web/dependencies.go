package web

import (
	"context"
	"time"

	"fairy/agent/sticker"
	appsession "fairy/app/session"
	"fairy/context/character"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/identity"
	"fairy/context/knowledge"
	memoryadmin "fairy/context/memory/admin"
	"fairy/context/memory/personal"
	"fairy/runtime/config"
	"fairy/runtime/ledger"
	"fairy/runtime/observability"
	"fairy/transport/desktopcapture"
	"fairy/transport/session"

	"go.uber.org/zap"
)

var (
	ErrEventSubscriberOverflow         = appsession.ErrEventSubscriberOverflow
	ErrParticipationSubscriberOverflow = appsession.ErrParticipationSubscriberOverflow
	ErrEventSubscriberCapacity         = appsession.ErrEventSubscriberCapacity
	ErrParticipationSubscriberCapacity = appsession.ErrParticipationSubscriberCapacity
)

type (
	EventSubscription            = appsession.EventSubscription
	ParticipationSubscription    = appsession.ParticipationSubscription
	TurnSubmission               = appsession.TurnSubmission
	ParticipationEvent           = session.ParticipationEvent
	DesktopObservationStep       = appsession.DesktopObservationStep
	DesktopObservationDiagnostic = appsession.DesktopObservationDiagnostic
	DesktopObservationResult     = appsession.DesktopObservationResult
)

// TurnRuntime is the transport layer's consumption-side view of reactive
// conversation orchestration. Core adapts the concrete turn service to it.
type TurnRuntime interface {
	appsession.TurnRuntime
	ActiveBackgroundJobs() int64
	AgentLoopMetrics() AgentLoopMetrics
}

// InitiativeRuntime is the Web-facing port for the Presence domain.
// Transport DTOs stay in api/session; initiative-owned control data does not
// cross this boundary.
type InitiativeRuntime interface {
	appsession.InitiativeRuntime
	ExperienceStats() ExperienceStats
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
	Enqueued                  int64 `json:"enqueued"`
	Dropped                   int64 `json:"dropped"`
	Succeeded                 int64 `json:"succeeded"`
	Failed                    int64 `json:"failed"`
	ModelCalls                int64 `json:"modelCalls"`
	InputTokens               int64 `json:"inputTokens"`
	CachedObservedInputTokens int64 `json:"cachedObservedInputTokens"`
	CachedInputTokens         int64 `json:"cachedInputTokens"`
	CacheWriteTokens          int64 `json:"cacheWriteTokens"`
	OutputTokens              int64 `json:"outputTokens"`
}

type FeedbackQueueStats struct {
	Registered                int64 `json:"registered"`
	Superseded                int64 `json:"superseded"`
	Dropped                   int64 `json:"dropped"`
	Succeeded                 int64 `json:"succeeded"`
	Failed                    int64 `json:"failed"`
	ModelCalls                int64 `json:"modelCalls"`
	InputTokens               int64 `json:"inputTokens"`
	CachedObservedInputTokens int64 `json:"cachedObservedInputTokens"`
	CachedInputTokens         int64 `json:"cachedInputTokens"`
	CacheWriteTokens          int64 `json:"cacheWriteTokens"`
	OutputTokens              int64 `json:"outputTokens"`
}

type ExperienceStats struct {
	Learning             LearningQueueStats `json:"learning"`
	Feedback             FeedbackQueueStats `json:"feedback"`
	CacheIdentityVersion string             `json:"cacheIdentityVersion"`
}

// ObservabilityHistory is the Web layer's read/write view of persisted
// observability projections. The SeekDB lifecycle remains owned by the Core
// composition root.
type ObservabilityHistory interface {
	RecentLogs(context.Context, int) ([]observability.LogEntry, error)
	RecentTraces(context.Context, int) ([]observability.MessageTraceDetail, error)
	Trace(context.Context, string) (observability.MessageTraceDetail, bool, error)
	TracesByMessageID(context.Context, string, int) ([]observability.MessageTraceDetail, error)
	RecentMetrics(context.Context, int) ([]observability.MetricHistoryPoint, error)
	EnqueueMetric(observability.MetricHistoryPoint) bool
	Stats() observability.HistoryStats
}

// Dependencies is API's consumption-side view of the Core composition root.
// API owns no construction or shutdown of these process-scoped services.
type Dependencies struct {
	ConfigRoot string
	Logger     *zap.Logger
	StartedAt  time.Time

	QueryStorageStatus func(context.Context) (StorageStatus, error)
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

// StorageStatus is the credential-free storage readiness projection for local
// status and metrics. It never includes connection strings or master-key material.
type StorageStatus struct {
	Ready           bool   `json:"ready"`
	Mode            string `json:"mode"`
	Storage         string `json:"storage,omitempty"`
	Descriptor      any    `json:"descriptor,omitempty"`
	Schema          any    `json:"schema,omitempty"`
	SecretsReady    bool   `json:"secretsReady,omitempty"`
	OpenConnections int    `json:"openConnections,omitempty"`
	Error           string `json:"error,omitempty"`
}

func newSessionService(rt *Dependencies) *appsession.Service {
	if rt == nil {
		return appsession.New(appsession.Dependencies{})
	}
	return appsession.New(appsession.Dependencies{
		Secret:                 rt.Secret,
		Characters:             rt.Character,
		Transcript:             rt.TranscriptStore,
		Turns:                  rt.Turns,
		Initiative:             rt.Initiative,
		Captures:               rt.Captures,
		SubscribeTurnEvents:    rt.SubscribeTurnEvents,
		SubscribeParticipation: rt.SubscribeParticipation,
	})
}
