package model

type CachedTokenObservation struct {
	Status string  `json:"status"`
	Tokens *uint64 `json:"tokens,omitempty"`
}

func CacheUnsupported() CachedTokenObservation {
	return CachedTokenObservation{Status: "unsupported"}
}

func CacheMissing() CachedTokenObservation {
	return CachedTokenObservation{Status: "missing"}
}

func CacheObserved(tokens uint64) CachedTokenObservation {
	return CachedTokenObservation{Status: "observed", Tokens: &tokens}
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

func LaneUsageFromEvents(lane PromptLane, events []StreamEvent, historyWindow uint64) []LaneModelUsage {
	promptTokens, completionTokens := 0, 0
	var cachedInputTokens *uint64
	var cacheWriteTokens *uint64
	known := false
	for _, event := range events {
		if event.Type == "usage" && event.Usage != nil {
			promptTokens = event.Usage.PromptTokens
			completionTokens = event.Usage.CompletionTokens
			cachedInputTokens = event.Usage.CachedInputTokens
			cacheWriteTokens = event.Usage.CacheWriteTokens
			known = true
		}
	}
	if !known {
		return []LaneModelUsage{}
	}
	input := uint64(promptTokens)
	output := uint64(completionTokens)
	return []LaneModelUsage{{
		Lane: string(lane), HistoryWindow: historyWindow,
		Usage: LaneUsage{
			InputTokens: &input, OutputTokens: &output,
			CachedInputTokens: cacheObservationFromProvider(cachedInputTokens),
			CacheWriteTokens:  cacheObservationFromProvider(cacheWriteTokens),
		},
	}}
}

func cacheObservationFromProvider(tokens *uint64) CachedTokenObservation {
	if tokens == nil {
		return CacheMissing()
	}
	return CacheObserved(*tokens)
}
