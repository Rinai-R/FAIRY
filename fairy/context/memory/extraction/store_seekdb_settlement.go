package extraction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"fairy/context/memory/personal"
	"fairy/runtime/embedding"
)

type seekDBPreparedMutation struct {
	mutation          Mutation
	embedding         embedding.EmbeddingValue
	embeddingPrepared bool
}

type seekDBDuplicateKey struct {
	kind, scopeKind, characterID, normalizedContent string
}

func validateClaimedBatch(batch *ClaimedBatch) error {
	if batch == nil {
		return errors.New("claimed extraction batch is required")
	}
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "batch_id", value: batch.BatchID},
		{label: "conversation_id", value: batch.ConversationID},
		{label: "character_id", value: batch.CharacterID},
	} {
		if err := validateASCIIID(item.label, item.value); err != nil {
			return err
		}
	}
	if len(batch.Turns) < 1 || len(batch.Turns) > DefaultBatchLimit {
		return errors.New("claimed extraction batch turn count is invalid")
	}
	seen := make(map[string]struct{}, len(batch.Turns))
	for _, turn := range batch.Turns {
		if err := validateASCIIID("turn_id", turn.TurnID); err != nil {
			return err
		}
		if _, exists := seen[turn.TurnID]; exists {
			return errors.New("claimed extraction batch contains a duplicate turn")
		}
		seen[turn.TurnID] = struct{}{}
		if !utf8.ValidString(turn.UserMessage) || !utf8.ValidString(turn.AssistantMessage) {
			return errors.New("claimed extraction batch contains invalid message text")
		}
	}
	return nil
}

func cloneBatchInput(batch *BatchInput) *BatchInput {
	if batch == nil {
		return nil
	}
	clone := *batch
	clone.Turns = slices.Clone(batch.Turns)
	clone.ExistingMemories = slices.Clone(batch.ExistingMemories)
	return &clone
}

func validateSeekDBBatchInput(batch *BatchInput) (map[string]personal.Retrieved, error) {
	if batch == nil {
		return nil, errors.New("extraction batch input is required")
	}
	if err := validateClaimedBatch(&ClaimedBatch{
		BatchID: batch.BatchID, ConversationID: batch.ConversationID,
		CharacterID: batch.CharacterID, Turns: batch.Turns,
	}); err != nil {
		return nil, err
	}
	if len(batch.ExistingMemories) > MaxMutations {
		return nil, errors.New("extraction existing-memory projection exceeds limit")
	}
	existing := make(map[string]personal.Retrieved, len(batch.ExistingMemories))
	for _, item := range batch.ExistingMemories {
		if err := validateASCIIID("existing_memory_id", item.ID); err != nil {
			return nil, err
		}
		if _, exists := existing[item.ID]; exists {
			return nil, errors.New("extraction existing-memory projection contains a duplicate id")
		}
		if err := personal.ValidateInput(item.Kind, item.Scope, item.Content, item.ConfidenceBasisPoints); err != nil {
			return nil, fmt.Errorf("validating existing personal memory %s: %w", item.ID, err)
		}
		if item.Scope.Type == "character" && item.Scope.CharacterID != batch.CharacterID {
			return nil, errors.New("existing personal memory does not belong to the extraction character")
		}
		if item.Scope.Type != "global" && item.Scope.Type != "character" {
			return nil, errors.New("existing personal memory scope is not extraction eligible")
		}
		if item.Layer != personal.Layer(item.Kind, item.Scope) || item.UpdatedAtUnixMS < 0 {
			return nil, errors.New("existing personal memory projection is invalid")
		}
		existing[item.ID] = item
	}
	return existing, nil
}

func (s *Store) planSeekDBSettlement(
	batch *BatchInput,
	mutations []Mutation,
) (*BatchInput, map[string]personal.Retrieved, []seekDBPreparedMutation, error) {
	batch = cloneBatchInput(batch)
	existing, err := validateSeekDBBatchInput(batch)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(mutations) > MaxMutations {
		return nil, nil, nil, errors.New("extraction batch exceeds memory mutation limit")
	}
	mutations = slices.Clone(mutations)
	turns := make(map[string]struct{}, len(batch.Turns))
	for _, turn := range batch.Turns {
		turns[turn.TurnID] = struct{}{}
	}
	targets := make(map[string]struct{}, len(mutations))
	prepared := make([]seekDBPreparedMutation, len(mutations))
	for index, mutation := range mutations {
		if err := ValidateMutation(&mutation, batch.CharacterID); err != nil {
			return nil, nil, nil, err
		}
		if err := validateASCIIID("source_turn_id", mutation.SourceTurnID); err != nil {
			return nil, nil, nil, err
		}
		if _, exists := turns[mutation.SourceTurnID]; !exists {
			return nil, nil, nil, errors.New("memory mutation source turn is not provided to the batch")
		}
		if mutation.MemoryID != "" {
			if err := validateASCIIID("memory_id", mutation.MemoryID); err != nil {
				return nil, nil, nil, err
			}
			if _, exists := targets[mutation.MemoryID]; exists {
				return nil, nil, nil, errors.New("memory mutation target is repeated within the batch")
			}
			targets[mutation.MemoryID] = struct{}{}
			alias, exists := existing[mutation.MemoryID]
			if !exists {
				return nil, nil, nil, errors.New("memory mutation references an alias not supplied to the batch")
			}
			if mutation.Operation == OperationReplace &&
				(alias.Kind != mutation.Kind || alias.Scope != mutation.Scope) {
				return nil, nil, nil, errors.New("REPLACE target kind or scope does not match its supplied alias")
			}
		}
		if mutation.Scope.Type == "character" {
			if err := validateASCIIID("memory character_id", mutation.Scope.CharacterID); err != nil {
				return nil, nil, nil, err
			}
		}
		prepared[index].mutation = mutation
	}
	return batch, existing, prepared, nil
}

func (s *Store) commitClaimedMemoryMutationsSeekDB(
	ctx context.Context,
	batch *BatchInput,
	mutations []Mutation,
) ([]MutationResult, error) {
	batch, supplied, prepared, err := s.planSeekDBSettlement(batch, mutations)
	if err != nil {
		return nil, err
	}
	if err := s.preflightSeekDBSettlementClaim(ctx, batch); err != nil {
		return nil, err
	}
	candidates, lockIDs, err := s.discoverSeekDBSettlementDuplicates(ctx, supplied, prepared)
	if err != nil {
		return nil, err
	}
	embeddingContents, embeddingIndexes := seekDBSettlementEmbeddingPlan(prepared, candidates)
	values, err := s.personal.PrepareEmbeddingsContext(ctx, embeddingContents)
	if err != nil {
		return nil, err
	}
	if len(values) != len(embeddingIndexes) {
		return nil, errors.New("personal embedding preparation returned an invalid result count")
	}
	for index, mutationIndex := range embeddingIndexes {
		prepared[mutationIndex].embedding = values[index]
		prepared[mutationIndex].embeddingPrepared = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The external provider has completed. From this point forward settlement
	// is an atomic database barrier bounded by the Store query limit; parent
	// shutdown must not turn a committed transaction into an unknown result.
	queryCtx, cancel := s.seekDBQueryContext(context.WithoutCancel(ctx))
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning SeekDB extraction settlement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	characterID, storedTimeFloor, err := s.lockSeekDBExtractionConversation(queryCtx, tx, batch.ConversationID)
	if err != nil {
		return nil, err
	}
	if characterID != batch.CharacterID {
		return nil, seekDBSettlementConflict("extraction batch character changed")
	}
	lockedTurns, err := lockSeekDBSettlementTurns(queryCtx, tx, batch, s.workerID)
	if err != nil {
		return nil, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageSettleAfterEvidenceLock); err != nil {
		return nil, err
	}
	if len(lockedTurns) != len(batch.Turns) {
		return nil, seekDBSettlementConflict("extraction claim turn set changed")
	}
	effectiveNow := max(s.currentUnixMS(), storedTimeFloor)
	for index, turn := range lockedTurns {
		expected := batch.Turns[index]
		if turn.id != expected.TurnID || turn.user != expected.UserMessage ||
			turn.assistant != expected.AssistantMessage {
			return nil, seekDBSettlementConflict("extraction claim evidence changed")
		}
		effectiveNow = max(effectiveNow, turn.updatedAt)
	}
	if err := s.personal.LockSeekDBMutationGuardTx(queryCtx, tx); err != nil {
		return nil, err
	}
	authoritativeRemaining := personal.MaxContentRunes
	authoritative, err := s.personal.RetrieveExtractionProjectionSeekDBTx(
		queryCtx, tx, batch.CharacterID, BuildRetrievalProjection(batch.Turns), &authoritativeRemaining,
	)
	if err != nil {
		return nil, err
	}
	if !equalExtractionProjection(authoritative, batch.ExistingMemories) {
		return nil, seekDBSettlementConflict("existing personal memory projection changed")
	}
	authoritativeCandidates := make([][]string, len(prepared))
	authoritativeCache := make(map[seekDBDuplicateKey][]string, len(prepared))
	for index, item := range prepared {
		mutation := item.mutation
		if mutation.Operation != OperationAdd && mutation.Operation != OperationReplace {
			continue
		}
		key := seekDBMutationDuplicateKey(mutation)
		ids, exists := authoritativeCache[key]
		if !exists {
			ids, err = s.personal.ListActiveDuplicateCandidateIDsSeekDBTx(
				queryCtx, tx, mutation.Kind, mutation.Scope, mutation.Content,
			)
			if err != nil {
				return nil, err
			}
			authoritativeCache[key] = ids
		}
		authoritativeCandidates[index] = slices.Clone(ids)
		lockIDs = append(lockIDs, ids...)
	}

	lockedRecords, err := s.personal.LockSeekDBRecordsTx(queryCtx, tx, lockIDs)
	if err != nil {
		return nil, fmt.Errorf("locking SeekDB extraction personal memories: %w", err)
	}
	recordIndexes := make(map[string]int, len(lockedRecords))
	for index, record := range lockedRecords {
		recordIndexes[record.ID] = index
		effectiveNow = max(effectiveNow, record.UpdatedAtUnixMS)
	}
	for memoryID, alias := range supplied {
		index, exists := recordIndexes[memoryID]
		if !exists {
			return nil, seekDBSettlementConflict("supplied personal memory disappeared")
		}
		record := lockedRecords[index]
		if record.Status != "active" || record.ReviewStatus != "ready" ||
			record.Kind != alias.Kind || record.Scope != alias.Scope ||
			record.Content != alias.Content ||
			record.ConfidenceBasisPoints != alias.ConfidenceBasisPoints ||
			record.UpdatedAtUnixMS != alias.UpdatedAtUnixMS {
			return nil, seekDBSettlementConflict("supplied personal memory changed")
		}
		if record.Scope.Type != "global" &&
			(record.Scope.Type != "character" || record.Scope.CharacterID != batch.CharacterID) {
			return nil, seekDBSettlementConflict("supplied personal memory left the extraction scope")
		}
	}

	results := make([]MutationResult, 0, len(prepared))
	// Records inserted earlier in this batch participate in the same stable
	// winner ordering as authoritative rows. They must not win merely because
	// they are new: later batches will use updated_at_ms DESC, id ASC too.
	createdDuplicates := make(map[seekDBDuplicateKey][]personal.Record, len(prepared))
	for index, item := range prepared {
		mutation := item.mutation
		duplicate, foundDuplicate := selectCurrentSeekDBSettlementDuplicate(
			authoritativeCandidates[index], lockedRecords, recordIndexes,
			createdDuplicates[seekDBMutationDuplicateKey(mutation)], mutation,
		)
		duplicateKey := seekDBMutationDuplicateKey(mutation)
		switch mutation.Operation {
		case OperationAdd:
			if foundDuplicate {
				results = append(results, MutationResult{Status: "no_change", ExistingMemoryID: duplicate.ID})
				continue
			}
			if !item.embeddingPrepared {
				return nil, seekDBSettlementConflict("memory candidates changed during embedding preparation")
			}
			record, err := s.personal.InsertSeekDBTx(
				queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content,
				mutation.ConfidenceBasisPoints, batch.ConversationID, mutation.SourceTurnID,
				nil, effectiveNow, item.embedding,
			)
			if err != nil {
				return nil, err
			}
			if err := s.runSeekDBWriteHook(seekDBStageSettleAfterInsert); err != nil {
				return nil, err
			}
			if err := s.setSeekDBSettlementEvidence(queryCtx, tx, record.ID, mutation.SourceTurnID, effectiveNow); err != nil {
				return nil, err
			}
			createdDuplicates[duplicateKey] = append(createdDuplicates[duplicateKey], record)
			results = append(results, MutationResult{Status: "applied", MemoryID: record.ID})
		case OperationReplace:
			targetIndex, exists := recordIndexes[mutation.MemoryID]
			if !exists {
				return nil, seekDBSettlementConflict("REPLACE target disappeared")
			}
			target := lockedRecords[targetIndex]
			if target.Status != "active" || target.Kind != mutation.Kind || target.Scope != mutation.Scope {
				return nil, seekDBSettlementConflict("REPLACE target changed")
			}
			if foundDuplicate && duplicate.ID != mutation.MemoryID {
				results = append(results, MutationResult{Status: "no_change", ExistingMemoryID: duplicate.ID})
				continue
			}
			if !item.embeddingPrepared {
				return nil, seekDBSettlementConflict("memory candidates changed during embedding preparation")
			}
			if err := s.personal.SupersedeSeekDBTx(queryCtx, tx, mutation.MemoryID, effectiveNow); err != nil {
				return nil, err
			}
			lockedRecords[targetIndex].Status = "superseded"
			lockedRecords[targetIndex].UpdatedAtUnixMS = effectiveNow
			if err := s.runSeekDBWriteHook(seekDBStageSettleAfterSupersede); err != nil {
				return nil, err
			}
			supersedesID := mutation.MemoryID
			record, err := s.personal.InsertSeekDBTx(
				queryCtx, tx, newID(), mutation.Kind, mutation.Scope, mutation.Content,
				mutation.ConfidenceBasisPoints, batch.ConversationID, mutation.SourceTurnID,
				&supersedesID, effectiveNow, item.embedding,
			)
			if err != nil {
				return nil, err
			}
			if err := s.runSeekDBWriteHook(seekDBStageSettleAfterInsert); err != nil {
				return nil, err
			}
			if err := s.setSeekDBSettlementEvidence(queryCtx, tx, record.ID, mutation.SourceTurnID, effectiveNow); err != nil {
				return nil, err
			}
			createdDuplicates[duplicateKey] = append(createdDuplicates[duplicateKey], record)
			results = append(results, MutationResult{Status: "applied", MemoryID: record.ID})
		case OperationDelete:
			targetIndex, exists := recordIndexes[mutation.MemoryID]
			if !exists {
				return nil, seekDBSettlementConflict("DELETE target disappeared")
			}
			if lockedRecords[targetIndex].Status != "active" {
				return nil, seekDBSettlementConflict("DELETE target changed")
			}
			if err := s.personal.TombstoneSeekDBTx(queryCtx, tx, mutation.MemoryID, effectiveNow); err != nil {
				return nil, err
			}
			lockedRecords[targetIndex].Status = "tombstone"
			lockedRecords[targetIndex].UpdatedAtUnixMS = effectiveNow
			results = append(results, MutationResult{Status: "applied", ExistingMemoryID: mutation.MemoryID})
		case OperationNone:
			targetIndex, exists := recordIndexes[mutation.MemoryID]
			if !exists {
				return nil, seekDBSettlementConflict("NONE target disappeared")
			}
			if lockedRecords[targetIndex].Status != "active" {
				return nil, seekDBSettlementConflict("NONE target changed")
			}
			results = append(results, MutationResult{Status: "no_change", ExistingMemoryID: mutation.MemoryID})
		default:
			return nil, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
	}

	for index, result := range results {
		memoryID := result.MemoryID
		if memoryID == "" {
			memoryID = result.ExistingMemoryID
		}
		if memoryID == "" {
			return nil, errors.New("committed memory mutation has no coverage memory")
		}
		if err := insertSeekDBCoverage(
			queryCtx, tx, batch.ConversationID, prepared[index].mutation.SourceTurnID,
			memoryID, result.Status, effectiveNow,
		); err != nil {
			return nil, err
		}
	}
	if err := s.runSeekDBWriteHook(seekDBStageSettleAfterCoverage); err != nil {
		return nil, err
	}
	if err := completeSeekDBSettlement(
		queryCtx, tx, batch.ConversationID, batch.BatchID, s.workerID,
		batch.Turns, effectiveNow,
	); err != nil {
		return nil, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageSettleAfterProcessed); err != nil {
		return nil, err
	}
	if err := s.runSeekDBWriteHook(seekDBStageSettleBeforeCommit); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing SeekDB extraction settlement: %w", err)
	}
	return results, nil
}

// seekDBSettlementEmbeddingPlan mirrors the existing per-mutation order using
// the preflight candidate snapshot. A pure no_change mutation never invokes
// the semantic provider. If an earlier mutation removes the only duplicate,
// the later ADD/REPLACE is prepared because it may insert in the transaction.
func seekDBSettlementEmbeddingPlan(
	prepared []seekDBPreparedMutation,
	candidates [][]string,
) ([]string, []int) {
	active := make(map[string]bool)
	for _, ids := range candidates {
		for _, id := range ids {
			active[id] = true
		}
	}
	for _, item := range prepared {
		if item.mutation.MemoryID != "" {
			active[item.mutation.MemoryID] = true
		}
	}
	contents := make([]string, 0, len(prepared))
	indexes := make([]int, 0, len(prepared))
	for index, item := range prepared {
		mutation := item.mutation
		duplicateID := firstActiveSeekDBCandidate(candidates[index], active)
		switch mutation.Operation {
		case OperationAdd:
			if duplicateID == "" {
				indexes = append(indexes, index)
				contents = append(contents, mutation.Content)
			}
		case OperationReplace:
			if duplicateID == "" || duplicateID == mutation.MemoryID {
				indexes = append(indexes, index)
				contents = append(contents, mutation.Content)
				active[mutation.MemoryID] = false
			}
		case OperationDelete:
			active[mutation.MemoryID] = false
		}
	}
	return contents, indexes
}

func firstActiveSeekDBCandidate(ids []string, active map[string]bool) string {
	for _, id := range ids {
		if active[id] {
			return id
		}
	}
	return ""
}

func seekDBMutationDuplicateKey(mutation Mutation) seekDBDuplicateKey {
	return seekDBDuplicateKey{
		kind: mutation.Kind, scopeKind: mutation.Scope.Type,
		characterID:       mutation.Scope.CharacterID,
		normalizedContent: personal.NormalizeContent(mutation.Content),
	}
}

func selectCurrentSeekDBSettlementDuplicate(
	candidateIDs []string,
	lockedRecords []personal.Record,
	recordIndexes map[string]int,
	createdRecords []personal.Record,
	mutation Mutation,
) (personal.Record, bool) {
	matches := make([]personal.Record, 0, len(candidateIDs)+len(createdRecords))
	for _, id := range candidateIDs {
		index, exists := recordIndexes[id]
		if !exists {
			continue
		}
		record := lockedRecords[index]
		if record.Status == "active" && record.ReviewStatus == "ready" &&
			record.Kind == mutation.Kind && record.Scope == mutation.Scope &&
			personal.NormalizeContent(record.Content) == personal.NormalizeContent(mutation.Content) {
			matches = append(matches, record)
		}
	}
	for _, record := range createdRecords {
		if record.Status == "active" && record.ReviewStatus == "ready" &&
			record.Kind == mutation.Kind && record.Scope == mutation.Scope &&
			personal.NormalizeContent(record.Content) == personal.NormalizeContent(mutation.Content) {
			matches = append(matches, record)
		}
	}
	slices.SortFunc(matches, func(left, right personal.Record) int {
		if left.UpdatedAtUnixMS > right.UpdatedAtUnixMS {
			return -1
		}
		if left.UpdatedAtUnixMS < right.UpdatedAtUnixMS {
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	if len(matches) == 0 {
		return personal.Record{}, false
	}
	return matches[0], true
}

// discoverSeekDBSettlementDuplicates performs an advisory indexed lookup
// before the write transaction starts so known no_change mutations can skip
// provider work. The final transaction locks the singleton write guard and
// repeats the lookup authoritatively; this snapshot never authorizes a write.
func (s *Store) discoverSeekDBSettlementDuplicates(
	ctx context.Context,
	supplied map[string]personal.Retrieved,
	prepared []seekDBPreparedMutation,
) ([][]string, []string, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	candidates := make([][]string, len(prepared))
	lockIDs := make([]string, 0, len(supplied))
	for memoryID := range supplied {
		lockIDs = append(lockIDs, memoryID)
	}
	cache := make(map[seekDBDuplicateKey][]string, len(prepared))
	for index, item := range prepared {
		mutation := item.mutation
		if mutation.Operation != OperationAdd && mutation.Operation != OperationReplace {
			continue
		}
		key := seekDBDuplicateKey{
			kind: mutation.Kind, scopeKind: mutation.Scope.Type,
			characterID:       mutation.Scope.CharacterID,
			normalizedContent: personal.NormalizeContent(mutation.Content),
		}
		ids, exists := cache[key]
		if !exists {
			var err error
			ids, err = s.personal.ListActiveDuplicateCandidateIDsSeekDBContext(
				queryCtx, mutation.Kind, mutation.Scope, mutation.Content,
			)
			if err != nil {
				return nil, nil, err
			}
			cache[key] = ids
		}
		candidates[index] = slices.Clone(ids)
	}
	return candidates, lockIDs, nil
}

type seekDBSettlementTurn struct {
	id, user, assistant string
	updatedAt           int64
}

// preflightSeekDBSettlementClaim avoids provider calls for already-terminal or
// foreign claims. It is advisory only; the transaction below re-locks and
// revalidates the exact Turn set before writing anything.
func (s *Store) preflightSeekDBSettlementClaim(ctx context.Context, batch *BatchInput) error {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var count int64
	if err := s.seekDB.QueryRowContext(queryCtx, `
SELECT COUNT(*)
FROM conversation_turns
WHERE conversation_id = ?
  AND extraction_claim_id = ?
  AND extraction_lease_owner = ?
  AND extraction_state = 'claimed'`,
		batch.ConversationID, batch.BatchID, s.workerID,
	).Scan(&count); err != nil {
		return fmt.Errorf("checking SeekDB extraction settlement claim: %w", err)
	}
	if count != int64(len(batch.Turns)) {
		return seekDBSettlementConflict("extraction claim is no longer running")
	}
	return nil
}

func lockSeekDBSettlementTurns(
	ctx context.Context,
	tx *sql.Tx,
	batch *BatchInput,
	workerID string,
) ([]seekDBSettlementTurn, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, updated_at_ms
FROM conversation_turns
WHERE conversation_id = ?
  AND extraction_claim_id = ?
  AND extraction_lease_owner = ?
  AND extraction_state = 'claimed'
  AND status = 'completed'
  AND origin = 'user'
ORDER BY sequence
FOR UPDATE`, batch.ConversationID, batch.BatchID, workerID)
	if err != nil {
		return nil, fmt.Errorf("locking SeekDB extraction settlement turns: %w", err)
	}
	turns := make([]seekDBSettlementTurn, 0, len(batch.Turns))
	for rows.Next() {
		var turn seekDBSettlementTurn
		if err := rows.Scan(&turn.id, &turn.updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning SeekDB extraction settlement turn: %w", err)
		}
		if err := validateASCIIID("stored extraction turn id", turn.id); err != nil {
			rows.Close()
			return nil, err
		}
		if turn.updatedAt < 0 {
			rows.Close()
			return nil, errors.New("stored extraction turn timestamp is invalid")
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating SeekDB extraction settlement turns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing SeekDB extraction settlement turns: %w", err)
	}
	if len(turns) == 0 || len(turns) != len(batch.Turns) {
		return nil, seekDBSettlementConflict("extraction claim is not owned by this worker")
	}
	messageRows, err := tx.QueryContext(ctx, `
SELECT turn.id, user_message.content, assistant_message.content
FROM conversation_turns AS turn
JOIN conversation_messages AS user_message
  ON user_message.conversation_id = turn.conversation_id
 AND user_message.turn_id = turn.id
 AND user_message.role = 'user'
JOIN conversation_messages AS assistant_message
  ON assistant_message.conversation_id = turn.conversation_id
 AND assistant_message.turn_id = turn.id
 AND assistant_message.role = 'assistant'
WHERE turn.conversation_id = ?
  AND turn.extraction_claim_id = ?
  AND turn.extraction_lease_owner = ?
  AND turn.extraction_state = 'claimed'
ORDER BY turn.sequence
FOR UPDATE`, batch.ConversationID, batch.BatchID, workerID)
	if err != nil {
		return nil, fmt.Errorf("loading SeekDB extraction settlement evidence: %w", err)
	}
	index := 0
	for messageRows.Next() {
		if index >= len(turns) {
			messageRows.Close()
			return nil, seekDBSettlementConflict("extraction claim evidence changed")
		}
		var id string
		if err := messageRows.Scan(&id, &turns[index].user, &turns[index].assistant); err != nil {
			messageRows.Close()
			return nil, fmt.Errorf("scanning SeekDB extraction settlement evidence: %w", err)
		}
		if id != turns[index].id {
			messageRows.Close()
			return nil, seekDBSettlementConflict("extraction claim evidence changed")
		}
		index++
	}
	if err := messageRows.Err(); err != nil {
		messageRows.Close()
		return nil, fmt.Errorf("iterating SeekDB extraction settlement evidence: %w", err)
	}
	if err := messageRows.Close(); err != nil {
		return nil, fmt.Errorf("closing SeekDB extraction settlement evidence: %w", err)
	}
	if index != len(turns) {
		return nil, seekDBSettlementConflict("extraction claim is missing completed messages")
	}
	return turns, nil
}

func (s *Store) setSeekDBSettlementEvidence(
	ctx context.Context,
	tx *sql.Tx,
	memoryID, turnID string,
	now int64,
) error {
	rows, err := tx.QueryContext(ctx, `
SELECT evidence_id
FROM conversation_turn_evidence
WHERE turn_id = ?
ORDER BY evidence_id`, turnID)
	if err != nil {
		return fmt.Errorf("loading SeekDB memory source evidence: %w", err)
	}
	evidenceIDs := make([]string, 0, 8)
	for rows.Next() {
		var evidenceID string
		if err := rows.Scan(&evidenceID); err != nil {
			rows.Close()
			return fmt.Errorf("scanning SeekDB memory source evidence: %w", err)
		}
		evidenceIDs = append(evidenceIDs, evidenceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating SeekDB memory source evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing SeekDB memory source evidence: %w", err)
	}
	if err := s.personal.SetEvidenceIDsSeekDBTx(ctx, tx, memoryID, evidenceIDs, now); err != nil {
		return err
	}
	return s.runSeekDBWriteHook(seekDBStageSettleAfterEvidence)
}

func insertSeekDBCoverage(
	ctx context.Context,
	tx *sql.Tx,
	conversationID, turnID, memoryID, resultStatus string,
	now int64,
) error {
	if resultStatus != "applied" && resultStatus != "no_change" {
		return errors.New("memory context coverage result status is invalid")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_context_coverages(
  conversation_id, turn_id, memory_id, result_status, created_at_ms
) VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  result_status = CASE
    WHEN result_status = 'applied' OR VALUES(result_status) = 'applied' THEN 'applied'
    ELSE 'no_change'
  END,
  created_at_ms = LEAST(created_at_ms, VALUES(created_at_ms))`,
		conversationID, turnID, memoryID, resultStatus, now,
	); err != nil {
		return fmt.Errorf("inserting SeekDB memory context coverage: %w", err)
	}
	return nil
}

func completeSeekDBSettlement(
	ctx context.Context,
	tx *sql.Tx,
	conversationID, batchID, workerID string,
	turns []Turn,
	now int64,
) error {
	placeholders := make([]string, len(turns))
	arguments := make([]any, 0, 5+len(turns))
	arguments = append(arguments, now, conversationID, batchID, workerID)
	for index, turn := range turns {
		placeholders[index] = "?"
		arguments = append(arguments, turn.TurnID)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE conversation_turns
SET extraction_state = 'processed',
    extraction_claim_id = NULL,
    extraction_lease_owner = NULL,
    extraction_lease_expires_at_ms = NULL,
    extraction_next_attempt_at_ms = 0,
    extraction_error_code = NULL,
    extraction_error_message = NULL,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE conversation_id = ?
  AND extraction_claim_id = ?
  AND extraction_lease_owner = ?
  AND extraction_state = 'claimed'
  AND id IN (`+strings.Join(placeholders, ",")+`)`, arguments...)
	if err != nil {
		return fmt.Errorf("completing SeekDB extraction settlement: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting completed SeekDB extraction turns: %w", err)
	}
	if affected != int64(len(turns)) {
		return seekDBSettlementConflict("extraction claim changed before completion")
	}
	return nil
}

func seekDBSettlementConflict(message string) error {
	return fmt.Errorf("%w: %s", ErrExtractionClaimConflict, message)
}

func (s *Store) loadCommittedMemoryCoverageSeekDB(
	ctx context.Context,
	conversationID string,
) ([]Coverage, error) {
	if err := validateASCIIID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT covered.conversation_id,
       covered.turn_id,
       covered.memory_id,
       covered.result_status,
       turn.sequence,
       MIN(message.sequence),
       MAX(message.sequence),
       GREATEST(1, (SUM(CHAR_LENGTH(message.content)) + 3) DIV 4),
       covered.created_at_ms
FROM (
  SELECT conversation_id,
         turn_id,
         MIN(memory_id) AS memory_id,
         MIN(result_status) AS result_status,
         MIN(created_at_ms) AS created_at_ms
  FROM memory_context_coverages
  WHERE conversation_id = ?
  GROUP BY conversation_id, turn_id
) AS covered
JOIN conversation_turns AS turn
  ON turn.id = covered.turn_id
 AND turn.conversation_id = covered.conversation_id
JOIN conversation_messages AS message
  ON message.turn_id = covered.turn_id
 AND message.conversation_id = covered.conversation_id
WHERE turn.status = 'completed'
GROUP BY covered.conversation_id, covered.turn_id, covered.memory_id,
         covered.result_status, turn.sequence, covered.created_at_ms
HAVING COUNT(*) = 2
ORDER BY turn.sequence, covered.turn_id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("loading committed SeekDB memory coverage: %w", err)
	}
	defer rows.Close()
	records := make([]Coverage, 0)
	for rows.Next() {
		var record Coverage
		var turnSequence, startSequence, endSequence, coveredTokens int64
		if err := rows.Scan(
			&record.ConversationID, &record.TurnID, &record.MemoryID,
			&record.ResultStatus, &turnSequence, &startSequence, &endSequence,
			&coveredTokens, &record.CreatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scanning committed SeekDB memory coverage: %w", err)
		}
		for _, item := range []struct {
			label string
			value string
		}{
			{label: "stored coverage conversation id", value: record.ConversationID},
			{label: "stored coverage turn id", value: record.TurnID},
			{label: "stored coverage memory id", value: record.MemoryID},
		} {
			if err := validateASCIIID(item.label, item.value); err != nil {
				return nil, err
			}
		}
		if record.ResultStatus != "applied" && record.ResultStatus != "no_change" {
			return nil, errors.New("stored memory coverage result status is invalid")
		}
		if turnSequence <= 0 || startSequence <= 0 || endSequence < startSequence ||
			coveredTokens <= 0 || record.CreatedAtUnixMS < 0 {
			return nil, errors.New("stored memory coverage metadata is invalid")
		}
		record.TurnSequence = uint64(turnSequence)
		record.StartMessageSequence = uint64(startSequence)
		record.EndMessageSequence = uint64(endSequence)
		record.CoveredTokens = uint64(coveredTokens)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating committed SeekDB memory coverage: %w", err)
	}
	return records, nil
}

func equalExtractionProjection(left, right []personal.Retrieved) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID ||
			left[index].Kind != right[index].Kind ||
			left[index].Layer != right[index].Layer ||
			left[index].Scope != right[index].Scope ||
			left[index].Content != right[index].Content ||
			left[index].ConfidenceBasisPoints != right[index].ConfidenceBasisPoints ||
			left[index].UpdatedAtUnixMS != right[index].UpdatedAtUnixMS {
			return false
		}
	}
	return true
}
