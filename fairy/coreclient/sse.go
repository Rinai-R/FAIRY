package coreclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxSSELine  = 64 << 10
	maxSSEFrame = 256 << 10
)

type SSEEvent struct {
	ID    string
	Event string
	Data  []byte
}

type Stream struct {
	body    io.ReadCloser
	decoder *SSEDecoder
	cancel  context.CancelFunc
	once    sync.Once
}

type EventStream interface {
	Next() (SSEEvent, error)
	Close() error
}

func (s *Stream) Next() (SSEEvent, error) {
	if s == nil || s.decoder == nil {
		return SSEEvent{}, errors.New("stream is not open")
	}
	return s.decoder.Next()
}

func (s *Stream) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		s.cancel()
		err = s.body.Close()
	})
	return err
}

type SSEDecoder struct {
	reader *bufio.Reader
}

func NewSSEDecoder(reader io.Reader) *SSEDecoder {
	return &SSEDecoder{reader: bufio.NewReaderSize(reader, maxSSELine)}
}

func (d *SSEDecoder) Next() (SSEEvent, error) {
	var event SSEEvent
	var data []string
	frameSize := 0
	for {
		line, err := d.reader.ReadString('\n')
		if len(line) > maxSSELine {
			return SSEEvent{}, errors.New("SSE line exceeds 64 KiB")
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) == 0 && frameSize == 0 {
				return SSEEvent{}, io.EOF
			}
			return SSEEvent{}, fmt.Errorf("incomplete SSE frame: %w", err)
		}
		frameSize += len(line)
		if frameSize > maxSSEFrame {
			return SSEEvent{}, errors.New("SSE frame exceeds 256 KiB")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if event.ID == "" && event.Event == "" && len(data) == 0 {
				continue
			}
			event.Data = []byte(strings.Join(data, "\n"))
			return event, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "id":
			event.ID = value
		case "event":
			event.Event = value
		case "data":
			data = append(data, value)
		}
	}
}

func (c *Client) openReadyStream(ctx context.Context, action, path string, readyTimeout time.Duration) (EventStream, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	type result struct {
		stream *Stream
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.url(path), nil)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		c.authorize(req)
		res, err := c.http.Do(req)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			err := c.responseError(action, path, res)
			res.Body.Close()
			resultCh <- result{err: err}
			return
		}
		mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
		if err != nil || mediaType != "text/event-stream" {
			res.Body.Close()
			resultCh <- result{err: errors.New("response content type is not text/event-stream")}
			return
		}
		stream := &Stream{body: res.Body, decoder: NewSSEDecoder(res.Body), cancel: cancel}
		ready, err := stream.Next()
		if err != nil {
			stream.Close()
			resultCh <- result{err: err}
			return
		}
		if ready.Event != "ready" {
			stream.Close()
			resultCh <- result{err: fmt.Errorf("first SSE event is %q, want ready", ready.Event)}
			return
		}
		resultCh <- result{stream: stream}
	}()
	if readyTimeout <= 0 {
		readyTimeout = c.timeout
	}
	timer := time.NewTimer(readyTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, &Error{Action: action, Endpoint: c.url(path), Message: "SSE ready timeout"}
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			var clientErr *Error
			if errors.As(result.err, &clientErr) {
				return nil, clientErr
			}
			return nil, &Error{Action: action, Endpoint: c.url(path), Message: redactClientError(result.err.Error())}
		}
		return result.stream, nil
	}
}
