package sticker

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSniffMIMEType(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
		wantErr error
	}{
		{name: "jpeg", content: []byte{0xff, 0xd8, 0xff, 0x00}, want: "image/jpeg"},
		{name: "png", content: append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0), want: "image/png"},
		{name: "gif87", content: []byte("GIF87a!"), want: "image/gif"},
		{name: "gif89", content: []byte("GIF89a!"), want: "image/gif"},
		{name: "webp", content: []byte("RIFF0000WEBP"), want: "image/webp"},
		{name: "unknown", content: []byte("not-an-image"), wantErr: ErrUnsupportedMIME},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := sniffMIMEType(test.content)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("sniffMIMEType() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("sniffMIMEType() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("body")...)
	input, mimeType, digest, err := validateCreate(CreateInput{
		Content: png, DeclaredMIMEType: " image/png ",
		Description: "  震惊但不失礼貌  ", Tags: []string{"震惊", "  无语 ", "震惊"},
		Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/png" || len(digest) != 64 {
		t.Fatalf("mime=%q digest=%q", mimeType, digest)
	}
	if input.Description != "震惊但不失礼貌" {
		t.Fatalf("description = %q", input.Description)
	}
	if len(input.Tags) != 2 || input.Tags[0] != "震惊" || input.Tags[1] != "无语" {
		t.Fatalf("tags = %#v", input.Tags)
	}
	png[8] = 'x'
	if bytes.Equal(input.Content, png) {
		t.Fatal("validated content aliases caller buffer")
	}
}

func TestValidateCreateRejectsInvalidInputs(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0)
	tests := []struct {
		name    string
		input   CreateInput
		wantErr error
	}{
		{name: "empty", input: CreateInput{}, wantErr: ErrContentRequired},
		{name: "too large", input: CreateInput{Content: make([]byte, MaxContentBytes+1)}, wantErr: ErrContentTooLarge},
		{name: "mime mismatch", input: CreateInput{Content: png, DeclaredMIMEType: "image/jpeg"}, wantErr: ErrMIMEMismatch},
		{name: "active missing description", input: CreateInput{Content: png, Status: StatusActive}, wantErr: ErrDescriptionRequired},
		{name: "description too long", input: CreateInput{Content: png, Description: strings.Repeat("字", MaxDescriptionRunes+1)}, wantErr: ErrDescriptionTooLong},
		{name: "invalid status", input: CreateInput{Content: png, Status: "ready"}, wantErr: ErrStatusInvalid},
		{name: "too many tags", input: CreateInput{Content: png, Tags: make([]string, MaxTags+1)}, wantErr: ErrTooManyTags},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := validateCreate(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("validateCreate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestNewStoreRejectsMissingPool(t *testing.T) {
	if _, err := NewStore(nil); !errors.Is(err, ErrDatabasePoolRequired) {
		t.Fatalf("NewStore(nil) error = %v", err)
	}
}
