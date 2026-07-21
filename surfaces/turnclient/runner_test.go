package turnclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/coreclient"
)

func TestRunnerCompletedDeliversTypedBeatsAndWaitsForSubmit(t *testing.T) {
	allow := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/v1/sessions/c1/events" {
			fmt.Fprint(w, "event: ready\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			<-allow
			writeHarnessTestEvent(w, 1, "responding", `{"type":"beat.ready","beatId":"b1","kind":"final","displayText":"你好","visualState":"idle"}`)
			writeHarnessTestEvent(w, 2, "completed", `{"type":"completed","text":"你好"}`)
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/v1/sessions/c1/turns" {
			close(allow)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"你好"},"surface":"desktop"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	result, err := runner.Run(context.Background(), Request{ConversationID: "c1", Input: "你好"}, func(event Event) error {
		types = append(types, event.Type)
		if event.Type == "beat.ready" && (event.Beat == nil || event.Beat.DisplayText != "你好") {
			t.Fatal("beat was not decoded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Terminal.Type != "completed" || strings.Join(types, ",") != "beat.ready,completed" {
		t.Fatalf("result=%#v types=%v", result, types)
	}
}

func TestRunnerDisconnectReturnsErrorAndCancelsKnownTurn(t *testing.T) {
	var cancelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sessions/c1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: ready\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			writeHarnessTestEvent(w, 1, "responding", `{"type":"state_changed"}`)
			return
		case r.URL.Path == "/v1/sessions/c1/turns":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"outcome":{"conversationId":"c1","turnId":"t1","responseText":"断开"},"surface":"desktop"}`)
		case r.URL.Path == "/v1/sessions/c1/turns/t1/cancel":
			cancelCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, _ := coreclient.New(coreclient.Options{Endpoint: server.URL})
	runner, _ := New(client, time.Second)
	_, err := runner.Run(context.Background(), Request{ConversationID: "c1", Input: "断开"}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "disconnected before terminal") {
		t.Fatalf("Run() error = %v", err)
	}
	if cancelCalls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancelCalls.Load())
	}
}

func TestDecodeInterruptedAndFailedTerminals(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   string
		payload string
	}{
		{name: "interrupted", state: "interrupted", payload: `{"type":"state_changed"}`},
		{name: "failed", state: "failed", payload: `{"type":"failed","error":{"code":"MODEL_FAILED","message":"provider unavailable","retryable":true}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := coreclient.SSEEvent{Event: "harness", Data: []byte(fmt.Sprintf(`{"conversationId":"c1","turnId":"t1","sequence":1,"state":%q,"payload":%s}`, test.state, test.payload))}
			event, err := DecodeEvent(raw)
			if err != nil || !isTerminal(event) {
				t.Fatalf("event=%#v error=%v", event, err)
			}
			if test.name == "failed" && (event.Failure == nil || event.Failure.Code != "MODEL_FAILED") {
				t.Fatalf("failed payload = %#v", event.Failure)
			}
		})
	}
}

func TestDecodeEventRejectsInvalidBeat(t *testing.T) {
	_, err := DecodeEvent(coreclient.SSEEvent{Event: "harness", Data: []byte(`{"conversationId":"c1","turnId":"t1","sequence":1,"state":"responding","payload":{"type":"beat.ready"}}`)})
	if err == nil {
		t.Fatal("invalid beat accepted")
	}
}

func writeHarnessTestEvent(w io.Writer, sequence uint64, state, payload string) {
	fmt.Fprintf(w, "event: harness\ndata: {\"conversationId\":\"c1\",\"turnId\":\"t1\",\"sequence\":%d,\"state\":%q,\"payload\":%s}\n\n", sequence, state, payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
