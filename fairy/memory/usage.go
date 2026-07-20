package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	// DefaultUsageTurnLimit bounds how many recent turns the report returns in
	// detail. Global totals are always computed over every turn regardless.
	DefaultUsageTurnLimit = 100
	maxUsageTurnLimit     = 500

	usageTurnStatusUnknown = "unknown"
)

// UsageLaneAggregate sums token usage for a single prompt lane. Cache figures
// only reflect observations the provider actually returned; unobserved calls do
// not inflate hit rates.
type UsageLaneAggregate struct {
	Lane                      string `json:"lane"`
	InputTokens               uint64 `json:"inputTokens"`
	OutputTokens              uint64 `json:"outputTokens"`
	CachedInputTokens         uint64 `json:"cachedInputTokens"`
	CachedObservedInputTokens uint64 `json:"cachedObservedInputTokens"`
	CacheWriteTokens          uint64 `json:"cacheWriteTokens"`
	CallCount                 uint64 `json:"callCount"`
}

// UsageTurn is one user send (a conversation turn) with its per-lane usage.
type UsageTurn struct {
	ConversationID  string               `json:"conversationId"`
	TurnID          string               `json:"turnId"`
	CharacterID     string               `json:"characterId"`
	CreatedAtUnixMS int64                `json:"createdAtUnixMs"`
	Status          string               `json:"status"`
	Lanes           []UsageLaneAggregate `json:"lanes"`
}

// UsageReport is the read-only token usage rollup surfaced to the control panel.
type UsageReport struct {
	Overall   []UsageLaneAggregate `json:"overall"`
	Turns     []UsageTurn          `json:"turns"`
	TurnCount uint64               `json:"turnCount"`
	Truncated bool                 `json:"truncated"`
}

type ledgerCacheObservation struct {
	Status string  `json:"status"`
	Tokens *uint64 `json:"tokens"`
}

type ledgerLaneUsage struct {
	InputTokens       *uint64                `json:"inputTokens"`
	OutputTokens      *uint64                `json:"outputTokens"`
	CachedInputTokens ledgerCacheObservation `json:"cachedInputTokens"`
	CacheWriteTokens  ledgerCacheObservation `json:"cacheWriteTokens"`
}

type ledgerModelUsage struct {
	Lane  string          `json:"lane"`
	Usage ledgerLaneUsage `json:"usage"`
}

type ledgerModelMetadata struct {
	Usage []ledgerModelUsage `json:"usage"`
}

type usageTurnAccumulator struct {
	conversationID  string
	turnID          string
	createdAtUnixMS int64
	status          string
	lanes           map[string]*UsageLaneAggregate
}

type usageLedgerRow struct {
	conversationID  string
	turnID          string
	eventType       string
	state           *string
	metadataJSON    string
	createdAtUnixMS int64
}

// AggregateTokenUsage rolls up token usage across every conversation turn from
// the runtime ledger. Token figures come solely from `model` events (each model
// call carries its own lane-tagged usage) so tool-calling loops with several
// calls per turn are summed without double counting the `terminal` summary.
// `terminal` events only supply each turn's final status.
func (s *Store) AggregateTokenUsage(limit int) (UsageReport, error) {
	return s.AggregateTokenUsageContext(context.Background(), limit)
}

func (s *Store) AggregateTokenUsageContext(ctx context.Context, limit int) (UsageReport, error) {
	return s.aggregateTokenUsagePostgres(ctx, limit)
}

func aggregateUsageRows(characterByConversation map[string]string, ledgerRows []usageLedgerRow, limit int) (UsageReport, error) {
	if limit <= 0 {
		limit = DefaultUsageTurnLimit
	}
	if limit > maxUsageTurnLimit {
		limit = maxUsageTurnLimit
	}
	accumulators := make(map[string]*usageTurnAccumulator)
	order := make([]string, 0)
	for _, row := range ledgerRows {
		key := row.conversationID + "\x00" + row.turnID
		accumulator := accumulators[key]
		if accumulator == nil {
			accumulator = &usageTurnAccumulator{
				conversationID: row.conversationID,
				turnID:         row.turnID,
				status:         usageTurnStatusUnknown,
				lanes:          make(map[string]*UsageLaneAggregate),
			}
			accumulators[key] = accumulator
			order = append(order, key)
		}
		if row.createdAtUnixMS > accumulator.createdAtUnixMS {
			accumulator.createdAtUnixMS = row.createdAtUnixMS
		}
		switch row.eventType {
		case "model":
			if err := accumulateModelUsage(accumulator, row.metadataJSON); err != nil {
				return UsageReport{}, err
			}
		case "terminal":
			if row.state != nil && *row.state != "" {
				accumulator.status = *row.state
			}
		}
	}

	overall := make(map[string]*UsageLaneAggregate)
	turns := make([]UsageTurn, 0, len(order))
	for _, key := range order {
		accumulator := accumulators[key]
		for lane, aggregate := range accumulator.lanes {
			mergeLaneAggregate(overall, lane, aggregate)
		}
		turns = append(turns, UsageTurn{
			ConversationID:  accumulator.conversationID,
			TurnID:          accumulator.turnID,
			CharacterID:     characterByConversation[accumulator.conversationID],
			CreatedAtUnixMS: accumulator.createdAtUnixMS,
			Status:          accumulator.status,
			Lanes:           sortedLaneAggregateSlice(accumulator.lanes),
		})
	}

	sort.SliceStable(turns, func(a, b int) bool {
		if turns[a].CreatedAtUnixMS != turns[b].CreatedAtUnixMS {
			return turns[a].CreatedAtUnixMS > turns[b].CreatedAtUnixMS
		}
		return turns[a].TurnID > turns[b].TurnID
	})

	turnCount := uint64(len(turns))
	truncated := false
	if len(turns) > limit {
		turns = turns[:limit]
		truncated = true
	}

	return UsageReport{
		Overall:   sortedLaneAggregateSlice(overall),
		Turns:     turns,
		TurnCount: turnCount,
		Truncated: truncated,
	}, nil
}

func accumulateModelUsage(accumulator *usageTurnAccumulator, metadataJSON string) error {
	var metadata ledgerModelMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return fmt.Errorf("decoding model usage metadata: %w", err)
	}
	for _, entry := range metadata.Usage {
		if entry.Lane == "" {
			continue
		}
		aggregate := accumulator.lanes[entry.Lane]
		if aggregate == nil {
			aggregate = &UsageLaneAggregate{Lane: entry.Lane}
			accumulator.lanes[entry.Lane] = aggregate
		}
		aggregate.CallCount++
		if entry.Usage.InputTokens != nil {
			aggregate.InputTokens += *entry.Usage.InputTokens
		}
		if entry.Usage.OutputTokens != nil {
			aggregate.OutputTokens += *entry.Usage.OutputTokens
		}
		if entry.Usage.CachedInputTokens.Status == "observed" && entry.Usage.CachedInputTokens.Tokens != nil {
			aggregate.CachedInputTokens += *entry.Usage.CachedInputTokens.Tokens
			if entry.Usage.InputTokens != nil {
				aggregate.CachedObservedInputTokens += *entry.Usage.InputTokens
			}
		}
		if entry.Usage.CacheWriteTokens.Status == "observed" && entry.Usage.CacheWriteTokens.Tokens != nil {
			aggregate.CacheWriteTokens += *entry.Usage.CacheWriteTokens.Tokens
		}
	}
	return nil
}

func mergeLaneAggregate(target map[string]*UsageLaneAggregate, lane string, source *UsageLaneAggregate) {
	aggregate := target[lane]
	if aggregate == nil {
		aggregate = &UsageLaneAggregate{Lane: lane}
		target[lane] = aggregate
	}
	aggregate.InputTokens += source.InputTokens
	aggregate.OutputTokens += source.OutputTokens
	aggregate.CachedInputTokens += source.CachedInputTokens
	aggregate.CachedObservedInputTokens += source.CachedObservedInputTokens
	aggregate.CacheWriteTokens += source.CacheWriteTokens
	aggregate.CallCount += source.CallCount
}

func sortedLaneAggregateSlice(lanes map[string]*UsageLaneAggregate) []UsageLaneAggregate {
	result := make([]UsageLaneAggregate, 0, len(lanes))
	for _, aggregate := range lanes {
		result = append(result, *aggregate)
	}
	sort.SliceStable(result, func(a, b int) bool {
		return result[a].Lane < result[b].Lane
	})
	return result
}
