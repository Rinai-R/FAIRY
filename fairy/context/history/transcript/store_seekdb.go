package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"unicode"
	"unicode/utf8"

	historyprojection "fairy/context/history/projection"
	"fairy/transport/session"

	gomysql "github.com/go-sql-driver/mysql"
)

const emptyPromptProjectionJSON = `{"version":1,"omissions":[]}`

func (s *Store) openOrCreateCharacterConversationSeekDB(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	if err := validateSeekDBIdentifier("character_id", characterID); err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	conversationID, found, err := selectCharacterConversationSeekDB(queryCtx, s.seekDB, characterID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	if found {
		return s.loadConversationSeekDB(ctx, conversationID)
	}
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("beginning SeekDB conversation transaction: %w", err)
	}
	defer tx.Rollback()
	conversationID = newID()
	if err := insertConversationWithPromptWindowSeekDB(queryCtx, tx, conversationID, characterID, "character", s.currentUnixMS()); err != nil {
		return ConversationBootstrap{}, err
	}
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, 'character')`, characterID, conversationID); err != nil {
		if !isDuplicateSeekDBError(err) {
			return ConversationBootstrap{}, fmt.Errorf("binding SeekDB character conversation: %w", err)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return ConversationBootstrap{}, errors.Join(
				fmt.Errorf("binding SeekDB character conversation: %w", err),
				fmt.Errorf("rolling back losing SeekDB character conversation: %w", rollbackErr),
			)
		}
		conversationID, found, err = selectCharacterConversationSeekDB(queryCtx, s.seekDB, characterID)
		if err != nil {
			return ConversationBootstrap{}, err
		}
		if !found {
			return ConversationBootstrap{}, errors.New("concurrent SeekDB character conversation is unavailable")
		}
		return s.loadConversationSeekDB(ctx, conversationID)
	}
	if err := tx.Commit(); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing SeekDB conversation transaction: %w", err)
	}
	return s.loadConversationSeekDB(ctx, conversationID)
}

func (s *Store) openOrCreateEndpointConversationSeekDB(ctx context.Context, characterID string, binding session.Binding, digestHex string) (ConversationBootstrap, error) {
	if err := validateSeekDBIdentifier("character_id", characterID); err != nil {
		return ConversationBootstrap{}, err
	}
	digest, err := decodeDigest(digestHex)
	if err != nil {
		return ConversationBootstrap{}, errors.New("endpoint key digest is invalid")
	}
	principalDigest, err := decodeOptionalDigest(binding.Facts.PrincipalDigest)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("beginning SeekDB endpoint conversation transaction: %w", err)
	}
	defer tx.Rollback()
	stored, found, err := selectEndpointConversationSeekDB(queryCtx, tx, characterID, binding.Endpoint, digest)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	now := s.currentUnixMS()
	if found {
		if stored.Binding(binding.Endpoint) != binding {
			return ConversationBootstrap{}, ErrEndpointBindingMismatch
		}
		if _, err := tx.ExecContext(queryCtx, `
UPDATE endpoint_conversations
SET updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE character_id = ? AND endpoint = ? AND endpoint_key_digest = ?`, now, characterID, binding.Endpoint, digest); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("touching SeekDB endpoint conversation: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("committing SeekDB endpoint conversation touch: %w", err)
		}
		return s.loadConversationSeekDB(ctx, stored.ConversationID)
	}

	conversationID := newID()
	if err := insertConversationWithPromptWindowSeekDB(queryCtx, tx, conversationID, characterID, "endpoint", now); err != nil {
		return ConversationBootstrap{}, err
	}
	var namespace any
	var principal any
	if binding.Facts.PrincipalNamespace != "" {
		namespace = binding.Facts.PrincipalNamespace
		principal = principalDigest
	}
	if _, err := tx.ExecContext(queryCtx, `
INSERT INTO endpoint_conversations(
    character_id, endpoint, endpoint_key_digest, conversation_id, kind,
    audience, initiation, presentation, principal_namespace, principal_digest,
    evaluation, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, 'endpoint', ?, ?, ?, ?, ?, ?, ?, ?)`,
		characterID, binding.Endpoint, digest, conversationID,
		binding.Facts.Audience, binding.Facts.Initiation, binding.Facts.Presentation,
		namespace, principal, binding.Facts.Evaluation, now, now,
	); err != nil {
		if !isDuplicateSeekDBError(err) {
			return ConversationBootstrap{}, fmt.Errorf("binding SeekDB endpoint conversation: %w", err)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return ConversationBootstrap{}, errors.Join(
				fmt.Errorf("binding SeekDB endpoint conversation: %w", err),
				fmt.Errorf("rolling back losing SeekDB endpoint conversation: %w", rollbackErr),
			)
		}
		return s.loadExistingEndpointConversationSeekDB(queryCtx, characterID, binding, digest, now)
	}
	if err := tx.Commit(); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing SeekDB endpoint conversation: %w", err)
	}
	return s.loadConversationSeekDB(ctx, conversationID)
}

func (s *Store) lookupEndpointForConversationSeekDB(ctx context.Context, conversationID string) (session.Binding, bool, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return session.Binding{}, false, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var endpoint, audience, initiation, presentation string
	var namespace sql.NullString
	var digest []byte
	var evaluation bool
	err := s.seekDB.QueryRowContext(queryCtx, `
SELECT endpoint, audience, initiation, presentation,
       principal_namespace, principal_digest, evaluation
FROM endpoint_conversations
WHERE conversation_id = ?`, conversationID).Scan(
		&endpoint, &audience, &initiation, &presentation,
		&namespace, &digest, &evaluation,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Binding{}, false, nil
	}
	if err != nil {
		return session.Binding{}, false, fmt.Errorf("looking up SeekDB endpoint conversation: %w", err)
	}
	if namespace.Valid != (len(digest) != 0) {
		return session.Binding{}, false, errors.New("stored SeekDB principal reference is inconsistent")
	}
	binding := session.Binding{Endpoint: session.EndpointKind(endpoint), Facts: session.Facts{
		Audience: session.AudienceKind(audience), Initiation: session.InitiationKind(initiation),
		Presentation: session.PresentationKind(presentation), Evaluation: evaluation,
	}}
	if namespace.Valid {
		if len(digest) != sha256.Size {
			return session.Binding{}, false, errors.New("stored SeekDB principal digest is invalid")
		}
		binding.Facts.PrincipalNamespace = namespace.String
		binding.Facts.PrincipalDigest = hex.EncodeToString(digest)
	}
	if err := binding.Validate(); err != nil {
		return session.Binding{}, false, fmt.Errorf("validating stored SeekDB endpoint conversation: %w", err)
	}
	return binding, true, nil
}

func (s *Store) loadConversationSeekDB(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	conversation, prompt, err := loadConversationMetadataSeekDB(queryCtx, s.seekDB, conversationID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id,
       m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM conversation_messages m
JOIN conversation_turns t
  ON t.id = m.turn_id AND t.conversation_id = m.conversation_id
WHERE m.conversation_id = ?
ORDER BY m.sequence ASC`, conversationID)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading SeekDB conversation messages: %w", err)
	}
	messages, err := scanSeekDBMessages(rows)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	return ConversationBootstrap{Conversation: conversation, Messages: messages, PromptWindow: prompt}, nil
}

func (s *Store) listConversationMessagesBeforeSeekDB(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var exists int
	if err := s.seekDB.QueryRowContext(queryCtx, "SELECT 1 FROM conversations WHERE id = ?", conversationID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return MessagePage{}, errors.New("conversation does not exist")
	} else if err != nil {
		return MessagePage{}, fmt.Errorf("checking SeekDB conversation for message page: %w", err)
	}
	query := `
SELECT m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id,
       m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM conversation_messages m
JOIN conversation_turns t
  ON t.id = m.turn_id AND t.conversation_id = m.conversation_id
WHERE m.conversation_id = ?`
	arguments := []any{conversationID}
	if beforeSequence != 0 {
		query += " AND m.sequence < ?"
		arguments = append(arguments, int64(beforeSequence))
	}
	query += " ORDER BY m.sequence DESC LIMIT ?"
	arguments = append(arguments, limit+1)
	rows, err := s.seekDB.QueryContext(queryCtx, query, arguments...)
	if err != nil {
		return MessagePage{}, fmt.Errorf("listing SeekDB conversation messages: %w", err)
	}
	messages, err := scanSeekDBMessages(rows)
	if err != nil {
		return MessagePage{}, err
	}
	var next *uint64
	if len(messages) > limit {
		messages = messages[:limit]
		cursor := messages[len(messages)-1].Sequence
		next = &cursor
	}
	slices.Reverse(messages)
	return MessagePage{Messages: messages, NextBeforeSequence: next}, nil
}

type seekDBQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func selectCharacterConversationSeekDB(ctx context.Context, queryer seekDBQueryer, characterID string) (string, bool, error) {
	var id string
	err := queryer.QueryRowContext(ctx, `
SELECT conversation_id
FROM character_conversations
WHERE character_id = ?`, characterID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("loading SeekDB character conversation: %w", err)
	}
	return id, true, nil
}

func insertConversationWithPromptWindowSeekDB(ctx context.Context, tx *sql.Tx, conversationID, characterID, kind string, now int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?)`, conversationID, characterID, kind, now, now); err != nil {
		return fmt.Errorf("creating SeekDB conversation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO prompt_windows(
    conversation_id, revision, summary, cutoff_message_sequence,
    projection_revision, projection_state, updated_at_ms
) VALUES (?, 1, NULL, 0, 1, ?, ?)`, conversationID, emptyPromptProjectionJSON, now); err != nil {
		return fmt.Errorf("creating SeekDB prompt window: %w", err)
	}
	return nil
}

func selectEndpointConversationSeekDB(ctx context.Context, queryer seekDBQueryer, characterID string, endpoint session.EndpointKind, digest []byte) (EndpointConversationRow, bool, error) {
	var row EndpointConversationRow
	var namespace sql.NullString
	var principalDigest []byte
	err := queryer.QueryRowContext(ctx, `
SELECT conversation_id, audience, initiation, presentation,
       principal_namespace, principal_digest, evaluation
FROM endpoint_conversations
WHERE character_id = ? AND endpoint = ? AND endpoint_key_digest = ?`, characterID, endpoint, digest).Scan(
		&row.ConversationID, &row.Audience, &row.Initiation, &row.Presentation,
		&namespace, &principalDigest, &row.Evaluation,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EndpointConversationRow{}, false, nil
	}
	if err != nil {
		return EndpointConversationRow{}, false, fmt.Errorf("loading SeekDB endpoint conversation: %w", err)
	}
	if namespace.Valid != (len(principalDigest) != 0) {
		return EndpointConversationRow{}, false, errors.New("stored SeekDB endpoint principal reference is inconsistent")
	}
	if namespace.Valid {
		if len(principalDigest) != sha256.Size {
			return EndpointConversationRow{}, false, errors.New("stored SeekDB endpoint principal digest is invalid")
		}
		row.PrincipalNamespace = namespace.String
		row.PrincipalDigest = hex.EncodeToString(principalDigest)
	}
	return row, true, nil
}

func (s *Store) loadExistingEndpointConversationSeekDB(
	ctx context.Context,
	characterID string,
	binding session.Binding,
	digest []byte,
	now int64,
) (ConversationBootstrap, error) {
	tx, err := s.seekDB.BeginTx(ctx, nil)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("beginning winning SeekDB endpoint conversation transaction: %w", err)
	}
	defer tx.Rollback()
	stored, found, err := selectEndpointConversationSeekDB(ctx, tx, characterID, binding.Endpoint, digest)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	if !found {
		return ConversationBootstrap{}, errors.New("concurrent SeekDB endpoint conversation is unavailable")
	}
	if stored.Binding(binding.Endpoint) != binding {
		return ConversationBootstrap{}, ErrEndpointBindingMismatch
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE endpoint_conversations
SET updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE character_id = ? AND endpoint = ? AND endpoint_key_digest = ?`, now, characterID, binding.Endpoint, digest); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("touching winning SeekDB endpoint conversation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing winning SeekDB endpoint conversation: %w", err)
	}
	return s.loadConversationSeekDB(ctx, stored.ConversationID)
}

func loadConversationMetadataSeekDB(ctx context.Context, database *sql.DB, conversationID string) (ConversationRecord, PromptWindowRecord, error) {
	var conversation ConversationRecord
	if err := database.QueryRowContext(ctx, `
SELECT id, character_id, created_at_ms, updated_at_ms
FROM conversations WHERE id = ?`, conversationID).Scan(
		&conversation.ID, &conversation.CharacterID,
		&conversation.CreatedAtUnixMS, &conversation.UpdatedAtUnixMS,
	); err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, fmt.Errorf("loading SeekDB conversation: %w", err)
	}
	if err := validateSeekDBIdentifier("stored conversation_id", conversation.ID); err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, err
	}
	if err := validateSeekDBIdentifier("stored character_id", conversation.CharacterID); err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, err
	}
	var prompt PromptWindowRecord
	var summary sql.NullString
	var revision, cutoff, projectionRevision int64
	var projectionJSON []byte
	if err := database.QueryRowContext(ctx, `
SELECT conversation_id, revision, summary, cutoff_message_sequence,
       projection_revision, projection_state, updated_at_ms
FROM prompt_windows WHERE conversation_id = ?`, conversationID).Scan(
		&prompt.ConversationID, &revision, &summary, &cutoff,
		&projectionRevision, &projectionJSON, &prompt.UpdatedAtUnixMS,
	); err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, fmt.Errorf("loading SeekDB prompt window: %w", err)
	}
	if conversation.CreatedAtUnixMS < 0 || conversation.UpdatedAtUnixMS < conversation.CreatedAtUnixMS ||
		prompt.UpdatedAtUnixMS < 0 || revision <= 0 || cutoff < 0 || projectionRevision <= 0 {
		return ConversationRecord{}, PromptWindowRecord{}, errors.New("stored SeekDB conversation metadata is invalid")
	}
	prompt.Revision = uint64(revision)
	prompt.CutoffMessageSequence = uint64(cutoff)
	prompt.ProjectionRevision = uint64(projectionRevision)
	projection, err := historyprojection.Decode(projectionJSON)
	if err != nil {
		return ConversationRecord{}, PromptWindowRecord{}, err
	}
	prompt.Projection = projection
	if summary.Valid {
		prompt.Summary = &summary.String
	}
	return conversation, prompt, nil
}

func scanSeekDBMessages(rows *sql.Rows) ([]MessageRecord, error) {
	defer rows.Close()
	messages := make([]MessageRecord, 0)
	for rows.Next() {
		var message MessageRecord
		var sequence int64
		var expressionPartsJSON []byte
		if err := rows.Scan(
			&message.ID, &message.MessageID, &message.ConversationID, &message.TurnID,
			&sequence, &message.Role, &message.Content, &expressionPartsJSON, &message.CreatedAtUnixMS,
		); err != nil {
			return nil, fmt.Errorf("scanning SeekDB conversation message: %w", err)
		}
		if err := validateSeekDBIdentifier("stored message_id", message.ID); err != nil {
			return nil, err
		}
		if err := validateSeekDBIdentifier("stored message conversation_id", message.ConversationID); err != nil {
			return nil, err
		}
		if err := validateSeekDBIdentifier("stored turn_id", message.TurnID); err != nil {
			return nil, err
		}
		if message.CreatedAtUnixMS < 0 {
			return nil, errors.New("stored SeekDB conversation message timestamp is invalid")
		}
		if sequence <= 0 {
			return nil, errors.New("stored SeekDB conversation message sequence is invalid")
		}
		if err := ValidateOptionalMessageID(message.MessageID); err != nil {
			return nil, fmt.Errorf("validating stored SeekDB external message id: %w", err)
		}
		message, err := finishScannedMessage(message, sequence, expressionPartsJSON)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB conversation messages: %w", err)
	}
	return messages, nil
}

func decodeDigest(value string) ([]byte, error) {
	if err := session.ValidateDigest(value); err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("digest is invalid")
	}
	return decoded, nil
}

func decodeOptionalDigest(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	return decodeDigest(value)
}

func validateSeekDBIdentifier(label, value string) error {
	if err := ValidateID(label, value); err != nil {
		return err
	}
	if len(value) > 128 || !utf8.ValidString(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	return nil
}

func isDuplicateSeekDBError(err error) bool {
	var databaseError *gomysql.MySQLError
	return errors.As(err, &databaseError) && databaseError.Number == 1062
}
