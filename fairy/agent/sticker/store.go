package sticker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	coredb "fairy/runtime/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Store struct {
	pool        *coredb.Pool
	seekDB      *sql.DB
	queryLimit  time.Duration
	contentRoot string
	now         func() time.Time
}

func NewStore(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolRequired
	}
	return &Store{pool: pool, now: time.Now}, nil
}

func (s *Store) Create(ctx context.Context, input CreateInput) (Record, error) {
	if s.usesSeekDB() {
		return s.createSeekDB(ctx, input)
	}
	if err := s.ready(); err != nil {
		return Record{}, err
	}
	input, mimeType, digest, err := validateCreate(input)
	if err != nil {
		return Record{}, err
	}
	tagsJSON, err := json.Marshal(input.Tags)
	if err != nil {
		return Record{}, fmt.Errorf("encode sticker tags: %w", err)
	}
	now := time.Now().UnixMilli()
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	row := s.pool.Raw().QueryRow(queryCtx, `
INSERT INTO stickers (
	id, content_sha256, mime_type, byte_count, content,
	description, tags, status, created_at_ms, updated_at_ms
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
ON CONFLICT (content_sha256) DO NOTHING
RETURNING id, content_sha256, mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms`,
		uuid.NewString(), digest, mimeType, int64(len(input.Content)), input.Content,
		input.Description, tagsJSON, input.Status, now,
	)
	record, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrDuplicateContent
	}
	if err != nil {
		return Record{}, fmt.Errorf("create sticker: %w", err)
	}
	return record, nil
}

func (s *Store) Find(ctx context.Context, id string) (Record, error) {
	if s.usesSeekDB() {
		return s.findSeekDB(ctx, id)
	}
	if err := s.ready(); err != nil {
		return Record{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, ErrNotFound
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	record, err := scanRecord(s.pool.Raw().QueryRow(queryCtx, `
SELECT id, content_sha256, mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms
FROM stickers WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("find sticker: %w", err)
	}
	return record, nil
}

func (s *Store) List(ctx context.Context, input ListInput) (Page, error) {
	if s.usesSeekDB() {
		return s.listSeekDB(ctx, input)
	}
	if err := s.ready(); err != nil {
		return Page{}, err
	}
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
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var total int64
	if err := s.pool.Raw().QueryRow(queryCtx,
		`SELECT COUNT(*) FROM stickers WHERE $1 = '' OR status = $1`, status,
	).Scan(&total); err != nil {
		return Page{}, fmt.Errorf("count stickers: %w", err)
	}
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, content_sha256, mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms
FROM stickers
WHERE $1 = '' OR status = $1
ORDER BY updated_at_ms DESC, id ASC
OFFSET $2 LIMIT $3`, status, input.Offset, input.Limit)
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

func (s *Store) Update(ctx context.Context, id string, input UpdateInput) (Record, error) {
	if s.usesSeekDB() {
		return s.updateSeekDB(ctx, id, input)
	}
	if err := s.ready(); err != nil {
		return Record{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, ErrNotFound
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Record{}, fmt.Errorf("begin sticker update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, err := scanRecord(tx.QueryRow(queryCtx, `
SELECT id, content_sha256, mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms
FROM stickers WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
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
	current.UpdatedAtUnixMS = time.Now().UnixMilli()
	updated, err := scanRecord(tx.QueryRow(queryCtx, `
UPDATE stickers
SET description = $2, tags = $3, status = $4, updated_at_ms = $5
WHERE id = $1
RETURNING id, content_sha256, mime_type, byte_count, description, tags, status, created_at_ms, updated_at_ms`,
		id, current.Description, tagsJSON, current.Status, current.UpdatedAtUnixMS,
	))
	if err != nil {
		return Record{}, fmt.Errorf("update sticker: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Record{}, fmt.Errorf("commit sticker update: %w", err)
	}
	return updated, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if s.usesSeekDB() {
		return s.deleteSeekDB(ctx, id)
	}
	if err := s.ready(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrNotFound
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var deleted string
	if err := s.pool.Raw().QueryRow(queryCtx,
		`DELETE FROM stickers WHERE id = $1 RETURNING id`, id,
	).Scan(&deleted); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("delete sticker: %w", err)
	}
	return nil
}

func (s *Store) Content(ctx context.Context, id string) (Content, error) {
	if s.usesSeekDB() {
		return s.contentSeekDB(ctx, id)
	}
	if err := s.ready(); err != nil {
		return Content{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Content{}, ErrNotFound
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var content Content
	err := s.pool.Raw().QueryRow(queryCtx, `
SELECT id, content_sha256, mime_type, content
FROM stickers WHERE id = $1`, id,
	).Scan(&content.ID, &content.ContentSHA256, &content.MIMEType, &content.Bytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return Content{}, ErrNotFound
	}
	if err != nil {
		return Content{}, fmt.Errorf("read sticker content: %w", err)
	}
	return content, nil
}

func (s *Store) HasActive(ctx context.Context) (bool, error) {
	if s.usesSeekDB() {
		return s.hasActiveSeekDB(ctx)
	}
	if err := s.ready(); err != nil {
		return false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var active bool
	if err := s.pool.Raw().QueryRow(queryCtx,
		`SELECT EXISTS (SELECT 1 FROM stickers WHERE status = 'active')`,
	).Scan(&active); err != nil {
		return false, fmt.Errorf("check active stickers: %w", err)
	}
	return active, nil
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	if s.usesSeekDB() {
		return s.searchSeekDB(ctx, query, limit)
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
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
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, description, tags, mime_type
FROM stickers
WHERE status = 'active'
	AND (
	description ILIKE $2 ESCAPE '\'
	OR tags::text ILIKE $2 ESCAPE '\'
	OR description OPERATOR(public.%) $1
  )
ORDER BY GREATEST(
	public.similarity(description, $1),
	public.similarity(tags::text, $1),
	CASE WHEN description ILIKE $2 ESCAPE '\' THEN 1 ELSE 0 END
) DESC, updated_at_ms DESC, id ASC
LIMIT $3`, query, escapeLike(query), limit)
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

func (s *Store) ready() error {
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return ErrDatabasePoolRequired
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var tagsJSON []byte
	if err := row.Scan(
		&record.ID, &record.ContentSHA256, &record.MIMEType, &record.ByteCount,
		&record.Description, &tagsJSON, &record.Status,
		&record.CreatedAtUnixMS, &record.UpdatedAtUnixMS,
	); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal(tagsJSON, &record.Tags); err != nil {
		return Record{}, err
	}
	if record.Tags == nil {
		record.Tags = []string{}
	}
	record.ContentSHA256 = strings.ToLower(record.ContentSHA256)
	return record, nil
}
