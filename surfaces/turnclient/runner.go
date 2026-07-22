package turnclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"fairy/coreclient"
)

const defaultReadyTimeout = 15 * time.Second

type CoreClient interface {
	OpenEvents(context.Context, string, time.Duration) (coreclient.EventStream, error)
	SubmitTurn(context.Context, string, coreclient.SubmitTurnRequest) (coreclient.SubmitTurnResponse, error)
	CancelTurn(context.Context, string, string) error
}

type Runner struct {
	client       CoreClient
	readyTimeout time.Duration
}

type Request struct {
	ConversationID string
	Input          string
	SpeechEnabled  bool
}

type Event struct {
	Harness coreclient.HarnessEvent
	Type    string
	Beat    *BeatReady
	Failure *Failure
}

type BeatReady struct {
	BeatID               string `json:"beatId"`
	Kind                 string `json:"kind"`
	Index                uint8  `json:"index"`
	ChainIndex           int    `json:"chainIndex"`
	DisplayText          string `json:"displayText"`
	SpeechText           string `json:"speechText"`
	VisualState          string `json:"visualState"`
	TargetIntervalMS     int64  `json:"targetIntervalMs"`
	PaceWaitMS           int64  `json:"paceWaitMs"`
	PublishedPrefixCount int    `json:"publishedPrefixCount"`
	Reason               string `json:"reason,omitempty"`
	DataURL              string `json:"dataUrl,omitempty"`
}

type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Result struct {
	Response coreclient.SubmitTurnResponse
	Terminal Event
}

type Callback func(Event) error

func New(client CoreClient, readyTimeout time.Duration) (*Runner, error) {
	if client == nil {
		return nil, errors.New("core client is required")
	}
	if readyTimeout <= 0 {
		readyTimeout = defaultReadyTimeout
	}
	return &Runner{client: client, readyTimeout: readyTimeout}, nil
}

func (r *Runner) Run(ctx context.Context, request Request, callback Callback) (Result, error) {
	if r == nil || r.client == nil {
		return Result{}, errors.New("turn runner is not configured")
	}
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if request.ConversationID == "" || request.Input == "" {
		return Result{}, errors.New("conversation ID and input are required")
	}
	if callback == nil {
		return Result{}, errors.New("event callback is required")
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	stream, err := r.client.OpenEvents(runCtx, request.ConversationID, r.readyTimeout)
	if err != nil {
		return Result{}, err
	}
	defer stream.Close()

	type submitResult struct {
		response coreclient.SubmitTurnResponse
		err      error
	}
	submitCh := make(chan submitResult, 1)
	go func() {
		response, submitErr := r.client.SubmitTurn(runCtx, request.ConversationID, coreclient.SubmitTurnRequest{
			Input: request.Input, SpeechEnabled: request.SpeechEnabled,
		})
		submitCh <- submitResult{response: response, err: submitErr}
	}()

	eventCh := make(chan streamResult, 1)
	go readEvents(stream, eventCh)

	var turnID string
	var lastSequence uint64
	var terminal *Event
	var submitted *submitResult
	cancelOnce := sync.Once{}
	cancelTurn := func() {
		stop()
		if turnID == "" {
			return
		}
		cancelOnce.Do(func() {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = r.client.CancelTurn(cancelCtx, request.ConversationID, turnID)
		})
	}
	for terminal == nil || submitted == nil {
		select {
		case <-ctx.Done():
			cancelTurn()
			return Result{}, ctx.Err()
		case result := <-submitCh:
			submitted = &result
			if result.err != nil {
				cancelTurn()
				return Result{}, result.err
			}
			if turnID != "" && result.response.Outcome.TurnID != turnID {
				cancelTurn()
				return Result{}, errors.New("submit response turn ID does not match event stream")
			}
		case result, ok := <-eventCh:
			if !ok {
				cancelTurn()
				return Result{}, errors.New("event stream closed before terminal event")
			}
			if result.err != nil {
				cancelTurn()
				if errors.Is(result.err, io.EOF) {
					return Result{}, errors.New("event stream disconnected before terminal event")
				}
				return Result{}, result.err
			}
			event := result.event
			if event.Harness.ConversationID != request.ConversationID {
				cancelTurn()
				return Result{}, errors.New("event conversation ID does not match request")
			}
			if turnID == "" {
				turnID = event.Harness.TurnID
			} else if event.Harness.TurnID != turnID {
				cancelTurn()
				return Result{}, errors.New("event stream contains multiple turns")
			}
			if event.Harness.Sequence <= lastSequence {
				cancelTurn()
				return Result{}, errors.New("event sequence is not strictly increasing")
			}
			lastSequence = event.Harness.Sequence
			if err := callback(event); err != nil {
				cancelTurn()
				return Result{}, fmt.Errorf("turn event callback: %w", err)
			}
			if isTerminal(event) {
				terminal = &event
				_ = stream.Close()
			}
		}
	}
	if submitted.response.Outcome.TurnID != terminal.Harness.TurnID {
		return Result{}, errors.New("terminal event does not match submit response")
	}
	return Result{Response: submitted.response, Terminal: *terminal}, nil
}

func readEvents(stream coreclient.EventStream, output chan<- streamResult) {
	defer close(output)
	for {
		raw, err := stream.Next()
		if err != nil {
			output <- streamResult{err: err}
			return
		}
		if raw.Event != "harness" {
			continue
		}
		event, err := DecodeEvent(raw)
		if err != nil {
			output <- streamResult{err: err}
			return
		}
		output <- streamResult{event: event}
	}
}

type streamResult struct {
	event Event
	err   error
}

func DecodeEvent(raw coreclient.SSEEvent) (Event, error) {
	harness, err := coreclient.DecodeHarnessEvent(raw)
	if err != nil {
		return Event{}, err
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(harness.Payload, &envelope); err != nil || envelope.Type == "" {
		return Event{}, errors.New("harness payload is missing type")
	}
	event := Event{Harness: harness, Type: envelope.Type}
	switch envelope.Type {
	case "beat.ready":
		var beat BeatReady
		if err := json.Unmarshal(harness.Payload, &beat); err != nil || beat.BeatID == "" || beat.DisplayText == "" {
			return Event{}, errors.New("invalid beat.ready payload")
		}
		event.Beat = &beat
	case "failed":
		var payload struct {
			Error Failure `json:"error"`
		}
		if err := json.Unmarshal(harness.Payload, &payload); err != nil || payload.Error.Code == "" {
			return Event{}, errors.New("invalid failed payload")
		}
		event.Failure = &payload.Error
	}
	return event, nil
}

func isTerminal(event Event) bool {
	switch event.Harness.State {
	case "completed":
		return event.Type == "completed"
	case "interrupted":
		return event.Type == "state_changed"
	case "failed":
		return event.Type == "failed"
	default:
		return false
	}
}
