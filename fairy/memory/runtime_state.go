package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	PromptLaneRespond = "respond"
	PromptLaneCompact = "compact"
	PromptLaneExtract = "extract"

	maxRuntimeMetadataBytes = 32 * 1024
	maxRuntimeTokenRunes    = 128
	maxSQLiteInteger        = uint64(1<<63 - 1)
)

type TurnRuntimeEventInput struct {
	ConversationID string  `json:"conversationId"`
	TurnID         string  `json:"turnId"`
	EventType      string  `json:"eventType"`
	State          *string `json:"state,omitempty"`
	Code           *string `json:"code,omitempty"`
	MetadataJSON   string  `json:"metadataJson"`
}

type TurnRuntimeEventRecord struct {
	ID              string  `json:"id"`
	ConversationID  string  `json:"conversationId"`
	TurnID          string  `json:"turnId"`
	Sequence        uint64  `json:"sequence"`
	EventType       string  `json:"eventType"`
	State           *string `json:"state,omitempty"`
	Code            *string `json:"code,omitempty"`
	MetadataJSON    string  `json:"metadataJson"`
	CreatedAtUnixMS int64   `json:"createdAtUnixMs"`
}

type LaneContinuationRecord struct {
	ConversationID     string `json:"conversationId"`
	Lane               string `json:"lane"`
	PreviousResponseID string `json:"previousResponseId"`
	RequestShapeHash   string `json:"requestShapeHash"`
	InputPrefixHash    string `json:"inputPrefixHash"`
	ResponseItemHash   string `json:"responseItemHash"`
	WindowRevision     uint64 `json:"windowRevision"`
	UpdatedAtUnixMS    int64  `json:"updatedAtUnixMs"`
}

type ContextWindowRecord struct {
	ConversationID         string  `json:"conversationId"`
	Lane                   string  `json:"lane"`
	WindowNumber           uint64  `json:"windowNumber"`
	FirstWindowID          string  `json:"firstWindowId"`
	PreviousWindowID       *string `json:"previousWindowId,omitempty"`
	WindowID               string  `json:"windowId"`
	ObservedPrefillTokens  *uint64 `json:"observedPrefillTokens,omitempty"`
	EstimatedPrefillTokens *uint64 `json:"estimatedPrefillTokens,omitempty"`
	LastTrigger            string  `json:"lastTrigger"`
	FailureCount           uint64  `json:"failureCount"`
	PromptWindowRevision   uint64  `json:"promptWindowRevision"`
	UpdatedAtUnixMS        int64   `json:"updatedAtUnixMs"`
}

func (s *Store) AppendTurnRuntimeEvent(input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	if err := validateTurnRuntimeEventInput(input); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	metadataJSON, err := normalizeRuntimeMetadataJSON(input.MetadataJSON)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("beginning runtime event transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireTurn(tx, input.ConversationID, input.TurnID); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	sequence, err := nextTurnRuntimeEventSequence(tx, input.ConversationID, input.TurnID)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	id := newID()
	if _, err := tx.Exec("INSERT INTO turn_runtime_events(id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)", id, input.ConversationID, input.TurnID, sequence, input.EventType, nullableStringValue(input.State), nullableStringValue(input.Code), metadataJSON, now); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("appending runtime event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("committing runtime event transaction: %w", err)
	}
	return TurnRuntimeEventRecord{
		ID:              id,
		ConversationID:  input.ConversationID,
		TurnID:          input.TurnID,
		Sequence:        uint64(sequence),
		EventType:       input.EventType,
		State:           cloneStringPtr(input.State),
		Code:            cloneStringPtr(input.Code),
		MetadataJSON:    metadataJSON,
		CreatedAtUnixMS: now,
	}, nil
}

func (s *Store) ListTurnRuntimeEvents(conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return nil, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms FROM turn_runtime_events WHERE conversation_id = ?1 AND turn_id = ?2 ORDER BY sequence ASC", conversationID, turnID)
	if err != nil {
		return nil, fmt.Errorf("listing runtime events: %w", err)
	}
	defer rows.Close()
	records := make([]TurnRuntimeEventRecord, 0)
	for rows.Next() {
		var record TurnRuntimeEventRecord
		var state sql.NullString
		var code sql.NullString
		if err := rows.Scan(&record.ID, &record.ConversationID, &record.TurnID, &record.Sequence, &record.EventType, &state, &code, &record.MetadataJSON, &record.CreatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning runtime event: %w", err)
		}
		record.State = stringPtrFromNull(state)
		record.Code = stringPtrFromNull(code)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating runtime events: %w", err)
	}
	return records, nil
}

func (s *Store) SaveLaneContinuation(record LaneContinuationRecord) (LaneContinuationRecord, error) {
	if err := validateLaneContinuation(record); err != nil {
		return LaneContinuationRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return LaneContinuationRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("beginning lane continuation transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireConversation(tx, record.ConversationID); err != nil {
		return LaneContinuationRecord{}, err
	}
	windowRevision, err := sqliteUint64("window_revision", record.WindowRevision)
	if err != nil {
		return LaneContinuationRecord{}, err
	}
	if _, err := tx.Exec("INSERT INTO lane_continuations(conversation_id, lane, previous_response_id, request_shape_hash, input_prefix_hash, response_item_hash, window_revision, updated_at_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8) ON CONFLICT(conversation_id, lane) DO UPDATE SET previous_response_id = excluded.previous_response_id, request_shape_hash = excluded.request_shape_hash, input_prefix_hash = excluded.input_prefix_hash, response_item_hash = excluded.response_item_hash, window_revision = excluded.window_revision, updated_at_ms = excluded.updated_at_ms", record.ConversationID, record.Lane, record.PreviousResponseID, record.RequestShapeHash, record.InputPrefixHash, record.ResponseItemHash, windowRevision, now); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("saving lane continuation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("committing lane continuation transaction: %w", err)
	}
	record.UpdatedAtUnixMS = now
	return record, nil
}

func (s *Store) LoadLaneContinuation(conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return LaneContinuationRecord{}, false, err
	}
	defer db.Close()
	var record LaneContinuationRecord
	err = db.QueryRow("SELECT conversation_id, lane, previous_response_id, request_shape_hash, input_prefix_hash, response_item_hash, window_revision, updated_at_ms FROM lane_continuations WHERE conversation_id = ?1 AND lane = ?2", conversationID, lane).Scan(&record.ConversationID, &record.Lane, &record.PreviousResponseID, &record.RequestShapeHash, &record.InputPrefixHash, &record.ResponseItemHash, &record.WindowRevision, &record.UpdatedAtUnixMS)
	if errors.Is(err, sql.ErrNoRows) {
		return LaneContinuationRecord{}, false, nil
	}
	if err != nil {
		return LaneContinuationRecord{}, false, fmt.Errorf("loading lane continuation: %w", err)
	}
	return record, true, nil
}

func (s *Store) ClearLaneContinuation(conversationID string, lane string) error {
	if err := validateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validatePromptLane(lane); err != nil {
		return err
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning lane continuation clear transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireConversation(tx, conversationID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM lane_continuations WHERE conversation_id = ?1 AND lane = ?2", conversationID, lane); err != nil {
		return fmt.Errorf("clearing lane continuation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing lane continuation clear transaction: %w", err)
	}
	return nil
}

func (s *Store) SaveContextWindow(record ContextWindowRecord) (ContextWindowRecord, error) {
	if err := validateContextWindow(record); err != nil {
		return ContextWindowRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return ContextWindowRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return ContextWindowRecord{}, fmt.Errorf("beginning context window transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireConversation(tx, record.ConversationID); err != nil {
		return ContextWindowRecord{}, err
	}
	windowNumber, err := sqliteUint64("window_number", record.WindowNumber)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	failureCount, err := sqliteUint64("failure_count", record.FailureCount)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	promptWindowRevision, err := sqliteUint64("prompt_window_revision", record.PromptWindowRevision)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	observedPrefillTokens, err := nullableSQLiteUint64("observed_prefill_tokens", record.ObservedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	estimatedPrefillTokens, err := nullableSQLiteUint64("estimated_prefill_tokens", record.EstimatedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, err
	}
	if _, err := tx.Exec("INSERT INTO context_windows(conversation_id, lane, window_number, first_window_id, previous_window_id, window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger, failure_count, prompt_window_revision, updated_at_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12) ON CONFLICT(conversation_id, lane) DO UPDATE SET window_number = excluded.window_number, first_window_id = excluded.first_window_id, previous_window_id = excluded.previous_window_id, window_id = excluded.window_id, observed_prefill_tokens = excluded.observed_prefill_tokens, estimated_prefill_tokens = excluded.estimated_prefill_tokens, last_trigger = excluded.last_trigger, failure_count = excluded.failure_count, prompt_window_revision = excluded.prompt_window_revision, updated_at_ms = excluded.updated_at_ms", record.ConversationID, record.Lane, windowNumber, record.FirstWindowID, nullableStringValue(record.PreviousWindowID), record.WindowID, observedPrefillTokens, estimatedPrefillTokens, record.LastTrigger, failureCount, promptWindowRevision, now); err != nil {
		return ContextWindowRecord{}, fmt.Errorf("saving context window: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ContextWindowRecord{}, fmt.Errorf("committing context window transaction: %w", err)
	}
	record.UpdatedAtUnixMS = now
	return record, nil
}

func (s *Store) LoadContextWindow(conversationID string, lane string) (ContextWindowRecord, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return ContextWindowRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return ContextWindowRecord{}, false, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return ContextWindowRecord{}, false, err
	}
	defer db.Close()
	var record ContextWindowRecord
	var previousWindowID sql.NullString
	var observedPrefillTokens sql.NullInt64
	var estimatedPrefillTokens sql.NullInt64
	err = db.QueryRow("SELECT conversation_id, lane, window_number, first_window_id, previous_window_id, window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger, failure_count, prompt_window_revision, updated_at_ms FROM context_windows WHERE conversation_id = ?1 AND lane = ?2", conversationID, lane).Scan(&record.ConversationID, &record.Lane, &record.WindowNumber, &record.FirstWindowID, &previousWindowID, &record.WindowID, &observedPrefillTokens, &estimatedPrefillTokens, &record.LastTrigger, &record.FailureCount, &record.PromptWindowRevision, &record.UpdatedAtUnixMS)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextWindowRecord{}, false, nil
	}
	if err != nil {
		return ContextWindowRecord{}, false, fmt.Errorf("loading context window: %w", err)
	}
	record.PreviousWindowID = stringPtrFromNull(previousWindowID)
	observed, err := uintPtrFromNullInt64("observed_prefill_tokens", observedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, false, err
	}
	estimated, err := uintPtrFromNullInt64("estimated_prefill_tokens", estimatedPrefillTokens)
	if err != nil {
		return ContextWindowRecord{}, false, err
	}
	record.ObservedPrefillTokens = observed
	record.EstimatedPrefillTokens = estimated
	return record, true, nil
}

func validateTurnRuntimeEventInput(input TurnRuntimeEventInput) error {
	if err := validateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := validateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if err := validateRuntimeToken("event_type", input.EventType); err != nil {
		return err
	}
	if input.State != nil {
		if err := validateRuntimeToken("state", *input.State); err != nil {
			return err
		}
	}
	if input.Code != nil {
		if err := validateRuntimeToken("code", *input.Code); err != nil {
			return err
		}
	}
	return nil
}

func validateLaneContinuation(record LaneContinuationRecord) error {
	if err := validateID("conversation_id", record.ConversationID); err != nil {
		return err
	}
	if err := validatePromptLane(record.Lane); err != nil {
		return err
	}
	if err := validateRuntimeToken("previous_response_id", record.PreviousResponseID); err != nil {
		return err
	}
	if err := validateHash("request_shape_hash", record.RequestShapeHash); err != nil {
		return err
	}
	if err := validateHash("input_prefix_hash", record.InputPrefixHash); err != nil {
		return err
	}
	if err := validateHash("response_item_hash", record.ResponseItemHash); err != nil {
		return err
	}
	if record.WindowRevision == 0 {
		return errors.New("window_revision is required")
	}
	if _, err := sqliteUint64("window_revision", record.WindowRevision); err != nil {
		return err
	}
	return nil
}

func validateContextWindow(record ContextWindowRecord) error {
	if err := validateID("conversation_id", record.ConversationID); err != nil {
		return err
	}
	if err := validatePromptLane(record.Lane); err != nil {
		return err
	}
	if err := validateID("first_window_id", record.FirstWindowID); err != nil {
		return err
	}
	if record.PreviousWindowID != nil {
		if err := validateID("previous_window_id", *record.PreviousWindowID); err != nil {
			return err
		}
	}
	if err := validateID("window_id", record.WindowID); err != nil {
		return err
	}
	if record.PromptWindowRevision == 0 {
		return errors.New("prompt_window_revision is required")
	}
	if _, err := sqliteUint64("window_number", record.WindowNumber); err != nil {
		return err
	}
	if _, err := sqliteUint64("failure_count", record.FailureCount); err != nil {
		return err
	}
	if _, err := sqliteUint64("prompt_window_revision", record.PromptWindowRevision); err != nil {
		return err
	}
	if _, err := nullableSQLiteUint64("observed_prefill_tokens", record.ObservedPrefillTokens); err != nil {
		return err
	}
	if _, err := nullableSQLiteUint64("estimated_prefill_tokens", record.EstimatedPrefillTokens); err != nil {
		return err
	}
	if err := validateRuntimeToken("last_trigger", record.LastTrigger); err != nil {
		return err
	}
	return nil
}

func validatePromptLane(lane string) error {
	if lane == "" || strings.TrimSpace(lane) != lane || containsDisallowedControl(lane) {
		return errors.New("lane is invalid")
	}
	switch lane {
	case PromptLaneRespond, PromptLaneCompact, PromptLaneExtract:
		return nil
	default:
		return fmt.Errorf("unsupported prompt lane: %q", lane)
	}
}

func validateHash(label string, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", label)
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return fmt.Errorf("%s must be lowercase sha256 hex", label)
	}
	return nil
}

func validateRuntimeToken(label string, value string) error {
	if value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value) || len([]rune(value)) > maxRuntimeTokenRunes {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func normalizeRuntimeMetadataJSON(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) == "" {
		return "", errors.New("metadata_json is required")
	}
	if len(value) > maxRuntimeMetadataBytes {
		return "", errors.New("metadata_json is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", fmt.Errorf("metadata_json must be valid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("metadata_json must contain a single JSON value")
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return "", errors.New("metadata_json must be a JSON object")
	}
	if err := rejectForbiddenMetadata(object); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("encoding metadata_json: %w", err)
	}
	return string(encoded), nil
}

func rejectForbiddenMetadata(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isForbiddenMetadataKey(key) {
				return fmt.Errorf("metadata_json contains forbidden key %q", key)
			}
			if err := rejectForbiddenMetadata(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectForbiddenMetadata(child); err != nil {
				return err
			}
		}
	case string:
		normalized := strings.ToLower(typed)
		if strings.Contains(normalized, "authorization:") || strings.Contains(normalized, "bearer ") {
			return errors.New("metadata_json contains secret-like text")
		}
	}
	return nil
}

func isForbiddenMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	switch normalized {
	case "decision", "stance", "replyintent", "tone", "relationshipsignal", "replymode", "reasoning", "analysis", "rationale", "authorization", "apikey", "xapikey", "accesstoken", "refreshtoken", "secret", "bearer":
		return true
	default:
		return false
	}
}

func requireTurn(tx *sql.Tx, conversationID string, turnID string) error {
	var exists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM conversation_turns WHERE conversation_id = ?1 AND id = ?2)", conversationID, turnID).Scan(&exists); err != nil {
		return fmt.Errorf("checking turn: %w", err)
	}
	if !exists {
		return errors.New("turn does not belong to conversation")
	}
	return nil
}

func nextTurnRuntimeEventSequence(tx *sql.Tx, conversationID string, turnID string) (int64, error) {
	var maxSequence int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(sequence), 0) FROM turn_runtime_events WHERE conversation_id = ?1 AND turn_id = ?2", conversationID, turnID).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("reading next runtime event sequence: %w", err)
	}
	return maxSequence + 1, nil
}

func sqliteUint64(label string, value uint64) (int64, error) {
	if value > maxSQLiteInteger {
		return 0, fmt.Errorf("%s exceeds SQLite integer range", label)
	}
	return int64(value), nil
}

func nullableSQLiteUint64(label string, value *uint64) (any, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := sqliteUint64(label, *value)
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func uintPtrFromNullInt64(label string, value sql.NullInt64) (*uint64, error) {
	if !value.Valid {
		return nil, nil
	}
	if value.Int64 < 0 {
		return nil, fmt.Errorf("%s is negative", label)
	}
	converted := uint64(value.Int64)
	return &converted, nil
}

func nullableStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func stringPtrFromNull(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
