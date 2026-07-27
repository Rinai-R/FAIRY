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

	"fairy/contracts/interaction"
	"fairy/contracts/session"
	"fairy/memory"
	"github.com/google/uuid"
)

var (
	ErrDesktopCaptureSurfaceUnavailable = errors.New("desktop capture surface is unavailable")
	ErrDesktopCaptureResultRejected     = errors.New("desktop capture result was rejected")
)

type ToolRequest struct {
	ConversationID string
	TurnID         string
	CallID         string
	Deadline       time.Time
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
	CreateToolExecution(context.Context, memory.CreateToolExecutionInput) (memory.ToolExecutionRecord, error)
	MarkToolExecutionDispatched(context.Context, string) (memory.ToolExecutionRecord, bool, error)
	CompleteToolExecution(context.Context, memory.CompleteToolExecutionInput) (memory.ToolExecutionRecord, bool, error)
	FailToolExecution(context.Context, string, string, string) (memory.ToolExecutionRecord, bool, error)
	LoadToolExecution(context.Context, string) (memory.ToolExecutionRecord, bool, error)
	CancelToolExecutionsForTurn(context.Context, string, string, string, string) (int64, error)
	ListRecoverableToolExecutions(context.Context) ([]memory.ToolExecutionRecord, error)
	FailTurn(string, string, string, string, bool) error
}

func (h *CaptureHub) SettleRecovered(ctx context.Context) error {
	if h == nil || h.store == nil {
		return nil
	}
	records, err := h.store.ListRecoverableToolExecutions(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		code := "evidence_lost"
		message := "desktop capture evidence was lost during Core restart"
		if record.Status == memory.ToolExecutionPending {
			code = "core_restarted"
			message = "desktop capture was interrupted by Core restart"
			if _, _, err := h.store.FailToolExecution(ctx, record.ID, code, message); err != nil {
				return err
			}
		}
		if err := h.store.FailTurn(record.ConversationID, record.TurnID, "DESKTOP_CAPTURE_RECOVERY_FAILED", message, false); err != nil {
			return err
		}
	}
	return nil
}

func (h *CaptureHub) Observe(ctx context.Context, request ToolRequest) (Evidence, error) {
	if h == nil || h.store == nil || ctx == nil {
		return Evidence{}, &ToolError{Code: "capture_unavailable"}
	}
	if !h.Available(request.ConversationID) {
		return Evidence{}, &ToolError{Code: "surface_unavailable"}
	}
	if request.Deadline.IsZero() || !request.Deadline.After(time.Now()) {
		return Evidence{}, &ToolError{Code: "deadline_exceeded"}
	}
	record, err := h.store.CreateToolExecution(ctx, memory.CreateToolExecutionInput{
		ConversationID: request.ConversationID, TurnID: request.TurnID, CallID: request.CallID,
		ToolName: memory.ToolNameDesktopObserve, DeadlineAtUnixMS: request.Deadline.UnixMilli(),
	})
	if err != nil {
		return Evidence{}, err
	}
	h.mu.Lock()
	h.turns[record.ID] = captureTurnRef{conversationID: record.ConversationID, turnID: record.TurnID}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.turns, record.ID)
		delete(h.evidence, record.ID)
		h.mu.Unlock()
	}()
	if err := h.Dispatch(ctx, DefaultDesktopCaptureRequest(record)); err != nil {
		_, _, _ = h.store.FailToolExecution(context.Background(), record.ID, "dispatch_failed", "desktop capture dispatch failed")
		return Evidence{}, &ToolError{Code: "dispatch_failed"}
	}
	waitCtx, cancel := context.WithDeadline(ctx, request.Deadline)
	err = h.Wait(waitCtx, record.ID)
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			_, _ = h.store.CancelToolExecutionsForTurn(context.Background(), request.ConversationID, request.TurnID, "turn_cancelled", "turn was cancelled")
			return Evidence{}, err
		}
		_, _, _ = h.store.FailToolExecution(context.Background(), record.ID, "deadline_exceeded", "desktop capture deadline exceeded")
		return Evidence{}, &ToolError{Code: "deadline_exceeded"}
	}
	settled, ok, err := h.store.LoadToolExecution(context.Background(), record.ID)
	if err != nil {
		return Evidence{}, err
	}
	if !ok {
		return Evidence{}, &ToolError{Code: "execution_missing"}
	}
	switch settled.Status {
	case memory.ToolExecutionCompleted:
		evidence, ok := h.TakeEvidence(record.ID)
		if !ok {
			return Evidence{}, &ToolError{Code: "evidence_lost"}
		}
		return Evidence{
			ExecutionID: evidence.ExecutionID, MediaType: evidence.MediaType,
			Width: evidence.Width, Height: evidence.Height, DataURL: evidence.DataURL(),
		}, nil
	case memory.ToolExecutionFailed, memory.ToolExecutionCancelled:
		code := "capture_failed"
		if settled.ErrorCode != nil && *settled.ErrorCode != "" {
			code = *settled.ErrorCode
		}
		return Evidence{}, &ToolError{Code: code}
	default:
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

	mu       sync.Mutex
	routes   map[string]captureRoute
	evidence map[string]CaptureEvidence
	waiters  map[string][]chan struct{}
	turns    map[string]captureTurnRef
}

type captureTurnRef struct {
	conversationID string
	turnID         string
}

func NewCaptureHub(store captureExecutionStore) *CaptureHub {
	return &CaptureHub{
		store: store, routes: make(map[string]captureRoute),
		evidence: make(map[string]CaptureEvidence), waiters: make(map[string][]chan struct{}),
		turns: make(map[string]captureTurnRef),
	}
}

// Register attaches one authenticated private desktop session to its conversation.
func (h *CaptureHub) Register(conversationID string, endpoint interaction.EndpointKind, context interaction.Context, send func(session.DesktopCaptureRequest) error) (string, func(), error) {
	conversationID = strings.TrimSpace(conversationID)
	if h == nil || h.store == nil {
		return "", nil, errors.New("capture hub is not configured")
	}
	if conversationID == "" || send == nil {
		return "", nil, errors.New("capture route is incomplete")
	}
	if endpoint != interaction.EndpointDesktop || context.Audience != interaction.AudienceSingle || context.Presentation != interaction.PresentationEmbodied {
		return "", nil, errors.New("capture route requires a private desktop interaction")
	}
	id := uuid.NewString()
	h.mu.Lock()
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
	if !ok || record.Status != memory.ToolExecutionPending || record.ConversationID != request.ConversationID || record.TurnID != request.TurnID || record.CallID != request.CallID || record.DeadlineAtUnixMS != request.DeadlineUnixMS {
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
	if !ok || record.Status != memory.ToolExecutionPending || record.ConversationID != result.ConversationID || record.TurnID != result.TurnID || record.CallID != result.CallID || record.DeadlineAtUnixMS <= time.Now().UnixMilli() {
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
	_, changed, err := h.store.CompleteToolExecution(ctx, memory.CompleteToolExecutionInput{
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
	waiters := h.waiters
	h.routes = make(map[string]captureRoute)
	h.evidence = make(map[string]CaptureEvidence)
	h.waiters = make(map[string][]chan struct{})
	h.turns = make(map[string]captureTurnRef)
	h.mu.Unlock()
	for _, executionWaiters := range waiters {
		for _, waiter := range executionWaiters {
			close(waiter)
		}
	}
}

func (h *CaptureHub) Wait(ctx context.Context, executionID string) error {
	if h == nil || h.store == nil || ctx == nil {
		return errors.New("capture wait is not configured")
	}
	record, ok, err := h.store.LoadToolExecution(ctx, executionID)
	if err != nil || !ok || record.Status != memory.ToolExecutionPending {
		return err
	}
	wake := make(chan struct{})
	h.mu.Lock()
	h.waiters[executionID] = append(h.waiters[executionID], wake)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		waiters := h.waiters[executionID]
		for index, waiter := range waiters {
			if waiter == wake {
				h.waiters[executionID] = append(waiters[:index], waiters[index+1:]...)
				break
			}
		}
		if len(h.waiters[executionID]) == 0 {
			delete(h.waiters, executionID)
		}
		h.mu.Unlock()
	}()
	// Close the subscribe-after-complete race by checking persistent state again.
	record, ok, err = h.store.LoadToolExecution(ctx, executionID)
	if err != nil || !ok || record.Status != memory.ToolExecutionPending {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
		return nil
	}
}

func (h *CaptureHub) notify(executionID string) {
	h.mu.Lock()
	waiters := append([]chan struct{}(nil), h.waiters[executionID]...)
	delete(h.waiters, executionID)
	h.mu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
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

func DefaultDesktopCaptureRequest(record memory.ToolExecutionRecord) session.DesktopCaptureRequest {
	return session.DesktopCaptureRequest{
		ExecutionID: record.ID, ConversationID: record.ConversationID, TurnID: record.TurnID, CallID: record.CallID,
		DeadlineUnixMS: record.DeadlineAtUnixMS, MaxDecodedBytes: session.DesktopCaptureMaxDecodedBytes,
		MaxDimension: session.DesktopCaptureMaxDimension, AllowedMIMETypes: []string{"image/png", "image/jpeg"},
	}
}

func (e CaptureEvidence) DataURL() string {
	return fmt.Sprintf("data:%s;base64,%s", e.MediaType, base64.StdEncoding.EncodeToString(e.Bytes))
}
