package presence

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/runtime/model"
)

func TestExperienceLoopProjectsOneObservationAcrossBothHorizons(t *testing.T) {
	host := newLearningTestHost()
	loop := NewExperienceLoop(host, host)
	t.Cleanup(loop.Close)
	registration := feedbackTestSnapshot(0).registration
	if !loop.CompleteReply(registration) {
		t.Fatal("CompleteReply = false")
	}
	if loop.CompleteReply(registration) {
		t.Fatal("duplicate CompleteReply = true")
	}
	observation := learningObservations()[0]
	loop.Observe(registration.ConversationID, observation)
	if !loop.EnqueueEpisode(registration.ConversationID, []AmbientObservation{observation}) {
		t.Fatal("EnqueueEpisode = false")
	}

	loop.feedback.mu.Lock()
	pending := loop.feedback.pending[registration.ConversationID][registration.TurnID]
	observed := 0
	if pending != nil {
		observed = len(pending.observations)
	}
	loop.feedback.mu.Unlock()
	stats := loop.Stats()
	if observed != 1 || stats.Feedback.Registered != 1 || stats.Feedback.Dropped != 1 || stats.Learning.Enqueued != 1 {
		t.Fatalf("observed=%d stats=%#v", observed, stats)
	}
	if stats.CacheIdentityVersion != model.PromptCacheKeyVersion {
		t.Fatalf("cache identity version = %q", stats.CacheIdentityVersion)
	}
}

func TestExperienceLoopKeepsLearningAndFeedbackFailuresIsolated(t *testing.T) {
	host := newLearningTestHost()
	host.modelErr = errors.New("provider unavailable")
	loop := newExperienceLoop(
		NewLearningEngine(host, 2),
		newFeedbackEngine(host, 2, 2, time.Millisecond),
	)
	t.Cleanup(loop.Close)
	if !loop.EnqueueEpisode("conversation-1", learningObservations()) {
		t.Fatal("EnqueueEpisode = false")
	}
	registration := feedbackTestSnapshot(0).registration
	if !loop.CompleteReply(registration) {
		t.Fatal("CompleteReply = false")
	}
	loop.Observe(registration.ConversationID, learningObservations()[0])

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := loop.Stats()
		if stats.Learning.Failed == 1 && stats.Feedback.Failed == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("failure stats = %#v", loop.Stats())
}

func TestExperienceLoopCloseRejectsBothAdmissions(t *testing.T) {
	host := newLearningTestHost()
	loop := NewExperienceLoop(host, host)
	loop.Close()
	if loop.EnqueueEpisode("conversation-1", learningObservations()) {
		t.Fatal("post-close episode accepted")
	}
	if loop.CompleteReply(feedbackTestSnapshot(0).registration) {
		t.Fatal("post-close feedback accepted")
	}
}

func TestExperienceLoopReportsLearningQueueDrop(t *testing.T) {
	host := newLearningTestHost()
	host.block = true
	host.started = make(chan struct{}, 1)
	loop := newExperienceLoop(NewLearningEngine(host, 1), nil)
	t.Cleanup(loop.Close)
	if !loop.EnqueueEpisode("conversation-1", learningObservations()) {
		t.Fatal("first episode was rejected")
	}
	select {
	case <-host.started:
	case <-time.After(time.Second):
		t.Fatal("learning worker did not start")
	}
	if !loop.EnqueueEpisode("conversation-1", learningObservations()) {
		t.Fatal("queued episode was rejected")
	}
	if loop.EnqueueEpisode("conversation-1", learningObservations()) {
		t.Fatal("overflow episode was accepted")
	}
	stats := loop.Stats()
	if stats.Learning.Enqueued != 2 || stats.Learning.Dropped != 1 {
		t.Fatalf("learning stats = %#v", stats.Learning)
	}
}

func TestExperienceLoopConcurrentCloseIsIdempotent(t *testing.T) {
	host := newLearningTestHost()
	loop := NewExperienceLoop(host, host)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop.EnqueueEpisode("conversation-1", learningObservations())
			loop.CompleteReply(feedbackTestSnapshot(0).registration)
			loop.Observe("conversation-1", learningObservations()[0])
			loop.Close()
		}()
	}
	wg.Wait()
	loop.Close()
	if loop.EnqueueEpisode("conversation-1", learningObservations()) {
		t.Fatal("episode accepted after concurrent close")
	}
}

func TestExperienceStatsJSONIsLowSensitivity(t *testing.T) {
	payload, err := json.Marshal(ExperienceStats{
		Learning:             LearningStats{Enqueued: 2, Dropped: 1, Succeeded: 1},
		Feedback:             FeedbackStats{Registered: 2, Failed: 1},
		CacheIdentityVersion: model.PromptCacheKeyVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, required := range []string{`"learning"`, `"feedback"`, `"cacheIdentityVersion"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("stats JSON missing %s: %s", required, encoded)
		}
	}
	for _, forbidden := range []string{"replyText", "evidenceMessageIds", "stablePromptHash", "promptCacheKey", "conversationId"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("stats JSON leaked %q: %s", forbidden, encoded)
		}
	}
}
