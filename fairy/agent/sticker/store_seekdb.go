package sticker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

const stickerSelectSQL = `
SELECT id, HEX(content_sha256), mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms
FROM stickers`

func NewSeekDBStore(database *sql.DB, contentRoot string, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBRequired
	}
	if queryLimit <= 0 {
		return nil, ErrQueryLimitInvalid
	}
	root, err := validateContentRoot(contentRoot)
	if err != nil {
		return nil, err
	}
	return &Store{
		seekDB:      database,
		queryLimit:  queryLimit,
		contentRoot: root,
		now:         time.Now,
	}, nil
}

func validateContentRoot(root string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || root == "." {
		return "", ErrContentRootInvalid
	}
	return root, nil
}

func (s *Store) usesSeekDB() bool {
	return s != nil && s.seekDB != nil
}

func (s *Store) seekDBQueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *Store) currentUnixMS() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), int64(1))
}

func (s *Store) createSeekDB(ctx context.Context, input CreateInput) (Record, error) {
	input, mimeType, digest, err := validateCreate(input)
	if err != nil {
		return Record{}, err
	}
	hash, err := hex.DecodeString(digest)
	if err != nil || len(hash) != sha256.Size {
		return Record{}, fmt.Errorf("sticker content hash is invalid")
	}
	tagsJSON, err := json.Marshal(input.Tags)
	if err != nil {
		return Record{}, fmt.Errorf("encode sticker tags: %w", err)
	}
	if err := s.writeContentFile(digest, input.Content); err != nil {
		return Record{}, err
	}
	now := s.currentUnixMS()
	id := uuid.NewString()
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	_, err = s.seekDB.ExecContext(queryCtx, `
INSERT INTO stickers (
  id, content_sha256, mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, hash, mimeType, int64(len(input.Content)), input.Description, tagsJSON, input.Status, now, now,
	)
	if isDuplicateSeekDBError(err) {
		return Record{}, ErrDuplicateContent
	}
	if err != nil {
		return Record{}, fmt.Errorf("create sticker: %w", err)
	}
	return Record{
		ID: id, ContentSHA256: digest, MIMEType: mimeType, ByteCount: int64(len(input.Content)),
		Description: input.Description, Tags: input.Tags, Status: input.Status,
		CreatedAtUnixMS: now, UpdatedAtUnixMS: now,
	}, nil
}

func (s *Store) findSeekDB(ctx context.Context, id string) (Record, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, ErrNotFound
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	record, err := scanRecord(s.seekDB.QueryRowContext(queryCtx, stickerSelectSQL+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("find sticker: %w", err)
	}
	return record, nil
}

func (s *Store) listSeekDB(ctx context.Context, input ListInput) (Page, error) {
	if input.Offset < 0 {
		return Page{}, ErrPageInvalid
	}
	if input.Limit == 0 {
		input.Limit = DefaultPageLimit
	}
	if input.Limit < 1 || input.Limit > MaxPageLimit {
		return Page{}, ErrPageInvalid
	}
	status := Status("")
	if input.Status != nil {
		status = *input.Status
		if status != StatusDraft && status != StatusActive && status != StatusDisabled {
			return Page{}, ErrStatusInvalid
		}
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var total int64
	if err := s.seekDB.QueryRowContext(queryCtx,
		`SELECT COUNT(*) FROM stickers WHERE ? = '' OR status = ?`, status, status,
	).Scan(&total); err != nil {
		return Page{}, fmt.Errorf("count stickers: %w", err)
	}
	rows, err := s.seekDB.QueryContext(queryCtx, stickerSelectSQL+`
WHERE ? = '' OR status = ?
ORDER BY updated_at_ms DESC, id ASC
LIMIT ? OFFSET ?`, status, status, input.Limit, input.Offset)
	if err != nil {
		return Page{}, fmt.Errorf("list stickers: %w", err)
	}
	defer rows.Close()
	items := make([]Record, 0, input.Limit)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return Page{}, fmt.Errorf("scan sticker list: %w", scanErr)
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate sticker list: %w", err)
	}
	page := Page{Items: items, Offset: input.Offset, Limit: input.Limit, Total: total}
	if next := input.Offset + len(items); next < int(total) {
		page.NextOffset = &next
	}
	return page, nil
}

func (s *Store) updateSeekDB(ctx context.Context, id string, input UpdateInput) (Record, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, ErrNotFound
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin sticker update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanRecord(tx.QueryRowContext(queryCtx, stickerSelectSQL+` WHERE id = ? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("load sticker for update: %w", err)
	}
	if input.Description != nil {
		current.Description, err = normalizeDescription(*input.Description)
		if err != nil {
			return Record{}, err
		}
	}
	if input.Tags != nil {
		current.Tags, err = normalizeTags(*input.Tags)
		if err != nil {
			return Record{}, err
		}
	}
	if input.Status != nil {
		current.Status = *input.Status
	}
	if err := validateStatus(current.Status, current.Description); err != nil {
		return Record{}, err
	}
	tagsJSON, err := json.Marshal(current.Tags)
	if err != nil {
		return Record{}, fmt.Errorf("encode sticker tags: %w", err)
	}
	current.UpdatedAtUnixMS = s.currentUnixMS()
	if _, err := tx.ExecContext(queryCtx, `
UPDATE stickers
SET description = ?, tags = ?, status = ?, updated_at_ms = ?
WHERE id = ?`, current.Description, tagsJSON, current.Status, current.UpdatedAtUnixMS, id); err != nil {
		return Record{}, fmt.Errorf("update sticker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit sticker update: %w", err)
	}
	return current, nil
}

func (s *Store) deleteSeekDB(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrNotFound
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var digest string
	err := s.seekDB.QueryRowContext(queryCtx, `SELECT HEX(content_sha256) FROM stickers WHERE id = ?`, id).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load sticker for delete: %w", err)
	}
	result, err := s.seekDB.ExecContext(queryCtx, `DELETE FROM stickers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete sticker: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete sticker: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if err := os.Remove(s.contentPath(strings.ToLower(digest))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete sticker content file: %w", err)
	}
	return nil
}

func (s *Store) contentSeekDB(ctx context.Context, id string) (Content, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Content{}, ErrNotFound
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var content Content
	var byteCount int64
	err := s.seekDB.QueryRowContext(queryCtx, `
SELECT id, HEX(content_sha256), mime_type, byte_count
FROM stickers WHERE id = ?`, id,
	).Scan(&content.ID, &content.ContentSHA256, &content.MIMEType, &byteCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Content{}, ErrNotFound
	}
	if err != nil {
		return Content{}, fmt.Errorf("read sticker catalog: %w", err)
	}
	content.ContentSHA256 = strings.ToLower(content.ContentSHA256)
	bytes, err := os.ReadFile(s.contentPath(content.ContentSHA256))
	if err != nil {
		return Content{}, fmt.Errorf("%w: %v", ErrContentInconsistent, err)
	}
	sum := sha256.Sum256(bytes)
	if hex.EncodeToString(sum[:]) != content.ContentSHA256 || int64(len(bytes)) != byteCount {
		return Content{}, ErrContentInconsistent
	}
	content.Bytes = bytes
	return content, nil
}

func (s *Store) hasActiveSeekDB(ctx context.Context) (bool, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var active bool
	if err := s.seekDB.QueryRowContext(queryCtx,
		`SELECT EXISTS (SELECT 1 FROM stickers WHERE status = 'active')`,
	).Scan(&active); err != nil {
		return false, fmt.Errorf("check active stickers: %w", err)
	}
	return active, nil
}

func (s *Store) searchSeekDB(ctx context.Context, query string, limit int) ([]Candidate, error) {
	query, err := normalizeSearchQuery(query)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	if limit < 1 || limit > MaxSearchLimit {
		return nil, ErrQueryInvalid
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT id, description, tags, mime_type
FROM stickers
WHERE status = 'active'
  AND (
    LOCATE(LOWER(?), LOWER(description)) > 0
    OR LOCATE(LOWER(?), LOWER(CAST(tags AS CHAR))) > 0
  )
ORDER BY updated_at_ms DESC, id ASC
LIMIT ?`, query, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search stickers: %w", err)
	}
	defer rows.Close()
	candidates := make([]Candidate, 0, limit)
	for rows.Next() {
		var candidate Candidate
		var tagsJSON []byte
		if err := rows.Scan(&candidate.ID, &candidate.Description, &tagsJSON, &candidate.MIMEType); err != nil {
			return nil, fmt.Errorf("scan sticker candidate: %w", err)
		}
		if err := json.Unmarshal(tagsJSON, &candidate.Tags); err != nil {
			return nil, fmt.Errorf("decode sticker candidate tags: %w", err)
		}
		if candidate.Tags == nil {
			candidate.Tags = []string{}
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sticker candidates: %w", err)
	}
	return candidates, nil
}

func (s *Store) writeContentFile(digest string, content []byte) error {
	if err := os.MkdirAll(s.contentRoot, 0o700); err != nil {
		return fmt.Errorf("create sticker content directory: %w", err)
	}
	destination := s.contentPath(digest)
	if existing, err := os.ReadFile(destination); err == nil {
		sum := sha256.Sum256(existing)
		if hex.EncodeToString(sum[:]) == digest {
			return nil
		}
		return ErrContentInconsistent
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat sticker content file: %w", err)
	}
	temporary := destination + ".tmp"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return fmt.Errorf("write sticker content file: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("commit sticker content file: %w", err)
	}
	return nil
}

func (s *Store) contentPath(digest string) string {
	return filepath.Join(s.contentRoot, digest)
}

func isDuplicateSeekDBError(err error) bool {
	var databaseError *gomysql.MySQLError
	return errors.As(err, &databaseError) && databaseError.Number == 1062
}
