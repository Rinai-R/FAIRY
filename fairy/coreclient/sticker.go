package coreclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
)

const MaxStickerContentBytes = 5 << 20

type CreateStickerInput struct {
	Filename    string
	MIMEType    string
	Content     []byte
	Description string
	Tags        []string
	Status      string
}

type UpdateStickerInput struct {
	Description *string   `json:"description,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	Status      *string   `json:"status,omitempty"`
}

func (c *Client) ListStickers(ctx context.Context, status string, offset, limit int) (StickerPage, error) {
	values := url.Values{}
	if status != "" {
		values.Set("status", status)
	}
	if offset != 0 {
		values.Set("offset", strconv.Itoa(offset))
	}
	if limit != 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/stickers"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page StickerPage
	err := c.doJSON(ctx, "list stickers", http.MethodGet, path, nil, &page)
	if err == nil && page.Items == nil {
		err = errors.New("sticker response is missing items")
	}
	return page, err
}

func (c *Client) CreateSticker(ctx context.Context, input CreateStickerInput) (StickerRecord, error) {
	if len(input.Content) == 0 {
		return StickerRecord{}, errors.New("sticker content is required")
	}
	if len(input.Content) > MaxStickerContentBytes {
		return StickerRecord{}, errors.New("sticker content exceeds 5 MiB")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name": "file", "filename": input.Filename,
	})
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", input.MIMEType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return StickerRecord{}, err
	}
	if _, err := part.Write(input.Content); err != nil {
		return StickerRecord{}, err
	}
	tags, err := json.Marshal(input.Tags)
	if err != nil {
		return StickerRecord{}, err
	}
	for name, value := range map[string]string{
		"description": input.Description,
		"tags":        string(tags),
		"status":      input.Status,
	} {
		if err := writer.WriteField(name, value); err != nil {
			return StickerRecord{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return StickerRecord{}, err
	}
	requestCtx, cancel := c.finiteContext(ctx)
	defer cancel()
	path := "/v1/stickers"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.url(path), &body)
	if err != nil {
		return StickerRecord{}, &Error{Action: "create sticker", Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.authorize(req)
	res, err := c.http.Do(req)
	if err != nil {
		return StickerRecord{}, &Error{Action: "create sticker", Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return StickerRecord{}, c.responseError("create sticker", path, res)
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return StickerRecord{}, &Error{Action: "create sticker", Endpoint: c.url(path), Status: res.StatusCode, Message: "response content type is not application/json"}
	}
	var record StickerRecord
	if err := decodeBoundedJSON(res.Body, maxJSONBody, &record); err != nil {
		return StickerRecord{}, err
	}
	return record, nil
}

func (c *Client) UpdateSticker(ctx context.Context, id string, input UpdateStickerInput) (StickerRecord, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return StickerRecord{}, err
	}
	path := "/v1/stickers/" + url.PathEscape(id)
	var record StickerRecord
	err = c.doJSON(ctx, "update sticker", http.MethodPut, path, body, &record)
	return record, err
}

func (c *Client) DeleteSticker(ctx context.Context, id string) error {
	path := "/v1/stickers/" + url.PathEscape(id)
	var result struct {
		Deleted bool `json:"deleted"`
	}
	if err := c.doJSON(ctx, "delete sticker", http.MethodDelete, path, nil, &result); err != nil {
		return err
	}
	if !result.Deleted {
		return errors.New("sticker delete response did not confirm deletion")
	}
	return nil
}

func (c *Client) ReadStickerContent(ctx context.Context, id string) (StickerContent, error) {
	requestCtx, cancel := c.finiteContext(ctx)
	defer cancel()
	path := "/v1/stickers/" + url.PathEscape(id) + "/content"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return StickerContent{}, &Error{Action: "read sticker content", Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	c.authorize(req)
	res, err := c.http.Do(req)
	if err != nil {
		return StickerContent{}, &Error{Action: "read sticker content", Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return StickerContent{}, c.responseError("read sticker content", path, res)
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || !supportedStickerMIME(mediaType) {
		return StickerContent{}, &Error{Action: "read sticker content", Endpoint: c.url(path), Status: res.StatusCode, Message: "response content type is not a supported sticker image"}
	}
	content, err := readBounded(res.Body, MaxStickerContentBytes)
	if err != nil {
		return StickerContent{}, &Error{Action: "read sticker content", Endpoint: c.url(path), Status: res.StatusCode, Message: err.Error()}
	}
	if len(content) == 0 {
		return StickerContent{}, &Error{Action: "read sticker content", Endpoint: c.url(path), Status: res.StatusCode, Message: "response sticker content is empty"}
	}
	return StickerContent{
		MIMEType: mediaType, ContentSHA256: res.Header.Get("X-Content-SHA256"), Bytes: content,
	}, nil
}

func supportedStickerMIME(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
