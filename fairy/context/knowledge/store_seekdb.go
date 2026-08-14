package knowledge

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"fairy/runtime/embedding"
)

const seekDBKnowledgeRecordColumns = `
id, topic, statement, status, verification_basis, confidence_basis_points,
source_conversation_id, source_turn_id, supersedes_id,
source_url, source_title, evidence_text, source_fetched_at_ms,
created_at_ms, updated_at_ms`

func validateSeekDBKnowledgeID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	return nil
}

func validateDirectKnowledgeSources(sources []AssistantSource) error {
	if len(sources) > 1 {
		return errors.New("knowledge supports at most one direct source")
	}
	if len(sources) == 0 {
		return nil
	}
	source := sources[0]
	if strings.TrimSpace(source.Title) != source.Title ||
		strings.TrimSpace(source.Snippet) != source.Snippet || source.Snippet == "" ||
		utf8.RuneCountInString(source.Title) > 512 ||
		utf8.RuneCountInString(source.Snippet) > MaxIngestSnippetRunes ||
		ContainsDisallowedControl(source.Title) || ContainsDisallowedControl(source.Snippet) ||
		source.Rank != 1 || source.FetchedAtUnixMS < 0 {
		return errors.New("knowledge direct source is invalid")
	}
	parsed, err := url.Parse(source.URL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" ||
		parsed.String() != source.URL || parsed.Scheme != "http" && parsed.Scheme != "https" ||
		len(source.URL) > 2048 {
		return errors.New("knowledge direct source URL is invalid")
	}
	return nil
}

func validateSeekDBDirectKnowledgeContent(topic, statement string) error {
	if err := validateSeekDBKnowledgeTopic(topic); err != nil {
		return err
	}
	return validateSeekDBKnowledgeStatement(statement)
}

func validateSeekDBKnowledgeTopic(topic string) error {
	if !utf8.ValidString(topic) || strings.TrimSpace(topic) == "" ||
		strings.TrimSpace(topic) != topic || utf8.RuneCountInString(topic) > 512 ||
		ContainsDisallowedControl(topic) {
		return errors.New("knowledge topic is invalid")
	}
	return nil
}

func validateSeekDBKnowledgeStatement(statement string) error {
	if !utf8.ValidString(statement) || strings.TrimSpace(statement) == "" ||
		strings.TrimSpace(statement) != statement || utf8.RuneCountInString(statement) > 2400 ||
		ContainsDisallowedControl(statement) {
		return errors.New("knowledge statement is invalid")
	}
	return nil
}

func validateSeekDBKnowledgeDocumentPersistence(task IngestTask, document Document) error {
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "knowledge_ingest_task_id", value: task.ID},
		{label: "conversation_id", value: task.ConversationID},
		{label: "turn_id", value: task.TurnID},
		{label: "knowledge_ingest_source_id", value: task.Source.ID},
		{label: "knowledge_evidence_id", value: document.EvidenceID},
	} {
		if err := validateSeekDBKnowledgeID(item.label, item.value); err != nil {
			return err
		}
	}
	if len(document.CanonicalURL) > 2048 {
		return errors.New("knowledge document URL is invalid")
	}
	if !utf8.ValidString(document.Title) || utf8.RuneCountInString(document.Title) > 512 ||
		ContainsDisallowedControl(document.Title) {
		return errors.New("knowledge document title is invalid")
	}
	if document.ContentType == "" || len(document.ContentType) > 255 {
		return errors.New("knowledge document content type is invalid")
	}
	for _, character := range document.ContentType {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return errors.New("knowledge document content type is invalid")
		}
	}
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "etag", value: document.ETag},
		{label: "last_modified", value: document.LastModified},
	} {
		if !utf8.ValidString(item.value) || utf8.RuneCountInString(item.value) > 512 ||
			ContainsDisallowedControl(item.value) {
			return fmt.Errorf("knowledge document %s is invalid", item.label)
		}
	}
	if document.ReconcilerRevision != "" &&
		(len(document.ReconcilerRevision) > 128 || !validContentHash(document.ReconcilerRevision)) {
		return errors.New("knowledge document reconciler revision is invalid")
	}
	return nil
}

func decodeKnowledgeHash(label, value string) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("%s is invalid", label)
	}
	return decoded, nil
}

func seekDBKnowledgeEmbeddingTuple(content string, value embedding.EmbeddingValue) (any, any, any, error) {
	if err := value.Validate(); err != nil {
		return nil, nil, nil, err
	}
	if !value.Enabled() {
		return nil, nil, nil, nil
	}
	if len(value.ModelID) > 256 || strings.TrimSpace(value.ModelID) != value.ModelID {
		return nil, nil, nil, errors.New("embedding space id is invalid")
	}
	for _, character := range value.ModelID {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return nil, nil, nil, errors.New("embedding space id is invalid")
		}
	}
	if value.ContentHash != embedding.ContentHash(content) {
		return nil, nil, nil, errors.New("embedding content hash does not match knowledge content")
	}
	hash, err := decodeKnowledgeHash("embedding content hash", value.ContentHash)
	if err != nil {
		return nil, nil, nil, err
	}
	return value.ModelID, hash, value.Vector.String(), nil
}

func scanSeekDBKnowledge(row scanner) (Record, error) {
	var (
		record          Record
		confidence      int64
		supersedesID    sql.NullString
		sourceURL       sql.NullString
		sourceTitle     sql.NullString
		sourceEvidence  sql.NullString
		sourceFetchedAt sql.NullInt64
	)
	if err := row.Scan(
		&record.ID, &record.Topic, &record.Statement, &record.Status,
		&record.VerificationBasis, &confidence, &record.SourceConversationID,
		&record.SourceTurnID, &supersedesID, &sourceURL, &sourceTitle,
		&sourceEvidence, &sourceFetchedAt, &record.CreatedAtUnixMS,
		&record.UpdatedAtUnixMS,
	); err != nil {
		return Record{}, fmt.Errorf("scanning SeekDB knowledge: %w", err)
	}
	if err := validateSeekDBKnowledgeID("knowledge_id", record.ID); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBKnowledgeID("source_conversation_id", record.SourceConversationID); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBKnowledgeID("source_turn_id", record.SourceTurnID); err != nil {
		return Record{}, err
	}
	if confidence < 0 || confidence > 10000 {
		return Record{}, errors.New("knowledge confidence is invalid")
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	if supersedesID.Valid {
		value := supersedesID.String
		record.SupersedesID = &value
	}
	if sourceURL.Valid {
		if !sourceEvidence.Valid || !sourceFetchedAt.Valid {
			return Record{}, errors.New("knowledge source projection is incomplete")
		}
		source := AssistantSource{
			URL: sourceURL.String, Snippet: sourceEvidence.String,
			Rank: 1, FetchedAtUnixMS: sourceFetchedAt.Int64,
		}
		if sourceTitle.Valid {
			source.Title = sourceTitle.String
		}
		record.Sources = []AssistantSource{source}
	} else if sourceTitle.Valid || sourceEvidence.Valid || sourceFetchedAt.Valid {
		return Record{}, errors.New("knowledge source projection is inconsistent")
	}
	return record, nil
}

func listSeekDBKnowledge(ctx context.Context, database *sql.DB, status string) ([]Record, error) {
	rows, err := database.QueryContext(ctx, "SELECT "+seekDBKnowledgeRecordColumns+`
FROM knowledge_entries
WHERE status = ?
ORDER BY updated_at_ms DESC, id ASC
LIMIT 20`, status)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB knowledge catalog: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanSeekDBKnowledge(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB knowledge catalog: %w", err)
	}
	return records, nil
}

type seekDBKnowledgeRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func seekDBKnowledgeByID(ctx context.Context, database seekDBKnowledgeRowQuerier, id string, forUpdate bool) (Record, error) {
	query := "SELECT " + seekDBKnowledgeRecordColumns + " FROM knowledge_entries WHERE id = ?"
	if forUpdate {
		query += " FOR UPDATE"
	}
	return scanSeekDBKnowledge(database.QueryRowContext(ctx, query, id))
}

func lockSeekDBKnowledgeSource(ctx context.Context, tx *sql.Tx, conversationID, turnID string) error {
	if err := validateSeekDBKnowledgeID("source_conversation_id", conversationID); err != nil {
		return err
	}
	if err := validateSeekDBKnowledgeID("source_turn_id", turnID); err != nil {
		return err
	}
	var found int
	if err := tx.QueryRowContext(ctx, `
SELECT 1 FROM conversations WHERE id = ? FOR UPDATE`, conversationID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return errors.New("knowledge source conversation does not exist")
	} else if err != nil {
		return fmt.Errorf("locking SeekDB knowledge source conversation: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT 1
FROM conversation_turns
WHERE conversation_id = ? AND id = ?
FOR UPDATE`, conversationID, turnID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return errors.New("knowledge source turn does not exist")
	} else if err != nil {
		return fmt.Errorf("locking SeekDB knowledge source turn: %w", err)
	}
	return nil
}

func lockSeekDBKnowledgeTargets(ctx context.Context, tx *sql.Tx, ids []string) (map[string]Record, error) {
	targetIDs := slices.Clone(ids)
	for _, id := range targetIDs {
		if err := validateSeekDBKnowledgeID("knowledge_id", id); err != nil {
			return nil, err
		}
	}
	slices.Sort(targetIDs)
	targetIDs = slices.Compact(targetIDs)
	records := make(map[string]Record, len(targetIDs))
	for _, id := range targetIDs {
		record, err := seekDBKnowledgeByID(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("knowledge action target no longer exists")
		}
		if err != nil {
			return nil, fmt.Errorf("locking SeekDB knowledge action target: %w", err)
		}
		records[id] = record
	}
	return records, nil
}

func prepareKnowledgeEmbeddings(
	ctx context.Context,
	embedder embedding.SemanticEmbedder,
	contents []string,
	requireContext bool,
) ([]embedding.EmbeddingValue, error) {
	if requireContext {
		return embedding.ForContentsContext(ctx, embedder, contents)
	}
	return embedding.ForContents(embedder, contents)
}
