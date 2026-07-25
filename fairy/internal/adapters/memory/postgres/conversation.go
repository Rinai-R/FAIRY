package postgres

import (
	"context"
	"errors"
	"fmt"

	contracts "fairy/contracts/interaction"
	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type ConversationDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func RecentConversationID(ctx context.Context, tx pgx.Tx, characterID string) (string, error) {
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

func InsertConversationWithPromptWindow(ctx context.Context, tx pgx.Tx, conversationID, characterID string, now int64) error {
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
}

func (row EndpointConversationRow) Binding(endpoint contracts.EndpointKind) contracts.Binding {
	return contracts.Binding{
		Endpoint: endpoint,
		Facts: contracts.Facts{
			Audience:           contracts.AudienceKind(row.Audience),
			Initiation:         contracts.InitiationKind(row.Initiation),
			Presentation:       contracts.PresentationKind(row.Presentation),
			PrincipalNamespace: row.PrincipalNamespace,
			PrincipalDigest:    row.PrincipalDigest,
		},
	}
}

func SelectEndpointConversation(ctx context.Context, tx pgx.Tx, characterID string, endpoint contracts.EndpointKind, digest string) (EndpointConversationRow, bool, error) {
	var row EndpointConversationRow
	var namespace, principalDigest pgtype.Text
	err := tx.QueryRow(ctx, `
SELECT conversation_id, audience, initiation, presentation, principal_namespace, principal_digest
FROM endpoint_conversations
WHERE character_id = $1 AND endpoint = $2 AND endpoint_key_digest = $3`,
		characterID, endpoint, digest,
	).Scan(&row.ConversationID, &row.Audience, &row.Initiation, &row.Presentation, &namespace, &principalDigest)
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

func InsertEndpointConversation(
	ctx context.Context,
	tx pgx.Tx,
	characterID string,
	endpoint contracts.EndpointKind,
	digest, conversationID string,
	audience, initiation, presentation, principalNamespace, principalDigest string,
	now int64,
) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO endpoint_conversations(
    character_id, endpoint, endpoint_key_digest, conversation_id,
    audience, initiation, presentation, principal_namespace, principal_digest,
    created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
		characterID, endpoint, digest, conversationID,
		audience, initiation, presentation,
		nullableText(principalNamespace), nullableText(principalDigest), now,
	); err != nil {
		return fmt.Errorf("binding endpoint conversation: %w", err)
	}
	return nil
}

func TouchEndpointConversation(ctx context.Context, tx pgx.Tx, characterID string, endpoint contracts.EndpointKind, digest string, now int64) error {
	if _, err := tx.Exec(ctx, `
UPDATE endpoint_conversations
SET updated_at_ms = $4
WHERE character_id = $1 AND endpoint = $2 AND endpoint_key_digest = $3`, characterID, endpoint, digest, now); err != nil {
		return fmt.Errorf("touching endpoint conversation binding: %w", err)
	}
	return nil
}

func LookupEndpointBinding(ctx context.Context, db RowQuerier, conversationID string) (contracts.Binding, bool, error) {
	var endpoint, audience, initiation, presentation string
	var namespace, digest pgtype.Text
	err := db.QueryRow(ctx, `
SELECT endpoint, audience, initiation, presentation, principal_namespace, principal_digest
FROM endpoint_conversations
WHERE conversation_id = $1`, conversationID).Scan(&endpoint, &audience, &initiation, &presentation, &namespace, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Binding{}, false, nil
	}
	if err != nil {
		return contracts.Binding{}, false, fmt.Errorf("looking up endpoint conversation: %w", err)
	}
	binding := contracts.Binding{
		Endpoint: contracts.EndpointKind(endpoint),
		Facts: contracts.Facts{
			Audience:           contracts.AudienceKind(audience),
			Initiation:         contracts.InitiationKind(initiation),
			Presentation:       contracts.PresentationKind(presentation),
			PrincipalNamespace: namespace.String,
			PrincipalDigest:    digest.String,
		},
	}
	if err := binding.Validate(); err != nil {
		return contracts.Binding{}, false, fmt.Errorf("validating stored endpoint conversation: %w", err)
	}
	return binding, true, nil
}

func LoadConversationBootstrap(ctx context.Context, db ConversationDB, conversationID string) (domainmemory.ConversationBootstrap, error) {
	var conversation domainmemory.ConversationRecord
	if err := db.QueryRow(ctx, "SELECT id, character_id, created_at_ms, updated_at_ms FROM conversations WHERE id = $1", conversationID).Scan(&conversation.ID, &conversation.CharacterID, &conversation.CreatedAtUnixMS, &conversation.UpdatedAtUnixMS); err != nil {
		return domainmemory.ConversationBootstrap{}, fmt.Errorf("loading conversation: %w", err)
	}
	var prompt domainmemory.PromptWindowRecord
	var summary pgtype.Text
	var promptRevision int64
	var cutoffSequence int64
	if err := db.QueryRow(ctx, "SELECT conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms FROM prompt_windows WHERE conversation_id = $1", conversationID).Scan(&prompt.ConversationID, &promptRevision, &summary, &cutoffSequence, &prompt.UpdatedAtUnixMS); err != nil {
		return domainmemory.ConversationBootstrap{}, fmt.Errorf("loading prompt window: %w", err)
	}
	prompt.Revision = uint64(promptRevision)
	prompt.CutoffMessageSequence = uint64(cutoffSequence)
	if summary.Valid {
		prompt.Summary = &summary.String
	}
	rows, err := db.Query(ctx, "SELECT id, conversation_id, turn_id, sequence, role, content, created_at_ms FROM conversation_messages WHERE conversation_id = $1 ORDER BY sequence ASC", conversationID)
	if err != nil {
		return domainmemory.ConversationBootstrap{}, fmt.Errorf("loading conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]domainmemory.MessageRecord, 0)
	for rows.Next() {
		message, err := ScanMessageRecord(rows)
		if err != nil {
			return domainmemory.ConversationBootstrap{}, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return domainmemory.ConversationBootstrap{}, fmt.Errorf("iterating conversation messages: %w", err)
	}
	return domainmemory.ConversationBootstrap{Conversation: conversation, Messages: messages, PromptWindow: prompt}, nil
}

func RequireConversation(ctx context.Context, tx pgx.Tx, conversationID string) error {
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

func NextSequence(ctx context.Context, tx pgx.Tx, table string, conversationID string) (int64, error) {
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

func TouchConversation(ctx context.Context, tx pgx.Tx, conversationID string, now int64) error {
	if _, err := tx.Exec(ctx, "UPDATE conversations SET updated_at_ms = $2 WHERE id = $1", conversationID, now); err != nil {
		return fmt.Errorf("touching conversation: %w", err)
	}
	return nil
}

func InsertUserTurn(
	ctx context.Context,
	tx pgx.Tx,
	turnID, conversationID, messageID, userMessage string,
	turnSequence, messageSequence, now int64,
) error {
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, 'interpreting', 'user', 'ineligible', $4, $4)", turnID, conversationID, turnSequence, now); err != nil {
		return fmt.Errorf("creating turn: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'user', $5, $6)", messageID, conversationID, turnID, messageSequence, userMessage, now); err != nil {
		return fmt.Errorf("writing user message: %w", err)
	}
	return nil
}

func InsertInitiationTurn(ctx context.Context, tx pgx.Tx, turnID, conversationID string, turnSequence, now int64, evidenceIDs []string) error {
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

func CompleteTurn(
	ctx context.Context,
	tx pgx.Tx,
	turnID, conversationID, messageID, assistantMessage string,
	messageSequence, now int64,
) error {
	changed, err := tx.Exec(ctx, "UPDATE conversation_turns SET status = 'completed', extraction_state = CASE WHEN origin = 'desktop_initiation' THEN 'ineligible' ELSE 'pending' END, updated_at_ms = $3 WHERE id = $1 AND conversation_id = $2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, now)
	if err != nil {
		return fmt.Errorf("updating turn completion: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("turn does not belong to conversation or is terminal")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'assistant', $5, $6)", messageID, conversationID, turnID, messageSequence, assistantMessage, now); err != nil {
		return fmt.Errorf("writing assistant message: %w", err)
	}
	return nil
}

func InterruptTurn(ctx context.Context, tx pgx.Tx, turnID, conversationID string, now int64) error {
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

func InsertAssistantMessage(ctx context.Context, tx pgx.Tx, messageID, conversationID, turnID, content string, messageSequence, now int64) error {
	if _, err := tx.Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'assistant', $5, $6)", messageID, conversationID, turnID, messageSequence, content, now); err != nil {
		return fmt.Errorf("writing interrupted assistant prefix: %w", err)
	}
	return nil
}

func FailTurn(ctx context.Context, db interface {
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

func ScanMessageRecord(row scanner) (domainmemory.MessageRecord, error) {
	var message domainmemory.MessageRecord
	var sequence int64
	if err := row.Scan(&message.ID, &message.ConversationID, &message.TurnID, &sequence, &message.Role, &message.Content, &message.CreatedAtUnixMS); err != nil {
		return domainmemory.MessageRecord{}, fmt.Errorf("scanning conversation message: %w", err)
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
