package sticker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateCreate(input CreateInput) (CreateInput, string, string, error) {
	if len(input.Content) == 0 {
		return CreateInput{}, "", "", ErrContentRequired
	}
	if len(input.Content) > MaxContentBytes {
		return CreateInput{}, "", "", ErrContentTooLarge
	}
	mimeType, err := sniffMIMEType(input.Content)
	if err != nil {
		return CreateInput{}, "", "", err
	}
	declared := strings.ToLower(strings.TrimSpace(input.DeclaredMIMEType))
	if declared != "" && declared != mimeType {
		return CreateInput{}, "", "", ErrMIMEMismatch
	}
	description, err := normalizeDescription(input.Description)
	if err != nil {
		return CreateInput{}, "", "", err
	}
	tags, err := normalizeTags(input.Tags)
	if err != nil {
		return CreateInput{}, "", "", err
	}
	status := input.Status
	if status == "" {
		status = StatusDraft
	}
	if err := validateStatus(status, description); err != nil {
		return CreateInput{}, "", "", err
	}
	digest := sha256.Sum256(input.Content)
	input.Description = description
	input.Tags = tags
	input.Status = status
	input.DeclaredMIMEType = mimeType
	input.Content = bytes.Clone(input.Content)
	return input, mimeType, hex.EncodeToString(digest[:]), nil
}

func normalizeDescription(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > MaxDescriptionRunes {
		return "", ErrDescriptionTooLong
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrTagInvalid
		}
	}
	return value, nil
}

func normalizeTags(values []string) ([]string, error) {
	if len(values) > MaxTags {
		return nil, ErrTooManyTags
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if utf8.RuneCountInString(value) > MaxTagRunes {
			return nil, ErrTagInvalid
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return nil, ErrTagInvalid
			}
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	if len(result) > MaxTags {
		return nil, ErrTooManyTags
	}
	return result, nil
}

func validateStatus(status Status, description string) error {
	switch status {
	case StatusDraft, StatusDisabled:
		return nil
	case StatusActive:
		if description == "" {
			return ErrDescriptionRequired
		}
		return nil
	default:
		return ErrStatusInvalid
	}
}

func normalizeSearchQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > MaxSearchQueryRunes {
		return "", ErrQueryInvalid
	}
	for _, character := range query {
		if unicode.IsControl(character) {
			return "", ErrQueryInvalid
		}
	}
	return query, nil
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(value) + "%"
}

func sniffMIMEType(content []byte) (string, error) {
	switch {
	case len(content) >= 3 && content[0] == 0xff && content[1] == 0xd8 && content[2] == 0xff:
		return "image/jpeg", nil
	case len(content) >= 8 && bytes.Equal(content[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", nil
	case len(content) >= 6 && (bytes.Equal(content[:6], []byte("GIF87a")) || bytes.Equal(content[:6], []byte("GIF89a"))):
		return "image/gif", nil
	case len(content) >= 12 && bytes.Equal(content[:4], []byte("RIFF")) && bytes.Equal(content[8:12], []byte("WEBP")):
		return "image/webp", nil
	default:
		return "", ErrUnsupportedMIME
	}
}
