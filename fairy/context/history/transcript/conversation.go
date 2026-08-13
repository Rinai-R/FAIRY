package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	historyexpr "fairy/context/history/expression"
	historyprojection "fairy/context/history/projection"
	"fairy/transport/session"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const conversationActivityQuery = `
SELECT c.id, c.character_id, c.created_at_ms, c.updated_at_ms,
       COALESCE(assistant_recent.messages_5m, 0),
       COALESCE(assistant_recent.messages_30m, 0),
       COALESCE(user_recent.messages_30m, 0),
       latest_assistant.created_at_ms
FROM conversations c
LEFT JOIN LATERAL (
    SELECT COUNT(*) FILTER (WHERE created_at_ms >= $3)::bigint AS messages_5m,
           COUNT(*)::bigint AS messages_30m
    FROM conversation_messages
    WHERE conversation_id = c.id
      AND role = 'assistant'
      AND created_at_ms >= $2
) assistant_recent ON true
LEFT JOIN LATERAL (
    SELECT COUNT(*)::bigint AS messages_30m
    FROM conversation_messages
    WHERE conversation_id = c.id
      AND role = 'user'
      AND created_at_ms >= $2
) user_recent ON true
LEFT JOIN LATERAL (
    SELECT created_at_ms
    FROM conversation_messages
    WHERE conversation_id = c.id
      AND role = 'assistant'
    ORDER BY created_at_ms DESC, sequence DESC
    LIMIT 1
) latest_assistant ON true
WHERE c.id = $1`

type conversationDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func recentConversationID(ctx context.Context, tx pgx.Tx, characterID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
SELECT c.id
FROM conversations c
WHERE c.character_id = $1
  AND NOT EXISTS (
    SELECT 1 FROM endpoint_conversations e WHERE e.conversation_id = c.id
  )
ORDER BY c.updated_at_ms DESC, c.id ASC
LIMIT 1`, characterID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("loading recent conversation: %w", err)
	}
	return id, nil
}

func insertConversationWithPromptWindow(ctx context.Context, tx pgx.Tx, conversationID, characterID string, now int64) error {
	if _, err := tx.Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, $3)", conversationID, characterID, now); err != nil {
		return fmt.Errorf("creating conversation: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO prompt_windows(conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms) VALUES ($1, 1, NULL, 0, $2)", conversationID, now); err != nil {
		return fmt.Errorf("creating prompt window: %w", err)
	}
	return nil
}

type EndpointConversationRow struct {
	ConversationID     string
	Audience           string
	Initiation         string
	Presentation       string
	PrincipalNamespace string
	PrincipalDigest    string
	Evaluation         bool
}

func (row EndpointConversationRow) Binding(endpoint session.EndpointKind) session.Binding {
	return session.Binding{
		Endpoint: endpoint,
		Facts: session.Facts{
			Audience:           session.AudienceKind(row.Audience),
			Initiation:         session.InitiationKind(row.Initiation),
			Presentation:       session.PresentationKind(row.Presentation),
			PrincipalNamespace: row.PrincipalNamespace,
			PrincipalDigest:    row.PrincipalDigest,
			Evaluation:         row.Evaluation,
		},
	}
}

func selectEndpointConversation(ctx context.Context, tx pgx.Tx, characterID string, endpoint session.EndpointKind, digest string) (EndpointConversationRow, bool, error) {
	var row EndpointConversationRow
	var namespace, principalDigest pgtype.Text
	err := tx.QueryRow(ctx, `
SELECT conversation_id, audience, initiation, presentation, principal_namespace, principal_digest, evaluation
FROM endpoint_conversations
WHERE character_id = $1 AND endpoint = $2 AND endpoint_key_digest = $3`,
		characterID, endpoint, digest,
	).Scan(&row.ConversationID, &row.Audience, &row.Initiation, &row.Presentation, &namespace, &principalDigest, &row.Evaluation)
	if errors.Is(err, pgx.ErrNoRows) {
		return EndpointConversationRow{}, false, nil
	}
	if err != nil {
		return EndpointConversationRow{}, false, fmt.Errorf("loading endpoint conversation: %w", err)
	}
	row.PrincipalNamespace = namespace.String
	row.PrincipalDigest = principalDigest.String
	return row, true, nil
}

func insertEndpointConversation(
	ctx context.Context,
	tx pgx.Tx,
	characterID string,
	endpoint session.EndpointKind,
	digest, conversationID string,
	audience, initiation, presentation, principalNamespace, principalDigest string,
	evaluation bool,
	now int64,
) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO endpoint_conversations(
    character_id, endpoint, endpoint_key_digest, conversation_id,
    audience, initiation, presentation, principal_namespace, principal_digest, evaluation,
    created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
		characterID, endpoint, digest, conversationID,
		audience, initiation, presentation,
		nullableText(principalNamespace), nullableText(principalDigest), evaluation, now,
	); err != nil {
		return fmt.Errorf("binding endpoint conversation: %w", err)
	}
	return nil
}

func touchEndpointConversation(ctx context.Context, tx pgx.Tx, characterID string, endpoint session.EndpointKind, digest string, now int64) error {
	if _, err := tx.Exec(ctx, `
UPDATE endpoint_conversations
SET updated_at_ms = $4
WHERE character_id = $1 AND endpoint = $2 AND endpoint_key_digest = $3`, characterID, endpoint, digest, now); err != nil {
		return fmt.Errorf("touching endpoint conversation binding: %w", err)
	}
	return nil
}

func lookupEndpointBinding(ctx context.Context, db rowQuerier, conversationID string) (session.Binding, bool, error) {
	var endpoint, audience, initiation, presentation string
	var evaluation bool
	var namespace, digest pgtype.Text
	err := db.QueryRow(ctx, `
SELECT endpoint, audience, initiation, presentation, principal_namespace, principal_digest, evaluation
FROM endpoint_conversations
WHERE conversation_id = $1`, conversationID).Scan(&endpoint, &audience, &initiation, &presentation, &namespace, &digest, &evaluation)
	if errors.Is(err, pgx.ErrNoRows) {
		return session.Binding{}, false, nil
	}
	if err != nil {
		return session.Binding{}, false, fmt.Errorf("looking up endpoint conversation: %w", err)
	}
	binding := session.Binding{
		Endpoint: session.EndpointKind(endpoint),
		Facts: session.Facts{
			Audience:           session.AudienceKind(audience),
			Initiation:         session.InitiationKind(initiation),
			Presentation:       session.PresentationKind(presentation),
			PrincipalNamespace: namespace.String,
			PrincipalDigest:    digest.String,
			Evaluation:         evaluation,
		},
	}
	if err := binding.Validate(); err != nil {
		return session.Binding{}, false, fmt.Errorf("validating stored endpoint conversation: %w", err)
	}
	return binding, true, nil
}

func loadConversationBootstrap(ctx context.Context, db conversationDB, conversationID string) (ConversationBootstrap, error) {
	conversation, prompt, boundary, err := loadConversationMetadata(ctx, db, conversationID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	rows, err := db.Query(ctx, `
SELECT m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id, m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM conversation_messages m
JOIN conversation_turns t ON t.id = m.turn_id
WHERE m.conversation_id = $1
ORDER BY m.sequence ASC`, conversationID)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading conversation messages: %w", err)
	}
	messages, err := scanConversationMessages(rows)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	return ConversationBootstrap{
		Conversation: conversation, Messages: messages, PromptWindow: prompt,
		TranscriptBoundary: boundary,
	}, nil
}

func loadConversationRecordPostgresRow(ctx context.Context, db conversationDB, conversationID string) (ConversationRecord, error) {
	return loadConversationRecord(ctx, db, conversationID)
}

func loadConversationActivityPostgresRow(ctx context.Context, db conversationDB, conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	if nowUnixMS <= 0 {
		return ConversationActivity{}, errors.New("activity evaluation time must be positive")
	}
	thirtyMinuteCutoff := max(int64(0), nowUnixMS-30*time.Minute.Milliseconds())
	fiveMinuteCutoff := max(int64(0), nowUnixMS-5*time.Minute.Milliseconds())
	var (
		activity                        ConversationActivity
		assistant5, assistant30, user30 int64
		lastAssistant                   pgtype.Int8
	)
	if err := db.QueryRow(ctx, conversationActivityQuery, conversationID, thirtyMinuteCutoff, fiveMinuteCutoff).Scan(
		&activity.Conversation.ID,
		&activity.Conversation.CharacterID,
		&activity.Conversation.CreatedAtUnixMS,
		&activity.Conversation.UpdatedAtUnixMS,
		&assistant5,
		&assistant30,
		&user30,
		&lastAssistant,
	); err != nil {
		return ConversationActivity{}, fmt.Errorf("loading conversation activity: %w", err)
	}
	if lastAssistant.Valid && lastAssistant.Int64 > nowUnixMS {
		return ConversationActivity{}, errors.New("assistant message timestamp is after activity evaluation time")
	}
	activity.AssistantMessages5Minutes = uint64(assistant5)
	activity.AssistantMessages30Minutes = uint64(assistant30)
	activity.UserMessages30Minutes = uint64(user30)
	if lastAssistant.Valid {
		value := lastAssistant.Int64
		activity.LastAssistantMessageAtUnixMS = &value
	}
	return activity, nil
}

func loadConversationPromptContextPostgresRows(ctx context.Context, db conversationDB, conversationID string) (ConversationPromptContext, error) {
	conversation, prompt, boundary, err := loadConversationMetadata(ctx, db, conversationID)
	if err != nil {
		return ConversationPromptContext{}, err
	}
	rows, err := db.Query(ctx, `
SELECT m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id, m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM conversation_messages m
JOIN conversation_turns t ON t.id = m.turn_id
WHERE m.conversation_id = $1 AND m.sequence > $2
ORDER BY m.sequence ASC`, conversationID, int64(prompt.CutoffMessageSequence))
	if err != nil {
		return ConversationPromptContext{}, fmt.Errorf("loading conversation prompt messages: %w", err)
	}
	messages, err := scanConversationMessages(rows)
	if err != nil {
		return ConversationPromptContext{}, err
	}
	messages = applyPromptProjection(messages, prompt.Projection)
	return ConversationPromptContext{
		Conversation: conversation, Messages: messages, PromptWindow: prompt,
		TranscriptBoundary: boundary,
	}, nil
}

func loadConversationMetadata(ctx context.Context, db conversationDB, conversationID string) (ConversationRecord, PromptWindowRecord, TranscriptBoundary, error) {
	conversation, err := loadConversationRecord(ctx, db, conversationID)
	if err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, TranscriptBoundary{}, err
	}
	var prompt PromptWindowRecord
	var boundary TranscriptBoundary
	var summary pgtype.Text
	var promptRevision int64
	var projectionRevision int64
	var projectionJSON []byte
	var cutoffSequence int64
	var turnSequence, messageSequence int64
	if err := db.QueryRow(ctx, `
SELECT pw.conversation_id, pw.revision, pw.summary, pw.cutoff_message_sequence,
       pw.projection_revision, pw.projection_state, pw.updated_at_ms,
       COALESCE((SELECT MAX(t.sequence) FROM conversation_turns t WHERE t.conversation_id = pw.conversation_id), 0),
       COALESCE((SELECT MAX(m.sequence) FROM conversation_messages m WHERE m.conversation_id = pw.conversation_id), 0)
FROM prompt_windows pw
WHERE pw.conversation_id = $1`, conversationID).Scan(
		&prompt.ConversationID, &promptRevision, &summary, &cutoffSequence,
		&projectionRevision, &projectionJSON, &prompt.UpdatedAtUnixMS,
		&turnSequence, &messageSequence,
	); err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, TranscriptBoundary{}, fmt.Errorf("loading prompt window: %w", err)
	}
	if turnSequence < 0 || messageSequence < 0 {
		return ConversationRecord{}, PromptWindowRecord{}, TranscriptBoundary{}, errors.New("stored transcript boundary is invalid")
	}
	prompt.Revision = uint64(promptRevision)
	prompt.CutoffMessageSequence = uint64(cutoffSequence)
	prompt.ProjectionRevision = uint64(projectionRevision)
	prompt.Projection, err = historyprojection.Decode(projectionJSON)
	if err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, TranscriptBoundary{}, err
	}
	if summary.Valid {
		prompt.Summary = &summary.String
	}
	boundary.TurnSequence = uint64(turnSequence)
	boundary.MessageSequence = uint64(messageSequence)
	return conversation, prompt, boundary, nil
}

func loadConversationRecord(ctx context.Context, db conversationDB, conversationID string) (ConversationRecord, error) {
	var conversation ConversationRecord
	if err := db.QueryRow(ctx, "SELECT id, character_id, created_at_ms, updated_at_ms FROM conversations WHERE id = $1", conversationID).Scan(&conversation.ID, &conversation.CharacterID, &conversation.CreatedAtUnixMS, &conversation.UpdatedAtUnixMS); err != nil {
		return ConversationRecord{}, fmt.Errorf("loading conversation: %w", err)
	}
	return conversation, nil
}

func scanConversationMessages(rows pgx.Rows) ([]MessageRecord, error) {
	defer rows.Close()
	messages := make([]MessageRecord, 0)
	for rows.Next() {
		message, err := scanMessageRecord(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating conversation messages: %w", err)
	}
	return messages, nil
}

func requireConversationPostgres(ctx context.Context, tx pgx.Tx, conversationID string) error {
	var exists int
	err := tx.QueryRow(ctx, "SELECT 1 FROM conversations WHERE id = $1 FOR UPDATE", conversationID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("conversation does not exist")
	}
	if err != nil {
		return fmt.Errorf("checking conversation: %w", err)
	}
	return nil
}

func nextSequencePostgres(ctx context.Context, tx pgx.Tx, table string, conversationID string) (int64, error) {
	if table != "conversation_turns" && table != "conversation_messages" {
		return 0, fmt.Errorf("reading next sequence from unsupported table %q", table)
	}
	var maxSequence int64
	query := "SELECT COALESCE(MAX(sequence), 0) FROM " + table + " WHERE conversation_id = $1"
	if err := tx.QueryRow(ctx, query, conversationID).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("reading next sequence from %s: %w", table, err)
	}
	return maxSequence + 1, nil
}

func touchConversationPostgres(ctx context.Context, tx pgx.Tx, conversationID string, now int64) error {
	if _, err := tx.Exec(ctx, "UPDATE conversations SET updated_at_ms = $2 WHERE id = $1", conversationID, now); err != nil {
		return fmt.Errorf("touching conversation: %w", err)
	}
	return nil
}

func insertUserTurnPostgres(
	ctx context.Context,
	tx pgx.Tx,
	turnID, conversationID, correlationMessageID, messageID, userMessage string,
	turnSequence, messageSequence, now int64,
) error {
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_turns(id, conversation_id, message_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, $4, 'interpreting', 'user', 'ineligible', $5, $5)", turnID, conversationID, nullableText(correlationMessageID), turnSequence, now); err != nil {
		return fmt.Errorf("creating turn: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'user', $5, $6)", messageID, conversationID, turnID, messageSequence, userMessage, now); err != nil {
		return fmt.Errorf("writing user message: %w", err)
	}
	return nil
}

func insertInitiationTurnPostgres(ctx context.Context, tx pgx.Tx, turnID, conversationID string, turnSequence, now int64, evidenceIDs []string) error {
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, 'interpreting', 'desktop_initiation', 'ineligible', $4, $4)", turnID, conversationID, turnSequence, now); err != nil {
		return fmt.Errorf("creating initiation turn: %w", err)
	}
	for _, evidenceID := range evidenceIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms) VALUES ($1, $2, $3)", turnID, evidenceID, now); err != nil {
			return fmt.Errorf("linking initiation evidence: %w", err)
		}
	}
	return nil
}

func completeTurnPostgresTx(
	ctx context.Context,
	tx pgx.Tx,
	turnID, conversationID, messageID, assistantMessage string,
	expressionPartsJSON []byte,
	messageSequence, now int64,
	extractionEligible bool,
) error {
	changed, err := tx.Exec(ctx, "UPDATE conversation_turns SET status = 'completed', extraction_state = CASE WHEN origin = 'desktop_initiation' OR NOT $4 THEN 'ineligible' ELSE 'pending' END, updated_at_ms = $3 WHERE id = $1 AND conversation_id = $2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, now, extractionEligible)
	if err != nil {
		return fmt.Errorf("updating turn completion: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("turn does not belong to conversation or is terminal")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms) VALUES ($1, $2, $3, $4, 'assistant', $5, $6, $7)", messageID, conversationID, turnID, messageSequence, assistantMessage, expressionPartsJSON, now); err != nil {
		return fmt.Errorf("writing assistant message: %w", err)
	}
	return nil
}

func interruptTurnPostgresTx(ctx context.Context, tx pgx.Tx, turnID, conversationID string, now int64) error {
	changed, err := tx.Exec(ctx, `
UPDATE conversation_turns
SET status = 'interrupted',
    extraction_state = 'ineligible',
    error_code = NULL,
    error_message = NULL,
    error_retryable = NULL,
    updated_at_ms = $3
WHERE id = $1
  AND conversation_id = $2
  AND status IN ('interpreting', 'planning', 'responding')`, turnID, conversationID, now)
	if err != nil {
		return fmt.Errorf("updating interrupted turn: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("turn does not belong to conversation or is terminal")
	}
	return nil
}

func insertAssistantMessagePostgres(ctx context.Context, tx pgx.Tx, messageID, conversationID, turnID, content string, expressionPartsJSON []byte, messageSequence, now int64) error {
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms) VALUES ($1, $2, $3, $4, 'assistant', $5, $6, $7)", messageID, conversationID, turnID, messageSequence, content, expressionPartsJSON, now); err != nil {
		return fmt.Errorf("writing interrupted assistant prefix: %w", err)
	}
	return nil
}

func failTurnPostgresExec(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, turnID, conversationID, code, message string, retryable bool, now int64) error {
	changed, err := db.Exec(ctx, "UPDATE conversation_turns SET status = 'failed', extraction_state = 'ineligible', error_code = $3, error_message = $4, error_retryable = $5, updated_at_ms = $6 WHERE id = $1 AND conversation_id = $2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, code, message, retryable, now)
	if err != nil {
		return fmt.Errorf("marking turn failed: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("turn does not belong to conversation or is terminal")
	}
	return nil
}

func scanMessageRecord(row scanner) (MessageRecord, error) {
	var message MessageRecord
	var sequence int64
	var expressionPartsJSON []byte
	if err := row.Scan(&message.ID, &message.MessageID, &message.ConversationID, &message.TurnID, &sequence, &message.Role, &message.Content, &expressionPartsJSON, &message.CreatedAtUnixMS); err != nil {
		return MessageRecord{}, fmt.Errorf("scanning conversation message: %w", err)
	}
	return finishScannedMessage(message, sequence, expressionPartsJSON)
}

func finishScannedMessage(message MessageRecord, sequence int64, expressionPartsJSON []byte) (MessageRecord, error) {
	if err := json.Unmarshal(expressionPartsJSON, &message.Parts); err != nil {
		return MessageRecord{}, fmt.Errorf("decoding conversation message expression parts: %w", err)
	}
	if message.Parts == nil {
		message.Parts = []historyexpr.Part{}
	}
	if message.Role == "assistant" && len(message.Parts) > 0 {
		if err := validateExpressionMessage(message.Content, message.Parts); err != nil {
			return MessageRecord{}, fmt.Errorf("validating conversation message expression parts: %w", err)
		}
	}
	message.Sequence = uint64(sequence)
	return message, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
