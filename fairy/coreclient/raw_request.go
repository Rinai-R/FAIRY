package coreclient

import (
	"bytes"
	"context"
	"mime"
	"net/http"
)

func (c *Client) doJSONWithoutTimeout(ctx context.Context, action, method, path string, body []byte, out any) error {
	if len(body) > maxRequestBody {
		return &Error{Action: action, Endpoint: c.url(path), Message: "request body exceeds 1 MiB"}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	res, err := c.http.Do(req)
	if err != nil {
		return &Error{Action: action, Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return c.responseError(action, path, res)
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &Error{Action: action, Endpoint: c.url(path), Status: res.StatusCode, Message: "response content type is not application/json"}
	}
	return decodeBoundedJSON(res.Body, maxJSONBody, out)
}
