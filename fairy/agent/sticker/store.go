package sticker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrStoreBackendUnavailable = errors.New("sticker store backend is unavailable")

type Store struct {
	seekDB      *sql.DB
	queryLimit  time.Duration
	contentRoot string
	now         func() time.Time
}

func (s *Store) Create(ctx context.Context, input CreateInput) (Record, error) {
	if !s.usesSeekDB() {
		return Record{}, ErrStoreBackendUnavailable
	}
	return s.createSeekDB(ctx, input)
}

func (s *Store) Find(ctx context.Context, id string) (Record, error) {
	if !s.usesSeekDB() {
		return Record{}, ErrStoreBackendUnavailable
	}
	return s.findSeekDB(ctx, id)
}

func (s *Store) List(ctx context.Context, input ListInput) (Page, error) {
	if !s.usesSeekDB() {
		return Page{}, ErrStoreBackendUnavailable
	}
	return s.listSeekDB(ctx, input)
}

func (s *Store) Update(ctx context.Context, id string, input UpdateInput) (Record, error) {
	if !s.usesSeekDB() {
		return Record{}, ErrStoreBackendUnavailable
	}
	return s.updateSeekDB(ctx, id, input)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	return s.deleteSeekDB(ctx, id)
}

func (s *Store) Content(ctx context.Context, id string) (Content, error) {
	if !s.usesSeekDB() {
		return Content{}, ErrStoreBackendUnavailable
	}
	return s.contentSeekDB(ctx, id)
}

func (s *Store) HasActive(ctx context.Context) (bool, error) {
	if !s.usesSeekDB() {
		return false, ErrStoreBackendUnavailable
	}
	return s.hasActiveSeekDB(ctx)
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.searchSeekDB(ctx, query, limit)
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
