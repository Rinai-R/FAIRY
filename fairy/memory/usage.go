package memory

import (
	"database/sql"
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

// AggregateTokenUsage rolls up token usage across every conversation turn from
// the runtime ledger. Token figures come solely from `model` events (each model
// call carries its own lane-tagged usage) so tool-calling loops with several
// calls per turn are summed without double counting the `terminal` summary.
// `terminal` events only supply each turn's final status.
func (s *Store) AggregateTokenUsage(limit int) (UsageReport, error) {
	if limit <= 0 {
		limit = DefaultUsageTurnLimit
	}
	if limit > maxUsageTurnLimit {
		limit = maxUsageTurnLimit
	}
	db, err := s.openReadOnly()
	if err != nil {
		return UsageReport{}, err
	}
	defer db.Close()

	characterByConversation, err := loadCharacterByConversation(db)
	if err != nil {
		return UsageReport{}, err
	}

	rows, err := db.Query("SELECT conversation_id, turn_id, event_type, state, metadata_json, created_at_ms FROM turn_runtime_events WHERE event_type IN ('model', 'terminal') ORDER BY created_at_ms ASC, sequence ASC")
	if err != nil {
		return UsageReport{}, fmt.Errorf("querying runtime usage events: %w", err)
	}
	defer rows.Close()

	accumulators := make(map[string]*usageTurnAccumulator)
	order := make([]string, 0)
	for rows.Next() {
		var conversationID string
		var turnID string
		var eventType string
		var state *string
		var metadataJSON string
		var createdAtUnixMS int64
		if err := rows.Scan(&conversationID, &turnID, &eventType, &state, &metadataJSON, &createdAtUnixMS); err != nil {
			return UsageReport{}, fmt.Errorf("scanning runtime usage event: %w", err)
		}
		key := conversationID + "\x00" + turnID
		accumulator := accumulators[key]
		if accumulator == nil {
			accumulator = &usageTurnAccumulator{
				conversationID: conversationID,
				turnID:         turnID,
				status:         usageTurnStatusUnknown,
				lanes:          make(map[string]*UsageLaneAggregate),
			}
			accumulators[key] = accumulator
			order = append(order, key)
		}
		if createdAtUnixMS > accumulator.createdAtUnixMS {
			accumulator.createdAtUnixMS = createdAtUnixMS
		}
		switch eventType {
		case "model":
			if err := accumulateModelUsage(accumulator, metadataJSON); err != nil {
				return UsageReport{}, err
			}
		case "terminal":
			if state != nil && *state != "" {
				accumulator.status = *state
			}
		}
	}
	if err := rows.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("iterating runtime usage events: %w", err)
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

func loadCharacterByConversation(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SELECT id, character_id FROM conversations")
	if err != nil {
		return nil, fmt.Errorf("querying conversations for usage report: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var id string
		var characterID string
		if err := rows.Scan(&id, &characterID); err != nil {
			return nil, fmt.Errorf("scanning conversation for usage report: %w", err)
		}
		result[id] = characterID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating conversations for usage report: %w", err)
	}
	return result, nil
}
