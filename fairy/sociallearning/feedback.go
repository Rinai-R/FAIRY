package sociallearning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fairy/memory"
	"fairy/model"
	"fairy/participation"
	"fairy/pkg/boundedqueue"
)

const (
	SocialFeedbackMaxOutputTokens    uint32 = 96
	FeedbackQueueCapacity                   = 8
	socialFeedbackMaxPendingPerGroup        = 4
	socialFeedbackObservationLimit          = 6
	socialFeedbackObservationWindow         = 2 * time.Minute
)

const SocialFeedbackInstructions = "Judge the observable social outcome of one public-group reply from only the supplied reply and later external observations. Output exactly one strict JSON object: {\"outcome\":\"positive|negative|unknown\"}. positive means the conversation visibly continues constructively, especially when another participant engages with the reply or the topic progresses. negative means the reply is explicitly corrected, rejected, makes the exchange worse, or triggers a visible repair loop. unknown means evidence is absent, ambiguous, unrelated, or only silence. Do not infer private feelings or hidden reactions. Output no reasoning, Markdown, unknown fields, null fields, or trailing data."

type FeedbackRegistration struct {
	CharacterID    string
	ConversationID string
	TurnID         string
	EntryIDs       []string
	ReplyText      string
}

type pendingFeedback struct {
	registration FeedbackRegistration
	observations []participation.AmbientObservation
	timer        *time.Timer
}

type feedbackSnapshot struct {
	registration FeedbackRegistration
	observations []participation.AmbientObservation
}

type FeedbackStats struct {
	Registered int64
	Dropped    int64
	Succeeded  int64
	Failed     int64
}

type FeedbackEngine struct {
	host   FeedbackHost
	ctx    context.Context
	cancel context.CancelFunc
	queue  *boundedqueue.Queue[feedbackSnapshot]
	wg     sync.WaitGroup
	once   sync.Once

	mu         sync.Mutex
	closed     bool
	pending    map[string]map[string]*pendingFeedback
	registered atomic.Int64
	dropped    atomic.Int64
	succeeded  atomic.Int64
	failed     atomic.Int64
}

type feedbackPromptPayload struct {
	ContextType  string                          `json:"contextType"`
	Reply        string                          `json:"reply"`
	Observations []socialLearnObservationPayload `json:"observations"`
}

type feedbackResult struct {
	Outcome string `json:"outcome"`
}

func NewFeedbackEngine(host FeedbackHost, capacity int) *FeedbackEngine {
	if capacity < 1 {
		capacity = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := &FeedbackEngine{
		host: host, ctx: ctx, cancel: cancel, queue: boundedqueue.New[feedbackSnapshot](capacity),
		pending: make(map[string]map[string]*pendingFeedback),
	}
	engine.wg.Add(1)
	go engine.run()
	return engine
}

func (e *FeedbackEngine) Register(registration FeedbackRegistration) bool {
	if e == nil || strings.TrimSpace(registration.ConversationID) == "" || strings.TrimSpace(registration.TurnID) == "" {
		return false
	}
	if strings.TrimSpace(registration.ReplyText) == "" {
		return false
	}
	registration.EntryIDs = append([]string(nil), registration.EntryIDs...)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	group := e.pending[registration.ConversationID]
	if group == nil {
		group = make(map[string]*pendingFeedback)
		e.pending[registration.ConversationID] = group
	}
	if len(group) >= socialFeedbackMaxPendingPerGroup {
		e.dropped.Add(1)
		return false
	}
	pending := &pendingFeedback{registration: registration}
	pending.timer = time.AfterFunc(socialFeedbackObservationWindow, func() {
		e.finalize(registration.ConversationID, registration.TurnID)
	})
	group[registration.TurnID] = pending
	e.registered.Add(1)
	return true
}

func (e *FeedbackEngine) Observe(conversationID string, observation participation.AmbientObservation) {
	if e == nil {
		return
	}
	e.mu.Lock()
	group := e.pending[conversationID]
	ready := make([]feedbackSnapshot, 0)
	for turnID, pending := range group {
		pending.observations = append(pending.observations, observation)
		if len(pending.observations) < socialFeedbackObservationLimit {
			continue
		}
		pending.timer.Stop()
		delete(group, turnID)
		ready = append(ready, snapshotSocialFeedback(pending))
	}
	if len(group) == 0 {
		delete(e.pending, conversationID)
	}
	e.mu.Unlock()
	for _, snapshot := range ready {
		e.enqueue(snapshot)
	}
}

func (e *FeedbackEngine) finalize(conversationID, turnID string) {
	e.mu.Lock()
	group := e.pending[conversationID]
	pending := group[turnID]
	if pending != nil {
		delete(group, turnID)
		if len(group) == 0 {
			delete(e.pending, conversationID)
		}
	}
	e.mu.Unlock()
	if pending != nil {
		e.enqueue(snapshotSocialFeedback(pending))
	}
}

func snapshotSocialFeedback(pending *pendingFeedback) feedbackSnapshot {
	return feedbackSnapshot{
		registration: pending.registration,
		observations: append([]participation.AmbientObservation(nil), pending.observations...),
	}
}

func (e *FeedbackEngine) enqueue(snapshot feedbackSnapshot) {
	select {
	case <-e.ctx.Done():
		return
	default:
	}
	if !e.queue.TryPush(snapshot) {
		e.dropped.Add(1)
	}
}

func (e *FeedbackEngine) run() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case snapshot := <-e.queue.Recv():
			if err := e.process(e.ctx, snapshot); err != nil {
				e.failed.Add(1)
				if e.host != nil {
					e.host.WarnFeedback(snapshot.registration.ConversationID, snapshot.registration.TurnID, err)
				}
				continue
			}
			e.succeeded.Add(1)
		}
	}
}

func (e *FeedbackEngine) process(ctx context.Context, snapshot feedbackSnapshot) error {
	if e.host == nil {
		return errors.New("social feedback runtime is not configured")
	}
	outcome := memory.SocialFeedbackUnknown
	if len(snapshot.observations) > 0 {
		input, err := buildSocialFeedbackInput(snapshot)
		if err != nil {
			return err
		}
		connection, err := e.host.ModelConnection()
		if err != nil {
			return err
		}
		record, err := e.host.ActiveCharacter(snapshot.registration.CharacterID)
		if err != nil {
			return err
		}
		cacheKey := ""
		if connection.Capabilities.PromptCacheKey {
			cacheKey = model.LaneCacheKey(snapshot.registration.ConversationID, model.PromptLaneSocialFeedback)
		}
		cacheInput := model.NewCacheKeyInput(model.PromptLaneSocialFeedback, connection.Model, snapshot.registration.ConversationID, SocialFeedbackInstructions)
		cacheInput.CharacterRevision = record.Revision
		events, err := e.host.ExecuteRequest(ctx, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{Lane: model.PromptLaneSocialFeedback, Model: connection.Model, Instructions: SocialFeedbackInstructions, MaxOutputTokens: SocialFeedbackMaxOutputTokens, PromptCacheKey: cacheKey},
			Input: input, CacheInput: &cacheInput,
		})
		if err != nil {
			return fmt.Errorf("executing social feedback request: %w", err)
		}
		outcome, err = compileSocialFeedback(model.CollectTextFromEvents(events))
		if err != nil {
			return err
		}
	}
	_, err := e.host.RecordSocialReplyFeedback(ctx, memory.SocialReplyFeedbackInput{
		CharacterID: snapshot.registration.CharacterID, ConversationID: snapshot.registration.ConversationID,
		TurnID: snapshot.registration.TurnID, EntryIDs: snapshot.registration.EntryIDs,
		Outcome: outcome, ObservedMessageCount: len(snapshot.observations),
	})
	return err
}

func buildSocialFeedbackInput(snapshot feedbackSnapshot) ([]model.PromptItem, error) {
	observations := make([]socialLearnObservationPayload, 0, len(snapshot.observations))
	for _, observation := range snapshot.observations {
		observations = append(observations, socialLearnObservationPayload{
			ContextType: "later_external_group_observation", MessageID: observation.MessageID,
			SenderID: observation.SenderID, SenderName: observation.SenderName, Text: observation.Text,
			TimestampUnixMS: observation.TimestampUnixMS,
		})
	}
	payload, err := json.Marshal(feedbackPromptPayload{ContextType: "public_reply_outcome_evidence", Reply: snapshot.registration.ReplyText, Observations: observations})
	if err != nil {
		return nil, fmt.Errorf("serializing social feedback input: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func compileSocialFeedback(draft string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(draft)))
	decoder.DisallowUnknownFields()
	var result feedbackResult
	if err := decoder.Decode(&result); err != nil {
		return "", fmt.Errorf("decoding social feedback result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", errors.New("social feedback result contains trailing data")
	}
	switch result.Outcome {
	case memory.SocialFeedbackPositive, memory.SocialFeedbackNegative, memory.SocialFeedbackUnknown:
		return result.Outcome, nil
	default:
		return "", errors.New("social feedback outcome is invalid")
	}
}

func (e *FeedbackEngine) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		e.mu.Lock()
		e.closed = true
		for _, group := range e.pending {
			for _, pending := range group {
				pending.timer.Stop()
			}
		}
		e.pending = make(map[string]map[string]*pendingFeedback)
		e.mu.Unlock()
		e.cancel()
		e.wg.Wait()
	})
}

func (e *FeedbackEngine) Stats() FeedbackStats {
	if e == nil {
		return FeedbackStats{}
	}
	return FeedbackStats{
		Registered: e.registered.Load(), Dropped: e.dropped.Load(),
		Succeeded: e.succeeded.Load(), Failed: e.failed.Load(),
	}
}

func MemoryEntryIDs(context memory.SocialMemoryContext) []string {
	ids := make([]string, 0, len(context.Entries))
	for _, entry := range context.Entries {
		ids = append(ids, entry.ID)
	}
	return ids
}
