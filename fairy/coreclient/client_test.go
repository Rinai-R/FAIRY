package coreclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientStatusUsesBearerAndRejectsInvalidResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"bootstrap":{},"configRoot":"/tmp","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0,"database":{"ready":true,"mode":"production"},"qdrant":{"ready":true,"mode":"production"},"secretKey":{"ready":true,"mode":"production"}}`)
	}))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, Token: "exact-token"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil || status.ConfigRoot != "/tmp" {
		t.Fatalf("status=%#v err=%v", status, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, `{}`)
	}))
	defer bad.Close()
	client, _ = New(Options{Endpoint: bad.URL})
	if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientFiniteTimeoutAndTurnCallerLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/turns") {
			io.WriteString(w, `{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"ok"},"surface":"desktop"}`)
			return
		}
		io.WriteString(w, `{"bootstrap":{},"configRoot":"/tmp","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0}`)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, Timeout: 10 * time.Millisecond})
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("finite status request did not time out")
	}
	turn, err := client.SubmitTurn(context.Background(), "c1", SubmitTurnRequest{Input: "hello"})
	if err != nil || turn.Outcome.TurnID != "t1" {
		t.Fatalf("turn=%#v err=%v", turn, err)
	}
}

func TestClientRejectsSecretWhitespaceAndRedactsServerErrors(t *testing.T) {
	if _, err := New(Options{Token: " secret "}); err == nil {
		t.Fatal("token whitespace accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"provider Authorization: Bearer abc api_key=sk-test"}`)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	_, err := client.Status(context.Background())
	if err == nil || strings.Contains(err.Error(), "abc") || strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("error leaked credential: %v", err)
	}
}

func TestAdminRejectsMalformedAndOversizedJSONBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	if _, err := client.ApplyConfig(context.Background(), "model", []byte(`{"missing"`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	oversized := []byte(`{"value":"` + strings.Repeat("x", maxRequestBody) + `"}`)
	if _, err := client.CreateCharacter(context.Background(), oversized); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestClientRejectsOversizedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":"`+strings.Repeat("x", maxJSONBody)+`"}`)
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL})
	if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestSSEDecoderRejectsIncompleteAndOversizedFrames(t *testing.T) {
	if _, err := NewSSEDecoder(strings.NewReader("event: log\ndata: {}")).Next(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete error = %v", err)
	}
	oversized := "data: " + strings.Repeat("x", maxSSELine) + "\n\n"
	if _, err := NewSSEDecoder(strings.NewReader(oversized)).Next(); err == nil || !strings.Contains(err.Error(), "line exceeds") {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestOpenLogsReadyTimeoutAndLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not a flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: ready\ndata: {\"ok\":true}\n\n")
		flusher.Flush()
		time.Sleep(60 * time.Millisecond)
		io.WriteString(w, "id: 1\nevent: log\ndata: {\"sequence\":1,\"timestampUnixMs\":1,\"level\":\"warn\",\"logger\":\"test\",\"message\":\"late\",\"messageTruncated\":false,\"fields\":[],\"fieldsTruncated\":false}\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, Timeout: 10 * time.Millisecond})
	stream, err := client.OpenLogs(context.Background(), LogQuery{}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	event, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := DecodeLogEntry(event)
	if err != nil || entry.Message != "late" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}

	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer blocked.Close()
	client, _ = New(Options{Endpoint: blocked.URL})
	if _, err := client.OpenLogs(context.Background(), LogQuery{}, 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "ready timeout") {
		t.Fatalf("ready timeout error = %v", err)
	}
}

func TestSSEDecoderParsesMultilineData(t *testing.T) {
	event, err := NewSSEDecoder(strings.NewReader("id: 4\nevent: log\ndata: one\ndata: two\n\n")).Next()
	if err != nil || event.ID != "4" || event.Event != "log" || string(event.Data) != "one\ntwo" {
		t.Fatalf("event=%#v err=%v", event, err)
	}
}

func TestLogQueryValidation(t *testing.T) {
	client, _ := New(Options{})
	if _, err := client.Logs(context.Background(), LogQuery{Level: "verbose"}); err == nil {
		t.Fatal("invalid level accepted")
	}
	if _, err := client.OpenLogs(context.Background(), LogQuery{Limit: 2}, time.Second); err == nil {
		t.Fatal("stream limit accepted")
	}
}

func TestDecodeHarnessEventRejectsMissingFields(t *testing.T) {
	_, err := DecodeHarnessEvent(SSEEvent{Data: []byte(`{"conversationId":"c"}`)})
	if err == nil {
		t.Fatal("incomplete harness event accepted")
	}
}

func TestClientErrorSupportsErrorsAs(t *testing.T) {
	err := &Error{Action: "read", Endpoint: "http://example.test", Status: 401, Message: "unauthorized"}
	var target *Error
	if !errors.As(err, &target) || !strings.Contains(fmt.Sprint(err), "401") {
		t.Fatalf("error = %v", err)
	}
}
