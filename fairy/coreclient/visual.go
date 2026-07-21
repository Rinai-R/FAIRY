package coreclient

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maxVisualAssetBody = 16 << 20

func (c *Client) VisualAsset(ctx context.Context, packID, assetPath string) ([]byte, error) {
	if packID == "" || strings.ContainsAny(packID, "/\\?#") {
		return nil, errors.New("visual pack ID is invalid")
	}
	parts := strings.Split(assetPath, "/")
	if len(parts) == 0 || !strings.HasSuffix(assetPath, ".png") {
		return nil, errors.New("visual asset path is invalid")
	}
	escaped := make([]string, len(parts))
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\?#") {
			return nil, errors.New("visual asset path is invalid")
		}
		escaped[i] = url.PathEscape(part)
	}
	path := "/v1/visual-assets/" + url.PathEscape(packID) + "/" + strings.Join(escaped, "/")
	requestCtx, cancel := c.finiteContext(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, &Error{Action: "read visual asset", Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	c.authorize(req)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Action: "read visual asset", Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, c.responseError("read visual asset", path, res)
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mediaType != "image/png" {
		return nil, &Error{Action: "read visual asset", Endpoint: c.url(path), Status: res.StatusCode, Message: "response content type is not image/png"}
	}
	return readBounded(res.Body, maxVisualAssetBody)
}
