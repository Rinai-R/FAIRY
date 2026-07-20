package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	PromptLaneRespond   = "respond"
	PromptLaneCompact   = "compact"
	PromptLaneExtract   = "extract"
	PromptLaneTranslate = "translate"

	maxRuntimeMetadataBytes = 32 * 1024
	maxRuntimeTokenRunes    = 128
	maxDatabaseInteger      = uint64(1<<63 - 1)
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
	return s.AppendTurnRuntimeEventContext(context.Background(), input)
}

func (s *Store) AppendTurnRuntimeEventContext(ctx context.Context, input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	return s.appendTurnRuntimeEventPostgres(ctx, input)
}

func (s *Store) ListTurnRuntimeEvents(conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	return s.ListTurnRuntimeEventsContext(context.Background(), conversationID, turnID)
}

func (s *Store) ListTurnRuntimeEventsContext(ctx context.Context, conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	return s.listTurnRuntimeEventsPostgres(ctx, conversationID, turnID)
}

func (s *Store) SaveLaneContinuation(record LaneContinuationRecord) (LaneContinuationRecord, error) {
	return s.SaveLaneContinuationContext(context.Background(), record)
}

func (s *Store) SaveLaneContinuationContext(ctx context.Context, record LaneContinuationRecord) (LaneContinuationRecord, error) {
	return s.saveLaneContinuationPostgres(ctx, record)
}

func (s *Store) LoadLaneContinuation(conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	return s.LoadLaneContinuationContext(context.Background(), conversationID, lane)
}

func (s *Store) LoadLaneContinuationContext(ctx context.Context, conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	return s.loadLaneContinuationPostgres(ctx, conversationID, lane)
}

func (s *Store) ClearLaneContinuation(conversationID string, lane string) error {
	return s.ClearLaneContinuationContext(context.Background(), conversationID, lane)
}

func (s *Store) ClearLaneContinuationContext(ctx context.Context, conversationID string, lane string) error {
	return s.clearLaneContinuationPostgres(ctx, conversationID, lane)
}

func (s *Store) SaveContextWindow(record ContextWindowRecord) (ContextWindowRecord, error) {
	return s.SaveContextWindowContext(context.Background(), record)
}

func (s *Store) SaveContextWindowContext(ctx context.Context, record ContextWindowRecord) (ContextWindowRecord, error) {
	return s.saveContextWindowPostgres(ctx, record)
}

func (s *Store) LoadContextWindow(conversationID string, lane string) (ContextWindowRecord, bool, error) {
	return s.LoadContextWindowContext(context.Background(), conversationID, lane)
}

func (s *Store) LoadContextWindowContext(ctx context.Context, conversationID string, lane string) (ContextWindowRecord, bool, error) {
	return s.loadContextWindowPostgres(ctx, conversationID, lane)
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
	if _, err := databaseInt64("window_revision", record.WindowRevision); err != nil {
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
	if _, err := databaseInt64("window_number", record.WindowNumber); err != nil {
		return err
	}
	if _, err := databaseInt64("failure_count", record.FailureCount); err != nil {
		return err
	}
	if _, err := databaseInt64("prompt_window_revision", record.PromptWindowRevision); err != nil {
		return err
	}
	if _, err := nullableDatabaseInt64("observed_prefill_tokens", record.ObservedPrefillTokens); err != nil {
		return err
	}
	if _, err := nullableDatabaseInt64("estimated_prefill_tokens", record.EstimatedPrefillTokens); err != nil {
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
	case PromptLaneRespond, PromptLaneCompact, PromptLaneExtract, PromptLaneTranslate:
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

func databaseInt64(label string, value uint64) (int64, error) {
	if value > maxDatabaseInteger {
		return 0, fmt.Errorf("%s exceeds database integer range", label)
	}
	return int64(value), nil
}

func nullableDatabaseInt64(label string, value *uint64) (any, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := databaseInt64(label, *value)
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func nullableStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
