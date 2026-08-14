// Package sticker owns FAIRY's managed sticker assets and their human-authored
// semantic metadata.
package sticker

import "errors"

const (
	MaxContentBytes     = 5 << 20
	MaxDescriptionRunes = 512
	MaxTags             = 16
	MaxTagRunes         = 64
	MaxSearchQueryRunes = 200
	DefaultPageLimit    = 50
	MaxPageLimit        = 100
	DefaultSearchLimit  = 6
	MaxSearchLimit      = 8
)

var (
	ErrDatabasePoolRequired = errors.New("sticker database pool is required")
	ErrSeekDBRequired       = errors.New("sticker SeekDB connection is required")
	ErrQueryLimitInvalid    = errors.New("sticker query limit must be greater than zero")
	ErrContentRootInvalid   = errors.New("sticker content root must be an absolute non-root path")
	ErrContentInconsistent  = errors.New("sticker content file does not match catalog record")
	ErrContentRequired      = errors.New("sticker content is required")
	ErrContentTooLarge      = errors.New("sticker content exceeds 5 MiB")
	ErrUnsupportedMIME      = errors.New("sticker image format is unsupported")
	ErrMIMEMismatch         = errors.New("sticker declared MIME type does not match content")
	ErrDuplicateContent     = errors.New("sticker content already exists")
	ErrNotFound             = errors.New("sticker was not found")
	ErrDescriptionRequired  = errors.New("active sticker description is required")
	ErrDescriptionTooLong   = errors.New("sticker description is too long")
	ErrTooManyTags          = errors.New("sticker has too many tags")
	ErrTagInvalid           = errors.New("sticker tag is invalid")
	ErrStatusInvalid        = errors.New("sticker status is invalid")
	ErrQueryInvalid         = errors.New("sticker search query is invalid")
	ErrPageInvalid          = errors.New("sticker page is invalid")
)

type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Record struct {
	ID              string   `json:"id"`
	ContentSHA256   string   `json:"contentSha256"`
	MIMEType        string   `json:"mimeType"`
	ByteCount       int64    `json:"byteCount"`
	Description     string   `json:"description"`
	Tags            []string `json:"tags"`
	Status          Status   `json:"status"`
	CreatedAtUnixMS int64    `json:"createdAtUnixMs"`
	UpdatedAtUnixMS int64    `json:"updatedAtUnixMs"`
}

type Content struct {
	ID            string
	ContentSHA256 string
	MIMEType      string
	Bytes         []byte
}

type CreateInput struct {
	Content          []byte
	DeclaredMIMEType string
	Description      string
	Tags             []string
	Status           Status
}

type UpdateInput struct {
	Description *string
	Tags        *[]string
	Status      *Status
}

type ListInput struct {
	Status *Status
	Offset int
	Limit  int
}

type Page struct {
	Items      []Record `json:"items"`
	Offset     int      `json:"offset"`
	Limit      int      `json:"limit"`
	Total      int64    `json:"total"`
	NextOffset *int     `json:"nextOffset,omitempty"`
}

// Candidate contains MIMEType for strict final-expression validation, but the
// field is intentionally excluded from the model tool result.
type Candidate struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	MIMEType    string   `json:"-"`
}
