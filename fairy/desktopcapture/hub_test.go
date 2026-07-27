package desktopcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"

	"fairy/memory"
	"fairy/session"
)

type fakeCaptureStore struct {
	mu              sync.Mutex
	record          memory.ToolExecutionRecord
	turnFailureCode string
	loadCount       int
	waitReadyAtLoad int
	waitReady       chan struct{}
	allowComplete   chan struct{}
	completeWon     chan struct{}
	releaseComplete chan struct{}
}

func (s *fakeCaptureStore) ListRecoverableToolExecutions(context.Context) ([]memory.ToolExecutionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID == "" {
		return nil, nil
	}
	return []memory.ToolExecutionRecord{s.record}, nil
}

func (s *fakeCaptureStore) FailTurn(_, _, code, _ string, _ bool) error {
	s.mu.Lock()
	s.turnFailureCode = code
	s.mu.Unlock()
	return nil
}

func (s *fakeCaptureStore) CreateToolExecution(_ context.Context, input memory.CreateToolExecutionInput) (memory.ToolExecutionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record = memory.ToolExecutionRecord{
		ID: "execution-created", ConversationID: input.ConversationID, TurnID: input.TurnID, CallID: input.CallID,
		ToolName: input.ToolName, Status: memory.ToolExecutionPending, DeadlineAtUnixMS: input.DeadlineAtUnixMS,
	}
	return s.record, nil
}

func (s *fakeCaptureStore) CancelToolExecutionsForTurn(_ context.Context, conversationID, turnID, code, message string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Status == memory.ToolExecutionPending && s.record.ConversationID == conversationID && s.record.TurnID == turnID {
		s.record.Status = memory.ToolExecutionCancelled
		s.record.ErrorCode = &code
		s.record.ErrorMessage = &message
		return 1, nil
	}
	return 0, nil
}

func (s *fakeCaptureStore) LoadToolExecution(_ context.Context, id string) (memory.ToolExecutionRecord, bool, error) {
	s.mu.Lock()
	s.loadCount++
	record := s.record
	ready := s.waitReady != nil && s.loadCount == s.waitReadyAtLoad
	s.mu.Unlock()
	if ready {
		close(s.waitReady)
	}
	return record, record.ID == id, nil
}

func (s *fakeCaptureStore) MarkToolExecutionDispatched(_ context.Context, id string) (memory.ToolExecutionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != id || s.record.Status != memory.ToolExecutionPending || s.record.DeadlineAtUnixMS <= time.Now().UnixMilli() {
		return memory.ToolExecutionRecord{}, false, nil
	}
	s.record.AttemptCount++
	return s.record, true, nil
}

func (s *fakeCaptureStore) CompleteToolExecution(_ context.Context, input memory.CompleteToolExecutionInput) (memory.ToolExecutionRecord, bool, error) {
	if s.allowComplete != nil {
		<-s.allowComplete
	}
	s.mu.Lock()
	if s.record.Status != memory.ToolExecutionPending || s.record.ID != input.ID || s.record.ConversationID != input.ConversationID || s.record.TurnID != input.TurnID || s.record.CallID != input.CallID {
		s.mu.Unlock()
		return memory.ToolExecutionRecord{}, false, nil
	}
	s.record.Status = memory.ToolExecutionCompleted
	record := s.record
	s.mu.Unlock()
	if s.completeWon != nil {
		close(s.completeWon)
	}
	if s.releaseComplete != nil {
		<-s.releaseComplete
	}
	return record, true, nil
}

func (s *fakeCaptureStore) FailToolExecution(_ context.Context, id, code, message string) (memory.ToolExecutionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Status != memory.ToolExecutionPending || s.record.ID != id {
		return memory.ToolExecutionRecord{}, false, nil
	}
	s.record.Status = memory.ToolExecutionFailed
	s.record.ErrorCode = &code
	s.record.ErrorMessage = &message
	return s.record, true, nil
}

func TestCaptureHubSettlesRecoveredPendingAndCompletedTurns(t *testing.T) {
	for _, status := range []memory.ToolExecutionStatus{memory.ToolExecutionPending, memory.ToolExecutionCompleted} {
		t.Run(string(status), func(t *testing.T) {
			store := &fakeCaptureStore{record: memory.ToolExecutionRecord{
				ID: "execution-1", ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
				ToolName: memory.ToolNameDesktopObserve, Status: status,
			}}
			hub := NewCaptureHub(store)
			if err := hub.SettleRecovered(t.Context()); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if store.turnFailureCode != "DESKTOP_CAPTURE_RECOVERY_FAILED" {
				t.Fatalf("turn failure code = %q", store.turnFailureCode)
			}
			if status == memory.ToolExecutionPending && (store.record.Status != memory.ToolExecutionFailed || store.record.ErrorCode == nil || *store.record.ErrorCode != "core_restarted") {
				t.Fatalf("pending recovery = %#v", store.record)
			}
		})
	}
}

func TestCaptureHubPrivateRouteCorrelationCASAndEvidence(t *testing.T) {
	record := memory.ToolExecutionRecord{
		ID: "execution-1", ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
		ToolName: memory.ToolNameDesktopObserve, Status: memory.ToolExecutionPending,
		DeadlineAtUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	}
	store := &fakeCaptureStore{record: record}
	hub := NewCaptureHub(store)
	hub.turns[record.ID] = captureTurnRef{conversationID: record.ConversationID, turnID: record.TurnID}
	interactionContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	delivered := make(chan session.DesktopCaptureRequest, 1)
	registrationID, unregister, err := hub.Register(record.ConversationID, session.EndpointDesktop, interactionContext, func(request session.DesktopCaptureRequest) error {
		delivered <- request
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	request := DefaultDesktopCaptureRequest(record)
	if err := hub.Dispatch(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if got := <-delivered; got.ExecutionID != record.ID || got.MaxDecodedBytes != session.DesktopCaptureMaxDecodedBytes {
		t.Fatalf("delivered request = %#v", got)
	}

	result := capturePNGResult(t, record)
	if err := hub.AcceptResult(t.Context(), "wrong-registration", result); err != ErrDesktopCaptureResultRejected {
		t.Fatalf("wrong registration error = %v", err)
	}
	if err := hub.AcceptResult(t.Context(), registrationID, result); err != nil {
		t.Fatal(err)
	}
	if err := hub.AcceptResult(t.Context(), registrationID, result); err != ErrDesktopCaptureResultRejected {
		t.Fatalf("duplicate result error = %v", err)
	}
	evidence, ok := hub.TakeEvidence(record.ID)
	if !ok || evidence.MediaType != "image/png" || evidence.Width != 2 || len(evidence.Bytes) == 0 {
		t.Fatalf("evidence = %#v, %v", evidence, ok)
	}
	if _, ok := hub.TakeEvidence(record.ID); ok {
		t.Fatal("evidence was not removed after take")
	}
}

func TestCaptureHubRejectsNonPrivateRouteAndStaleUnregister(t *testing.T) {
	store := &fakeCaptureStore{record: memory.ToolExecutionRecord{ID: "execution-1"}}
	hub := NewCaptureHub(store)
	publicContext := session.Context{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat}
	if _, _, err := hub.Register("conversation-1", session.EndpointIM, publicContext, func(session.DesktopCaptureRequest) error { return nil }); err == nil {
		t.Fatal("public route was accepted")
	}
	privateContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	_, unregisterOld, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(session.DesktopCaptureRequest) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, unregisterNew, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(session.DesktopCaptureRequest) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	unregisterOld()
	if !hub.Available("conversation-1") {
		t.Fatal("stale unregister removed replacement route")
	}
	unregisterNew()
	if hub.Available("conversation-1") {
		t.Fatal("route remained after unregister")
	}
}

func TestCaptureHubBeginDispatchCompletionAndResultConsumeEphemeralEvidence(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	var registrationID string
	registrationID, unregister, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(request session.DesktopCaptureRequest) error {
		record := memory.ToolExecutionRecord{ID: request.ExecutionID, ConversationID: request.ConversationID, TurnID: request.TurnID, CallID: request.CallID}
		return hub.AcceptResult(t.Context(), registrationID, capturePNGResult(t, record))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	completed := make(chan struct{}, 1)
	execution, err := hub.Begin(t.Context(), ToolRequest{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(time.Minute),
	}, func() {
		completed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.DispatchExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("completed desktop capture did not notify coordinator")
	}
	evidence, err := hub.Result(t.Context(), execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ExecutionID != "execution-created" || evidence.MediaType != "image/png" || evidence.DataURL == "" {
		t.Fatalf("tool evidence = %#v", evidence)
	}
	if _, ok := hub.TakeEvidence(evidence.ExecutionID); ok {
		t.Fatal("Result left raw evidence in registry")
	}
}

func TestCaptureHubDispatchWithoutResultRemainsPending(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	_, unregister, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(session.DesktopCaptureRequest) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	completed := make(chan struct{}, 1)
	execution, err := hub.Begin(t.Context(), ToolRequest{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(time.Minute),
	}, func() {
		completed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.DispatchExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
		t.Fatal("pending execution notified without a result")
	default:
	}
	store.mu.Lock()
	status := store.record.Status
	store.mu.Unlock()
	if status != memory.ToolExecutionPending {
		t.Fatalf("execution status = %q, want pending", status)
	}
	if err := hub.CancelTurn(t.Context(), execution.ConversationID, execution.TurnID); err != nil {
		t.Fatal(err)
	}
	<-completed
}

func TestCaptureHubCancelTurnNotifiesCoordinatorAndRejectsLateResult(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	dispatched := make(chan session.DesktopCaptureRequest, 1)
	registrationID, unregister, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(request session.DesktopCaptureRequest) error {
		dispatched <- request
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	completed := make(chan struct{}, 1)
	execution, err := hub.Begin(t.Context(), ToolRequest{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(time.Minute),
	}, func() {
		completed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.DispatchExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	request := <-dispatched
	if err := hub.CancelTurn(t.Context(), request.ConversationID, request.TurnID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("cancelled execution did not notify coordinator")
	}
	if _, resultErr := hub.Result(t.Context(), execution.ID); resultErr == nil {
		t.Fatal("cancelled execution returned successful result")
	} else {
		var typed *ToolError
		if !errors.As(resultErr, &typed) || typed.Code != "turn_cancelled" {
			t.Fatalf("Result cancellation error = %v", resultErr)
		}
	}
	record := memory.ToolExecutionRecord{ID: request.ExecutionID, ConversationID: request.ConversationID, TurnID: request.TurnID, CallID: request.CallID}
	if err := hub.AcceptResult(t.Context(), registrationID, capturePNGResult(t, record)); err != ErrDesktopCaptureResultRejected {
		t.Fatalf("late result error = %v", err)
	}
}

func TestCaptureHubCloseFailsPendingExecutionAndNotifiesCoordinator(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	_, _, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(session.DesktopCaptureRequest) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{}, 1)
	execution, err := hub.Begin(t.Context(), ToolRequest{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(time.Minute),
	}, func() {
		completed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.DispatchExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("Close did not notify coordinator")
	}
	if _, resultErr := hub.Result(t.Context(), execution.ID); resultErr == nil {
		t.Fatal("closed execution returned successful result")
	} else {
		var typed *ToolError
		if !errors.As(resultErr, &typed) || typed.Code != "capture_unavailable" {
			t.Fatalf("Result close error = %v", resultErr)
		}
	}
}

func TestCaptureHubDeadlineNotifiesCoordinatorAndRejectsLateResult(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied}
	dispatched := make(chan session.DesktopCaptureRequest, 1)
	registrationID, unregister, err := hub.Register("conversation-1", session.EndpointDesktop, privateContext, func(request session.DesktopCaptureRequest) error {
		dispatched <- request
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	completed := make(chan struct{}, 1)
	execution, err := hub.Begin(t.Context(), ToolRequest{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(25 * time.Millisecond),
	}, func() {
		completed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.DispatchExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	request := <-dispatched
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("deadline did not notify coordinator")
	}
	if _, resultErr := hub.Result(t.Context(), execution.ID); resultErr == nil {
		t.Fatal("deadline execution returned successful result")
	} else {
		var typed *ToolError
		if !errors.As(resultErr, &typed) || typed.Code != "deadline_exceeded" {
			t.Fatalf("Result deadline error = %v", resultErr)
		}
	}
	record := memory.ToolExecutionRecord{ID: request.ExecutionID, ConversationID: request.ConversationID, TurnID: request.TurnID, CallID: request.CallID}
	if err := hub.AcceptResult(t.Context(), registrationID, capturePNGResult(t, record)); err != ErrDesktopCaptureResultRejected {
		t.Fatalf("late result error = %v", err)
	}
}

func capturePNGResult(t *testing.T, record memory.ToolExecutionRecord) session.DesktopCaptureResult {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded.Bytes())
	return session.DesktopCaptureResult{
		ExecutionID: record.ID, ConversationID: record.ConversationID, TurnID: record.TurnID, CallID: record.CallID,
		Status: "completed", MediaType: "image/png", Width: 2, Height: 1, ByteCount: encoded.Len(),
		SHA256: hex.EncodeToString(digest[:]), DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()),
	}
}
