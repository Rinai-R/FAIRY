package observability

const DefaultMetricResponseLimit = 60

// MetricHistoryPoint is the public metric snapshot persisted by Core and
// returned by the observability API.
type MetricHistoryPoint struct {
	TimestampUnixMS                   int64   `json:"timestampUnixMs"`
	ProcessStartedUnixMS              int64   `json:"processStartedAtUnixMs"`
	HTTPScope                         string  `json:"httpScope"`
	HTTPTotal                         uint64  `json:"httpTotal"`
	HTTPInFlight                      uint64  `json:"httpInFlight"`
	HTTPStatus4xx                     uint64  `json:"httpStatus4xx"`
	HTTPStatus5xx                     uint64  `json:"httpStatus5xx"`
	MessagesReceived                  uint64  `json:"messagesReceived"`
	MessagesSent                      uint64  `json:"messagesSent"`
	MessagesActive                    uint64  `json:"messagesActive"`
	MessagesFailed                    uint64  `json:"messagesFailed"`
	InputTokens                       uint64  `json:"inputTokens"`
	CachedInputTokens                 uint64  `json:"cachedInputTokens"`
	OutputTokens                      uint64  `json:"outputTokens"`
	ModelCalls                        uint64  `json:"modelCalls"`
	LearningEnqueued                  uint64  `json:"learningEnqueued"`
	LearningSucceeded                 uint64  `json:"learningSucceeded"`
	LearningFailed                    uint64  `json:"learningFailed"`
	LearningDropped                   uint64  `json:"learningDropped"`
	LearningModelCalls                uint64  `json:"learningModelCalls"`
	LearningInputTokens               uint64  `json:"learningInputTokens"`
	LearningCachedObservedInputTokens uint64  `json:"learningCachedObservedInputTokens"`
	LearningCachedInputTokens         uint64  `json:"learningCachedInputTokens"`
	LearningCacheWriteTokens          uint64  `json:"learningCacheWriteTokens"`
	LearningOutputTokens              uint64  `json:"learningOutputTokens"`
	FeedbackRegistered                uint64  `json:"feedbackRegistered"`
	FeedbackSuperseded                uint64  `json:"feedbackSuperseded"`
	FeedbackSucceeded                 uint64  `json:"feedbackSucceeded"`
	FeedbackFailed                    uint64  `json:"feedbackFailed"`
	FeedbackDropped                   uint64  `json:"feedbackDropped"`
	FeedbackModelCalls                uint64  `json:"feedbackModelCalls"`
	FeedbackInputTokens               uint64  `json:"feedbackInputTokens"`
	FeedbackCachedObservedInputTokens uint64  `json:"feedbackCachedObservedInputTokens"`
	FeedbackCachedInputTokens         uint64  `json:"feedbackCachedInputTokens"`
	FeedbackCacheWriteTokens          uint64  `json:"feedbackCacheWriteTokens"`
	FeedbackOutputTokens              uint64  `json:"feedbackOutputTokens"`
	CompactionL1Applied               uint64  `json:"compactionL1Applied"`
	CompactionL2Applied               uint64  `json:"compactionL2Applied"`
	CompactionL3Applied               uint64  `json:"compactionL3Applied"`
	CompactionFailed                  uint64  `json:"compactionFailed"`
	Goroutines                        uint64  `json:"goroutines"`
	BackgroundJobs                    uint64  `json:"backgroundJobs"`
	EventSubscribers                  uint64  `json:"eventSubscribers"`
	LogSubscribers                    uint64  `json:"logSubscribers"`
	HeapMiB                           float64 `json:"heapMiB"`
}

// HistoryStats exposes the bounded persistence queue health without exposing
// the concrete database-backed Store.
type HistoryStats struct {
	Queued        uint64 `json:"queued"`
	QueueDropped  uint64 `json:"queueDropped"`
	WriteFailed   uint64 `json:"writeFailed"`
	CleanupFailed uint64 `json:"cleanupFailed"`
}
