package desktopcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"sync"
	"time"

	"fairy/runtime/ledger"
	"fairy/transport/session"

	"github.com/google/uuid"
)

var (
	ErrDesktopCaptureSurfaceUnavailable = errors.New("desktop capture surface is unavailable")
	ErrDesktopCaptureResultRejected     = errors.New("desktop capture result was rejected")
)

const captureHubExecutionCapacity = 64

type ToolRequest struct {
	ConversationID string
	TurnID         string
	CallID         string
	Deadline       time.Time
}

// Execution is the durable correlation handle returned before a desktop
// request is dispatched. The caller can therefore publish its own awaiting
// state before the desktop surface can complete the request.
type Execution struct {
	ID             string
	ConversationID string
	TurnID         string
	CallID         string
	DeadlineUnixMS int64
}

type Evidence struct {
	ExecutionID string
	MediaType   string
	Width       int
	Height      int
	DataURL     string
}

type ToolError struct {
	Code string
}

func (err *ToolError) Error() string {
	if err == nil || strings.TrimSpace(err.Code) == "" {
		return "desktop observation failed"
	}
	return "desktop observation failed: " + err.Code
}

type captureExecutionStore interface {
	CreateToolExecution(context.Context, ledger.CreateToolExecutionInput) (ledger.ToolExecutionRecord, error)
	MarkToolExecutionDispatched(context.Context, string) (ledger.ToolExecutionRecord, bool, error)
	CompleteToolExecution(context.Context, ledger.CompleteToolExecutionInput) (ledger.ToolExecutionRecord, bool, error)
	FailToolExecution(context.Context, string, string, string) (ledger.ToolExecutionRecord, bool, error)
	LoadToolExecution(context.Context, string) (ledger.ToolExecutionRecord, bool, error)
	CancelToolExecutionsForTurn(context.Context, string, string, string, string) (int64, error)
	SettleRecoveredToolExecutions(context.Context) (int64, error)
}

func (h *CaptureHub) SettleRecovered(ctx context.Context) error {
	if h == nil || h.store == nil {
		return nil
	}
	_, err := h.store.SettleRecoveredToolExecutions(ctx)
	return err
}

func (h *CaptureHub) Begin(ctx context.Context, request ToolRequest, completed func()) (Execution, error) {
	if h == nil || h.store == nil || ctx == nil {
		return Execution{}, &ToolError{Code: "capture_unavailable"}
	}
	if request.Deadline.IsZero() || !request.Deadline.After(time.Now()) {
		return Execution{}, &ToolError{Code: "deadline_exceeded"}
	}
	if completed == nil {
		return Execution{}, errors.New("desktop capture completion callback is required")
	}
	if err := h.reserveBegin(); err != nil {
		return Execution{}, err
	}
	if !h.Available(request.ConversationID) {
		h.abortBegin()
		return Execution{}, &ToolError{Code: "surface_unavailable"}
	}
	record, err := h.store.CreateToolExecution(ctx, ledger.CreateToolExecutionInput{
		ConversationID: request.ConversationID, TurnID: request.TurnID, CallID: request.CallID,
		ToolName: ledger.ToolNameDesktopObserve, DeadlineAtUnixMS: request.Deadline.UnixMilli(),
	})
	if err != nil {
		h.abortBegin()
		return Execution{}, err
	}
	if !h.commitBegin(record, completed) {
		_, _, _ = h.store.FailToolExecution(context.Background(), record.ID, "capture_unavailable", "desktop capture hub closed")
		return Execution{}, &ToolError{Code: "capture_unavailable"}
	}
	return Execution{
		ID: record.ID, ConversationID: record.ConversationID, TurnID: record.TurnID,
		CallID: record.CallID, DeadlineUnixMS: record.DeadlineAtUnixMS,
	}, nil
}

func (h *CaptureHub) reserveBegin() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return &ToolError{Code: "capture_unavailable"}
	}
	capacity := h.executionCapacity
	if capacity <= 0 {
		capacity = captureHubExecutionCapacity
	}
	if len(h.turns)+h.beginning >= capacity {
		return &ToolError{Code: "capture_overloaded"}
	}
	h.beginning++
	h.beginWG.Add(1)
	return nil
}

func (h *CaptureHub) abortBegin() {
	h.mu.Lock()
	h.beginning--
	h.mu.Unlock()
	h.beginWG.Done()
}

func (h *CaptureHub) commitBegin(record ledger.ToolExecutionRecord, completed func()) bool {
	h.mu.Lock()
	h.beginning--
	accepted := !h.closed
	if accepted {
		h.turns[record.ID] = captureTurnRef{conversationID: record.ConversationID, turnID: record.TurnID}
		h.completions[record.ID] = completed
	}
	h.mu.Unlock()
	h.beginWG.Done()
	return accepted
}

func (h *CaptureHub) DispatchExecution(ctx context.Context, execution Execution) error {
	if h == nil || h.store == nil || ctx == nil {
		return &ToolError{Code: "capture_unavailable"}
	}
	if strings.TrimSpace(execution.ID) == "" || execution.DeadlineUnixMS <= time.Now().UnixMilli() {
		_, _, _ = h.store.FailToolExecution(context.Background(), execution.ID, "deadline_exceeded", "desktop capture deadline exceeded")
		h.notify(execution.ID)
		return &ToolError{Code: "deadline_exceeded"}
	}

	timer := time.AfterFunc(time.Until(time.UnixMilli(execution.DeadlineUnixMS)), func() {
		_, changed, failErr := h.store.FailToolExecution(context.Background(), execution.ID, "deadline_exceeded", "desktop capture deadline exceeded")
		if changed || failErr != nil {
			h.notify(execution.ID)
		}
	})
	h.mu.Lock()
	if _, active := h.completions[execution.ID]; !active {
		h.mu.Unlock()
		timer.Stop()
		return ErrDesktopCaptureResultRejected
	}
	h.timers[execution.ID] = timer
	h.mu.Unlock()

	request := session.DesktopCaptureRequest{
		ExecutionID: execution.ID, ConversationID: execution.ConversationID, TurnID: execution.TurnID,
		CallID: execution.CallID, DeadlineUnixMS: execution.DeadlineUnixMS,
		MaxDecodedBytes: session.DesktopCaptureMaxDecodedBytes, MaxDimension: session.DesktopCaptureMaxDimension,
		AllowedMIMETypes: []string{"image/png", "image/jpeg"},
	}
	if err := h.Dispatch(ctx, request); err != nil {
		_, _, _ = h.store.FailToolExecution(context.Background(), execution.ID, "dispatch_failed", "desktop capture dispatch failed")
		h.notify(execution.ID)
		return &ToolError{Code: "dispatch_failed"}
	}
	return nil
}

func (h *CaptureHub) Result(ctx context.Context, executionID string) (Evidence, error) {
	if h == nil || h.store == nil || ctx == nil {
		return Evidence{}, &ToolError{Code: "capture_unavailable"}
	}
	settled, ok, err := h.store.LoadToolExecution(ctx, executionID)
	if err != nil {
		return Evidence{}, err
	}
	if !ok {
		return Evidence{}, &ToolError{Code: "execution_missing"}
	}
	switch settled.Status {
	case ledger.ToolExecutionCompleted:
		defer h.release(executionID)
		evidence, ok := h.TakeEvidence(executionID)
		if !ok {
			return Evidence{}, &ToolError{Code: "evidence_lost"}
		}
		return Evidence{
			ExecutionID: evidence.ExecutionID, MediaType: evidence.MediaType,
			Width: evidence.Width, Height: evidence.Height, DataURL: evidence.DataURL(),
		}, nil
	case ledger.ToolExecutionFailed, ledger.ToolExecutionCancelled:
		defer h.release(executionID)
		code := "capture_failed"
		if settled.ErrorCode != nil && *settled.ErrorCode != "" {
			code = *settled.ErrorCode
		}
		return Evidence{}, &ToolError{Code: code}
	default:
		h.mu.Lock()
		_, completionActive := h.completions[executionID]
		h.mu.Unlock()
		if !completionActive {
			h.release(executionID)
		}
		return Evidence{}, &ToolError{Code: "execution_unsettled"}
	}
}

func (h *CaptureHub) CancelTurn(ctx context.Context, conversationID, turnID string) error {
	if h == nil || h.store == nil {
		return nil
	}
	count, err := h.store.CancelToolExecutionsForTurn(ctx, conversationID, turnID, "turn_cancelled", "turn was cancelled")
	if err == nil && count > 0 {
		h.mu.Lock()
		ids := make([]string, 0)
		for executionID, ref := range h.turns {
			if ref.conversationID == conversationID && ref.turnID == turnID {
				ids = append(ids, executionID)
			}
		}
		h.mu.Unlock()
		for _, executionID := range ids {
			h.notify(executionID)
			h.release(executionID)
		}
	}
	return err
}

type captureRoute struct {
	id   string
	send func(session.DesktopCaptureRequest) error
}

type CaptureEvidence struct {
	ExecutionID string
	MediaType   string
	Width       int
	Height      int
	Bytes       []byte
}

type CaptureHub struct {
	store captureExecutionStore

	mu                sync.Mutex
	routes            map[string]captureRoute
	evidence          map[string]CaptureEvidence
	completions       map[string]func()
	timers            map[string]*time.Timer
	turns             map[string]captureTurnRef
	executionCapacity int
	beginning         int
	beginWG           sync.WaitGroup
	closed            bool
}

type captureTurnRef struct {
	conversationID string
	turnID         string
}

func NewCaptureHub(store captureExecutionStore) *CaptureHub {
	return &CaptureHub{
		store: store, routes: make(map[string]captureRoute),
		evidence: make(map[string]CaptureEvidence), completions: make(map[string]func()),
		timers: make(map[string]*time.Timer), turns: make(map[string]captureTurnRef),
		executionCapacity: captureHubExecutionCapacity,
	}
}

// Register attaches one authenticated private desktop session to its conversation.
func (h *CaptureHub) Register(conversationID string, endpoint session.EndpointKind, context session.Context, send func(session.DesktopCaptureRequest) error) (string, func(), error) {
	conversationID = strings.TrimSpace(conversationID)
	if h == nil || h.store == nil {
		return "", nil, errors.New("capture hub is not configured")
	}
	if conversationID == "" || send == nil {
		return "", nil, errors.New("capture route is incomplete")
	}
	if endpoint != session.EndpointDesktop || context.Audience != session.AudienceSingle || context.Presentation != session.PresentationEmbodied {
		return "", nil, errors.New("capture route requires a private desktop interaction")
	}
	id := uuid.NewString()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return "", nil, errors.New("capture hub is closed")
	}
	h.routes[conversationID] = captureRoute{id: id, send: send}
	h.mu.Unlock()
	var once sync.Once
	return id, func() {
		once.Do(func() {
			h.mu.Lock()
			if route, ok := h.routes[conversationID]; ok && route.id == id {
				delete(h.routes, conversationID)
			}
			h.mu.Unlock()
		})
	}, nil
}

func (h *CaptureHub) Available(conversationID string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.routes[strings.TrimSpace(conversationID)]
	return ok
}

func (h *CaptureHub) Dispatch(ctx context.Context, request session.DesktopCaptureRequest) error {
	if h == nil || h.store == nil {
		return ErrDesktopCaptureSurfaceUnavailable
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if request.DeadlineUnixMS <= time.Now().UnixMilli() {
		return errors.New("desktop capture request has expired")
	}
	record, ok, err := h.store.LoadToolExecution(ctx, request.ExecutionID)
	if err != nil {
		return err
	}
	if !ok || record.Status != ledger.ToolExecutionPending || record.ConversationID != request.ConversationID || record.TurnID != request.TurnID || record.CallID != request.CallID || record.DeadlineAtUnixMS != request.DeadlineUnixMS {
		return ErrDesktopCaptureResultRejected
	}
	h.mu.Lock()
	route, ok := h.routes[request.ConversationID]
	h.mu.Unlock()
	if !ok {
		return ErrDesktopCaptureSurfaceUnavailable
	}
	if _, changed, err := h.store.MarkToolExecutionDispatched(ctx, request.ExecutionID); err != nil {
		return err
	} else if !changed {
		return ErrDesktopCaptureResultRejected
	}
	return route.send(request)
}

func (h *CaptureHub) AcceptResult(ctx context.Context, registrationID string, result session.DesktopCaptureResult) error {
	if h == nil || h.store == nil {
		return ErrDesktopCaptureResultRejected
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := result.ValidateShape(); err != nil {
		return err
	}
	h.mu.Lock()
	route, routeOK := h.routes[result.ConversationID]
	h.mu.Unlock()
	if !routeOK || route.id != registrationID {
		return ErrDesktopCaptureResultRejected
	}
	record, ok, err := h.store.LoadToolExecution(ctx, result.ExecutionID)
	if err != nil {
		return err
	}
	if !ok || record.Status != ledger.ToolExecutionPending || record.ConversationID != result.ConversationID || record.TurnID != result.TurnID || record.CallID != result.CallID || record.DeadlineAtUnixMS <= time.Now().UnixMilli() {
		return ErrDesktopCaptureResultRejected
	}
	if result.Status == "failed" {
		if !validCaptureErrorCode(result.ErrorCode) {
			return errors.New("desktop capture error code is invalid")
		}
		_, changed, err := h.store.FailToolExecution(ctx, result.ExecutionID, result.ErrorCode, "desktop capture failed")
		if err != nil {
			return err
		}
		if !changed {
			return ErrDesktopCaptureResultRejected
		}
		h.notify(result.ExecutionID)
		return nil
	}

	evidence, err := validateCaptureEvidence(result)
	if err != nil {
		return err
	}
	_, changed, err := h.store.CompleteToolExecution(ctx, ledger.CompleteToolExecutionInput{
		ID: result.ExecutionID, ConversationID: result.ConversationID, TurnID: result.TurnID, CallID: result.CallID,
		ResultMediaType: result.MediaType, ResultWidth: result.Width, ResultHeight: result.Height,
		ResultByteCount: result.ByteCount, ResultSHA256: result.SHA256,
	})
	if err != nil || !changed {
		if err != nil {
			return err
		}
		return ErrDesktopCaptureResultRejected
	}
	h.mu.Lock()
	active, activeOK := h.turns[result.ExecutionID]
	if activeOK && active.conversationID == result.ConversationID && active.turnID == result.TurnID {
		h.evidence[result.ExecutionID] = evidence
	}
	h.mu.Unlock()
	h.notify(result.ExecutionID)
	return nil
}

func (h *CaptureHub) TakeEvidence(executionID string) (CaptureEvidence, bool) {
	if h == nil {
		return CaptureEvidence{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	evidence, ok := h.evidence[executionID]
	delete(h.evidence, executionID)
	return evidence, ok
}

func (h *CaptureHub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		h.beginWG.Wait()
		return
	}
	h.closed = true
	completions := h.completions
	timers := h.timers
	h.routes = make(map[string]captureRoute)
	h.evidence = make(map[string]CaptureEvidence)
	h.completions = make(map[string]func())
	h.timers = make(map[string]*time.Timer)
	h.turns = make(map[string]captureTurnRef)
	h.mu.Unlock()
	for _, timer := range timers {
		timer.Stop()
	}
	h.beginWG.Wait()
	for executionID, completed := range completions {
		_, _, _ = h.store.FailToolExecution(context.Background(), executionID, "capture_unavailable", "desktop capture hub closed")
		completed()
	}
}

func (h *CaptureHub) release(executionID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	timer := h.timers[executionID]
	delete(h.timers, executionID)
	delete(h.completions, executionID)
	delete(h.turns, executionID)
	delete(h.evidence, executionID)
	h.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (h *CaptureHub) notify(executionID string) {
	h.mu.Lock()
	completed := h.completions[executionID]
	timer := h.timers[executionID]
	delete(h.completions, executionID)
	delete(h.timers, executionID)
	h.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if completed != nil {
		completed()
	}
}

func validateCaptureEvidence(result session.DesktopCaptureResult) (CaptureEvidence, error) {
	if result.MediaType != "image/png" && result.MediaType != "image/jpeg" {
		return CaptureEvidence{}, errors.New("desktop capture MIME type is unsupported")
	}
	prefix := "data:" + result.MediaType + ";base64,"
	if !strings.HasPrefix(result.DataURL, prefix) {
		return CaptureEvidence{}, errors.New("desktop capture data URL MIME type does not match")
	}
	encoded := strings.TrimPrefix(result.DataURL, prefix)
	if base64.StdEncoding.DecodedLen(len(encoded)) > session.DesktopCaptureMaxDecodedBytes {
		return CaptureEvidence{}, errors.New("desktop capture exceeds byte limit")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > session.DesktopCaptureMaxDecodedBytes {
		return CaptureEvidence{}, errors.New("desktop capture data URL is invalid")
	}
	if len(decoded) != result.ByteCount {
		return CaptureEvidence{}, errors.New("desktop capture byte count does not match")
	}
	digest := sha256.Sum256(decoded)
	if hex.EncodeToString(digest[:]) != result.SHA256 {
		return CaptureEvidence{}, errors.New("desktop capture digest does not match")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		return CaptureEvidence{}, errors.New("desktop capture image is invalid")
	}
	wantFormat := strings.TrimPrefix(result.MediaType, "image/")
	if format == "jpeg" && wantFormat == "jpeg" {
		// expected
	} else if format != wantFormat {
		return CaptureEvidence{}, errors.New("desktop capture encoded format does not match")
	}
	if config.Width != result.Width || config.Height != result.Height || config.Width > session.DesktopCaptureMaxDimension || config.Height > session.DesktopCaptureMaxDimension || int64(config.Width)*int64(config.Height) > session.DesktopCaptureMaxPixels {
		return CaptureEvidence{}, errors.New("desktop capture dimensions are invalid")
	}
	return CaptureEvidence{ExecutionID: result.ExecutionID, MediaType: result.MediaType, Width: result.Width, Height: result.Height, Bytes: decoded}, nil
}

func validCaptureErrorCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func DefaultDesktopCaptureRequest(record ledger.ToolExecutionRecord) session.DesktopCaptureRequest {
	return session.DesktopCaptureRequest{
		ExecutionID: record.ID, ConversationID: record.ConversationID, TurnID: record.TurnID, CallID: record.CallID,
		DeadlineUnixMS: record.DeadlineAtUnixMS, MaxDecodedBytes: session.DesktopCaptureMaxDecodedBytes,
		MaxDimension: session.DesktopCaptureMaxDimension, AllowedMIMETypes: []string{"image/png", "image/jpeg"},
	}
}

func (e CaptureEvidence) DataURL() string {
	return fmt.Sprintf("data:%s;base64,%s", e.MediaType, base64.StdEncoding.EncodeToString(e.Bytes))
}
