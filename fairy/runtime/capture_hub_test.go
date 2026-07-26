package runtime

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

	"fairy/companion"
	"fairy/contracts/interaction"
	"fairy/contracts/session"
	"fairy/memory"
)

type fakeCaptureStore struct {
	mu              sync.Mutex
	record          memory.ToolExecutionRecord
	turnFailureCode string
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
	defer s.mu.Unlock()
	return s.record, s.record.ID == id, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Status != memory.ToolExecutionPending || s.record.ID != input.ID || s.record.ConversationID != input.ConversationID || s.record.TurnID != input.TurnID || s.record.CallID != input.CallID {
		return memory.ToolExecutionRecord{}, false, nil
	}
	s.record.Status = memory.ToolExecutionCompleted
	return s.record, true, nil
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
	interactionContext := interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}
	delivered := make(chan session.DesktopCaptureRequest, 1)
	registrationID, unregister, err := hub.Register(record.ConversationID, interaction.EndpointDesktop, interactionContext, func(request session.DesktopCaptureRequest) error {
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
	publicContext := interaction.Context{Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient, Presentation: interaction.PresentationChat}
	if _, _, err := hub.Register("conversation-1", interaction.EndpointIM, publicContext, func(session.DesktopCaptureRequest) error { return nil }); err == nil {
		t.Fatal("public route was accepted")
	}
	privateContext := interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}
	_, unregisterOld, err := hub.Register("conversation-1", interaction.EndpointDesktop, privateContext, func(session.DesktopCaptureRequest) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, unregisterNew, err := hub.Register("conversation-1", interaction.EndpointDesktop, privateContext, func(session.DesktopCaptureRequest) error { return nil })
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

func TestCaptureHubObserveCompletesAndConsumesEphemeralEvidence(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}
	var registrationID string
	registrationID, unregister, err := hub.Register("conversation-1", interaction.EndpointDesktop, privateContext, func(request session.DesktopCaptureRequest) error {
		record := memory.ToolExecutionRecord{ID: request.ExecutionID, ConversationID: request.ConversationID, TurnID: request.TurnID, CallID: request.CallID}
		return hub.AcceptResult(t.Context(), registrationID, capturePNGResult(t, record))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	evidence, err := hub.Observe(t.Context(), companion.DesktopToolRequest{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ExecutionID != "execution-created" || evidence.MediaType != "image/png" || evidence.DataURL == "" {
		t.Fatalf("tool evidence = %#v", evidence)
	}
	if _, ok := hub.TakeEvidence(evidence.ExecutionID); ok {
		t.Fatal("Observe left raw evidence in registry")
	}
}

func TestCaptureHubCancelTurnWakesObserveAndRejectsLateResult(t *testing.T) {
	store := &fakeCaptureStore{}
	hub := NewCaptureHub(store)
	privateContext := interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied}
	dispatched := make(chan session.DesktopCaptureRequest, 1)
	registrationID, unregister, err := hub.Register("conversation-1", interaction.EndpointDesktop, privateContext, func(request session.DesktopCaptureRequest) error {
		dispatched <- request
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	result := make(chan error, 1)
	go func() {
		_, observeErr := hub.Observe(t.Context(), companion.DesktopToolRequest{
			ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1", Deadline: time.Now().Add(time.Minute),
		})
		result <- observeErr
	}()
	request := <-dispatched
	if err := hub.CancelTurn(t.Context(), request.ConversationID, request.TurnID); err != nil {
		t.Fatal(err)
	}
	select {
	case observeErr := <-result:
		var typed *companion.DesktopToolError
		if !errors.As(observeErr, &typed) || typed.Code != "turn_cancelled" {
			t.Fatalf("Observe cancellation error = %v", observeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Observe did not wake")
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
