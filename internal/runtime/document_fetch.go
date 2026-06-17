package runtime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const maxDocumentFetchBytes int64 = 32 << 20

func (r *Runtime) FetchDocument(ctx context.Context, req app.DocumentFetchRequest) (app.DocumentFetchResponse, error) {
	endpoint, err := parseDocumentURL(req.URL)
	if err != nil {
		return app.DocumentFetchResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return app.DocumentFetchResponse{}, fmt.Errorf("创建文档请求失败: %w", err)
	}
	httpReq.Header.Set("User-Agent", "FAIRY document fetcher/0.1")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return app.DocumentFetchResponse{}, fmt.Errorf("抓取网络文档失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return app.DocumentFetchResponse{}, fmt.Errorf("抓取网络文档失败: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxDocumentFetchBytes {
		return app.DocumentFetchResponse{}, fmt.Errorf("网络文档过大: %d bytes，当前上限 %d bytes", resp.ContentLength, maxDocumentFetchBytes)
	}

	limited := io.LimitReader(resp.Body, maxDocumentFetchBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return app.DocumentFetchResponse{}, fmt.Errorf("读取网络文档失败: %w", err)
	}
	if int64(len(data)) > maxDocumentFetchBytes {
		return app.DocumentFetchResponse{}, fmt.Errorf("网络文档超过大小上限: %d bytes", maxDocumentFetchBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	filename := filenameFromResponse(resp, endpoint)
	return app.DocumentFetchResponse{
		URL:         endpoint.String(),
		Filename:    filename,
		ContentType: contentType,
		DataBase64:  base64.StdEncoding.EncodeToString(data),
		SizeBytes:   int64(len(data)),
	}, nil
}

func parseDocumentURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, errors.New("url 不能为空")
	}
	endpoint, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("url 格式不正确: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("仅支持 http/https 网络文档: %s", endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, errors.New("url host 不能为空")
	}
	return endpoint, nil
}

func filenameFromResponse(resp *http.Response, endpoint *url.URL) string {
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		_, params, err := mime.ParseMediaType(disposition)
		if err == nil && params["filename"] != "" {
			return path.Base(params["filename"])
		}
	}
	name := path.Base(endpoint.Path)
	if name == "." || name == "/" || name == "" {
		return "network-document"
	}
	return name
}
