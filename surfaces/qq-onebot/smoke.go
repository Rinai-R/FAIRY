package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fairy/transport/session"
)

const (
	defaultSmokeWait       = 60 * time.Second
	minimumSmokeWait       = time.Second
	maximumSmokeWait       = 5 * time.Minute
	defaultSmokePoll       = 500 * time.Millisecond
	maxOneBotSmokeResponse = 64 << 10
)

type smokeError string

const (
	errSmokeConfigInvalid       smokeError = "config_invalid"
	errSmokeMessageIDInvalid    smokeError = "message_id_invalid"
	errSmokeReadinessFailed     smokeError = "readiness_failed"
	errSmokeTraceQueryFailed    smokeError = "trace_query_failed"
	errSmokeTraceNotFound       smokeError = "trace_not_found"
	errSmokeTraceAmbiguous      smokeError = "trace_ambiguous"
	errSmokeTraceNotCompleted   smokeError = "trace_not_completed"
	errSmokeReceiptMissing      smokeError = "delivery_receipt_missing"
	errSmokeReceiptAmbiguous    smokeError = "delivery_receipt_ambiguous"
	errSmokeReceiptInvalid      smokeError = "delivery_receipt_invalid"
	errSmokeOutboundUnavailable smokeError = "outbound_message_unavailable"
)

func (err smokeError) Error() string { return string(err) }

type smokeTraceClient interface {
	TracesByMessageID(context.Context, string) (session.TraceSearchResponse, error)
	Trace(context.Context, string) (session.MessageTraceDetail, error)
}

type deliverySmokePolicy struct {
	wait         time.Duration
	pollInterval time.Duration
}

type deliverySmokeResult struct {
	TraceID           string
	TurnID            string
	InboundMessageID  string
	OutboundMessageID string
}

func runDeliverySmoke(ctx context.Context, cfg Config, messageID string, wait time.Duration, output io.Writer) error {
	return runDeliverySmokeWithPolicy(ctx, cfg, messageID, output, deliverySmokePolicy{wait: wait, pollInterval: defaultSmokePoll})
}

func runDeliverySmokeWithPolicy(ctx context.Context, cfg Config, messageID string, output io.Writer, policy deliverySmokePolicy) error {
	if err := cfg.Validate(); err != nil {
		return errSmokeConfigInvalid
	}
	if !session.ValidCorrelationID(messageID) {
		return errSmokeMessageIDInvalid
	}
	if output == nil || policy.wait <= 0 || policy.pollInterval <= 0 || policy.pollInterval > policy.wait {
		return errSmokeConfigInvalid
	}
	if err := runReadinessCheck(ctx, cfg); err != nil {
		return errSmokeReadinessFailed
	}
	client, err := session.New(session.Options{Endpoint: cfg.CoreEndpoint, Token: cfg.CoreToken, Timeout: readinessRequestTimeout})
	if err != nil {
		return errSmokeConfigInvalid
	}
	waitCtx, cancel := context.WithTimeout(ctx, policy.wait)
	defer cancel()
	result, err := awaitDeliverySmokeEvidence(waitCtx, client, cfg, messageID, policy.pollInterval)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "PASS trace=%s turn=%s inbound=%s outbound=%s\n", result.TraceID, result.TurnID, result.InboundMessageID, result.OutboundMessageID)
	if err != nil {
		return errSmokeConfigInvalid
	}
	return nil
}

func awaitDeliverySmokeEvidence(ctx context.Context, client smokeTraceClient, cfg Config, messageID string, pollInterval time.Duration) (deliverySmokeResult, error) {
	lastMissing := smokeError(errSmokeTraceNotFound)
	for {
		search, err := client.TracesByMessageID(ctx, messageID)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return deliverySmokeResult{}, lastMissing
			}
			return deliverySmokeResult{}, errSmokeTraceQueryFailed
		}
		switch len(search.Traces) {
		case 0:
			lastMissing = errSmokeTraceNotFound
		case 1:
			trace := search.Traces[0]
			if trace.Status == "completed" {
				if trace.TurnID == "" {
					return deliverySmokeResult{}, errSmokeTraceNotCompleted
				}
				detail, err := client.Trace(ctx, trace.TraceID)
				if err != nil {
					return deliverySmokeResult{}, errSmokeTraceQueryFailed
				}
				if detail.MessageID != messageID || detail.ConversationID != trace.ConversationID || detail.TurnID != trace.TurnID || detail.Status != "completed" {
					return deliverySmokeResult{}, errSmokeTraceNotCompleted
				}
				externalMessageID, err := uniqueSucceededDeliveryReceipt(detail)
				if err != nil {
					return deliverySmokeResult{}, err
				}
				if err := lookupOneBotMessage(ctx, cfg, externalMessageID); err != nil {
					return deliverySmokeResult{}, errSmokeOutboundUnavailable
				}
				return deliverySmokeResult{TraceID: trace.TraceID, TurnID: trace.TurnID, InboundMessageID: messageID, OutboundMessageID: externalMessageID}, nil
			}
			if trace.Status != "running" && trace.Status != "pending" {
				return deliverySmokeResult{}, errSmokeTraceNotCompleted
			}
			lastMissing = errSmokeTraceNotCompleted
		default:
			return deliverySmokeResult{}, errSmokeTraceAmbiguous
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return deliverySmokeResult{}, lastMissing
		case <-timer.C:
		}
	}
}

func uniqueSucceededDeliveryReceipt(detail session.MessageTraceDetail) (string, error) {
	var externalMessageID string
	for _, span := range detail.Spans {
		if span.Operation != "Surface 回执" || span.Category != "delivery" || span.Status != "completed" || span.Attributes["status"] != "succeeded" {
			continue
		}
		candidate := span.Attributes["externalMessageId"]
		if !session.ValidCorrelationID(candidate) {
			return "", errSmokeReceiptInvalid
		}
		if externalMessageID != "" {
			return "", errSmokeReceiptAmbiguous
		}
		externalMessageID = candidate
	}
	if externalMessageID == "" {
		return "", errSmokeReceiptMissing
	}
	return externalMessageID, nil
}

func lookupOneBotMessage(ctx context.Context, cfg Config, externalMessageID string) error {
	messageID, err := strconv.ParseInt(externalMessageID, 10, 64)
	if err != nil || messageID <= 0 || strconv.FormatInt(messageID, 10) != externalMessageID {
		return errSmokeOutboundUnavailable
	}
	body, _ := json.Marshal(map[string]int64{"message_id": messageID})
	requestCtx, cancel := context.WithTimeout(ctx, readinessRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(cfg.OneBotAPIEndpoint, "/")+"/get_msg", bytes.NewReader(body))
	if err != nil {
		return errSmokeOutboundUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+cfg.OneBotToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: readinessRequestTimeout}).Do(request)
	if err != nil {
		return errSmokeOutboundUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errSmokeOutboundUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOneBotSmokeResponse+1))
	if err != nil || len(raw) > maxOneBotSmokeResponse {
		return errSmokeOutboundUnavailable
	}
	var result struct {
		Status  string `json:"status"`
		RetCode int64  `json:"retcode"`
		Data    struct {
			MessageID json.RawMessage `json:"message_id"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return errSmokeOutboundUnavailable
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || result.Status != "ok" || result.RetCode != 0 {
		return errSmokeOutboundUnavailable
	}
	var returnedNumber int64
	if err := json.Unmarshal(result.Data.MessageID, &returnedNumber); err == nil {
		if returnedNumber == messageID {
			return nil
		}
		return errSmokeOutboundUnavailable
	}
	var returnedString string
	if err := json.Unmarshal(result.Data.MessageID, &returnedString); err != nil || returnedString != externalMessageID {
		return errSmokeOutboundUnavailable
	}
	return nil
}
