package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fairy/transport/session"
)

func TestRunDeliverySmokeUsesReadOnlyControlledPeers(t *testing.T) {
	const (
		inboundID  = "71001"
		outboundID = "71002"
	)
	coreToken := "core-smoke-secret"
	oneBotToken := "onebot-smoke-secret"
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+coreToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			fmt.Fprint(w, `{"bootstrap":{},"configRoot":"/data","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0,"database":{"ready":true,"mode":"production"},"secretKey":{"ready":true,"mode":"production"}}`)
		case "/v1/traces":
			if r.URL.Query().Get("messageId") != inboundID {
				t.Errorf("messageId = %q", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"messageId":%q,"traces":[{"traceId":"trace-1","messageId":%q,"source":"direct","conversationId":"conversation-1","turnId":"turn-1","status":"completed","receivedAtUnixMs":1,"completedAtUnixMs":2}]}`, inboundID, inboundID)
		case "/v1/traces/trace-1":
			fmt.Fprintf(w, `{"traceId":"trace-1","messageId":%q,"conversationId":"conversation-1","turnId":"turn-1","source":"direct","status":"completed","startedAtUnixMs":1,"endedAtUnixMs":2,"spans":[{"spanId":"receipt-1","operation":"Surface 回执","category":"delivery","status":"completed","startedAtUnixMs":2,"endedAtUnixMs":2,"durationMs":0,"attributes":{"beatId":"b1","status":"succeeded","externalMessageId":%q}}]}`, inboundID, outboundID)
		default:
			http.NotFound(w, r)
		}
	}))
	defer core.Close()

	oneBot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+oneBotToken || r.Method != http.MethodPost {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/get_login_info":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":10001}}`)
		case "/get_msg":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":71002,"message":"must-not-be-read"}}`)
		default:
			t.Fatalf("smoke performed non-read-only OneBot action %q", r.URL.Path)
		}
	}))
	defer oneBot.Close()
	listener := newReadinessListener(t)
	defer listener.Close()
	cfg := Config{
		CoreEndpoint: core.URL, CoreToken: coreToken,
		OneBotWebhookEndpoint: "http://" + listener.Addr().String(), OneBotAPIEndpoint: oneBot.URL, OneBotToken: oneBotToken,
	}
	var output bytes.Buffer
	err := runDeliverySmokeWithPolicy(t.Context(), cfg, inboundID, &output, deliverySmokePolicy{wait: time.Second, pollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "PASS trace=trace-1 turn=turn-1 inbound=71001 outbound=71002\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	for _, forbidden := range []string{coreToken, oneBotToken, "must-not-be-read", "Bearer"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("output leaked %q: %q", forbidden, output.String())
		}
	}
}

func TestAwaitDeliverySmokeEvidenceFailureContracts(t *testing.T) {
	oneBotOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":71002}}`)
	}))
	defer oneBotOK.Close()
	oneBotFailed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret response body", http.StatusUnauthorized)
	}))
	defer oneBotFailed.Close()

	completed := smokeTraceSummary("completed")
	tests := []struct {
		name   string
		client *smokeTraceClientFake
		api    string
		wait   time.Duration
		want   smokeError
	}{
		{name: "ambiguous", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{completed, completed}}}, api: oneBotOK.URL, want: errSmokeTraceAmbiguous},
		{name: "not completed", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{smokeTraceSummary("failed")}}}, api: oneBotOK.URL, want: errSmokeTraceNotCompleted},
		{name: "receipt missing", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{completed}}, detail: smokeTraceDetail()}, api: oneBotOK.URL, want: errSmokeReceiptMissing},
		{name: "receipt invalid", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{completed}}, detail: smokeTraceDetail(smokeReceiptSpan(" invalid "))}, api: oneBotOK.URL, want: errSmokeReceiptInvalid},
		{name: "receipt ambiguous", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{completed}}, detail: smokeTraceDetail(smokeReceiptSpan("71002"), smokeReceiptSpan("71003"))}, api: oneBotOK.URL, want: errSmokeReceiptAmbiguous},
		{name: "lookup failed", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{completed}}, detail: smokeTraceDetail(smokeReceiptSpan("71002"))}, api: oneBotFailed.URL, want: errSmokeOutboundUnavailable},
		{name: "deadline", client: &smokeTraceClientFake{search: session.TraceSearchResponse{MessageID: "71001", Traces: []session.MessageTrace{}}}, api: oneBotOK.URL, wait: 25 * time.Millisecond, want: errSmokeTraceNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wait := test.wait
			if wait == 0 {
				wait = time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), wait)
			defer cancel()
			_, err := awaitDeliverySmokeEvidence(ctx, test.client, Config{OneBotAPIEndpoint: test.api, OneBotToken: "onebot-secret"}, "71001", 5*time.Millisecond)
			if err == nil || err.Error() != test.want.Error() {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			for _, forbidden := range []string{"onebot-secret", "secret response body", "Bearer"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

type smokeTraceClientFake struct {
	search session.TraceSearchResponse
	detail session.MessageTraceDetail
}

func (f *smokeTraceClientFake) TracesByMessageID(context.Context, string) (session.TraceSearchResponse, error) {
	return f.search, nil
}

func (f *smokeTraceClientFake) Trace(context.Context, string) (session.MessageTraceDetail, error) {
	return f.detail, nil
}

func smokeTraceSummary(status string) session.MessageTrace {
	return session.MessageTrace{
		TraceID: "trace-1", MessageID: "71001", Source: "direct", ConversationID: "conversation-1", TurnID: "turn-1", Status: status, ReceivedAtUnixMS: 1,
	}
}

func smokeTraceDetail(spans ...session.TraceSpan) session.MessageTraceDetail {
	return session.MessageTraceDetail{
		TraceID: "trace-1", MessageID: "71001", ConversationID: "conversation-1", TurnID: "turn-1", Source: "direct", Status: "completed", StartedAtUnixMS: 1, EndedAtUnixMS: 2, Spans: spans,
	}
}

func smokeReceiptSpan(externalMessageID string) session.TraceSpan {
	return session.TraceSpan{
		SpanID: "receipt-1", Operation: "Surface 回执", Category: "delivery", Status: "completed", StartedAtUnixMS: 2, EndedAtUnixMS: 2,
		Attributes: map[string]string{"beatId": "b1", "status": "succeeded", "externalMessageId": externalMessageID},
	}
}
