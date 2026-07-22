package companion

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/interaction"
)

func TestAmbientInboxStartsFirstMessageImmediately(t *testing.T) {
	service := NewCompanionService()
	bindAmbientInteraction(t, service)
	defer service.Close()
	started := make(chan ambientBatch, 1)
	service.ambient.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		started <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	if err := service.ObserveAmbient("c1", AmbientObservation{
		MessageID: "m1", SenderID: "u1", SenderName: "n", Text: "hi", TimestampUnixMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-started:
		if batch.evaluationReason != ParticipationReasonMessage || len(batch.messages) != 1 || !batch.messages[0].IsNew {
			t.Fatalf("batch = %#v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("first message did not start participation immediately")
	}
}

func TestObserveAmbientRejectsDirectIMSynchronously(t *testing.T) {
	service := NewCompanionService()
	AttachOwnerIdentityStore(service, staticOwnerIdentity(false))
	defer service.Close()
	if err := service.BindInteraction("c1", interaction.Binding{
		Endpoint: interaction.EndpointIM,
		Facts: interaction.Facts{
			Audience:           interaction.AudienceSingle,
			Initiation:         interaction.InitiationDirect,
			Presentation:       interaction.PresentationChat,
			PrincipalNamespace: "qq.onebot",
			PrincipalDigest:    strings.Repeat("a", 64),
		},
	}); err != nil {
		t.Fatal(err)
	}
	err := service.ObserveAmbient("c1", testAmbientObservation(1))
	if err == nil || !strings.Contains(err.Error(), "initiation=ambient") {
		t.Fatalf("ObserveAmbient() error = %v", err)
	}
	service.ambient.mu.Lock()
	defer service.ambient.mu.Unlock()
	if len(service.ambient.states) != 0 {
		t.Fatalf("direct observation was enqueued: %#v", service.ambient.states)
	}
}

func TestAmbientInboxSerializesAndKeepsLatestTwenty(t *testing.T) {
	service := NewCompanionService()
	bindAmbientInteraction(t, service)
	defer service.Close()
	started := make(chan ambientBatch, 2)
	release := make(chan ParticipationResult, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	service.ambient.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		started <- batch
		decision := <-release
		active.Add(-1)
		return decision, nil
	}
	if err := service.ObserveAmbient("c1", testAmbientObservation(0)); err != nil {
		t.Fatal(err)
	}
	first := receiveAmbientBatch(t, started)
	for i := 1; i <= maxAmbientObservations+5; i++ {
		if err := service.ObserveAmbient("c1", testAmbientObservation(i)); err != nil {
			t.Fatal(err)
		}
	}
	target := first.messages[0].MessageID
	release <- ParticipationResult{Action: ParticipationReply, TargetMessageID: &target}
	latest := receiveAmbientBatch(t, started)
	if len(latest.messages) != maxAmbientObservations || latest.messages[0].MessageID != "m6" || latest.messages[len(latest.messages)-1].MessageID != "m25" {
		t.Fatalf("latest messages = %#v", latest.messages)
	}
	release <- ParticipationResult{Action: ParticipationSilent}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if maximum.Load() <= 1 && active.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maximum.Load() > 1 {
		t.Fatalf("concurrent participation runs = %d", maximum.Load())
	}
}

func TestAmbientInboxNewObservationCancelsStaleDecision(t *testing.T) {
	service := NewCompanionService()
	bindAmbientInteraction(t, service)
	defer service.Close()
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	latest := make(chan ambientBatch, 1)
	var calls atomic.Int32
	service.ambient.decideHook = func(ctx context.Context, batch ambientBatch) (ParticipationResult, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-ctx.Done()
			close(firstCanceled)
			return ParticipationResult{}, ctx.Err()
		}
		latest <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	if err := service.ObserveAmbient("c1", testAmbientObservation(1)); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	if err := service.ObserveAmbient("c1", testAmbientObservation(2)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("stale participation context was not canceled")
	}
	batch := receiveAmbientBatch(t, latest)
	if batch.generation != 2 || len(batch.messages) != 2 || batch.messages[1].MessageID != "m2" {
		t.Fatalf("latest batch = %#v", batch)
	}
}

func TestAmbientInboxDoesNotCancelActiveReplyTurn(t *testing.T) {
	service := NewCompanionService()
	bindAmbientInteraction(t, service)
	defer service.Close()
	replyStarted := make(chan struct{})
	releaseReply := make(chan struct{})
	latest := make(chan ambientBatch, 1)
	var calls atomic.Int32
	service.ambient.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		if calls.Add(1) == 1 {
			target := batch.messages[0].MessageID
			return ParticipationResult{Action: ParticipationReply, TargetMessageID: &target}, nil
		}
		latest <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	service.ambient.submitHook = func(SubmitTurnRequest) (TurnOutcome, error) {
		close(replyStarted)
		<-releaseReply
		return TurnOutcome{}, nil
	}
	if err := service.ObserveAmbient("c1", testAmbientObservation(1)); err != nil {
		t.Fatal(err)
	}
	<-replyStarted
	service.ambient.mu.Lock()
	decisionCancel := service.ambient.states["c1"].decisionCancel
	service.ambient.mu.Unlock()
	if decisionCancel != nil {
		t.Fatal("decision cancel handle remained active during reply turn")
	}
	if err := service.ObserveAmbient("c1", testAmbientObservation(2)); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-latest:
		t.Fatalf("new decision started before active reply completed: %#v", batch)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseReply)
	batch := receiveAmbientBatch(t, latest)
	if batch.generation != 2 || len(batch.messages) != 2 || batch.messages[1].MessageID != "m2" {
		t.Fatalf("post-reply batch = %#v", batch)
	}
}

func receiveAmbientBatch(t *testing.T, ch <-chan ambientBatch) ambientBatch {
	t.Helper()
	select {
	case batch := <-ch:
		return batch
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ambient batch")
		return ambientBatch{}
	}
}

func bindAmbientInteraction(t *testing.T, service *CompanionService) {
	t.Helper()
	if err := service.BindInteraction("c1", publicAmbientBinding()); err != nil {
		t.Fatal(err)
	}
}

type staticOwnerIdentity bool

func (owner staticOwnerIdentity) IsOwner(string, string) (bool, error) {
	return bool(owner), nil
}

func testAmbientObservation(i int) AmbientObservation {
	return AmbientObservation{
		MessageID: fmt.Sprintf("m%d", i), SenderID: "u1", SenderName: "n", Text: "t", TimestampUnixMS: int64(i + 1),
	}
}
