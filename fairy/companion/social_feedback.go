package companion

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

	"go.uber.org/zap"
)

const (
	SocialFeedbackMaxOutputTokens    uint32 = 96
	socialFeedbackQueueCapacity             = 8
	socialFeedbackMaxPendingPerGroup        = 4
	socialFeedbackObservationLimit          = 6
	socialFeedbackObservationWindow         = 2 * time.Minute
)

const SocialFeedbackInstructions = "Judge the observable social outcome of one public-group reply from only the supplied reply and later external observations. Output exactly one strict JSON object: {\"outcome\":\"positive|negative|unknown\"}. positive means the conversation visibly continues constructively, especially when another participant engages with the reply or the topic progresses. negative means the reply is explicitly corrected, rejected, makes the exchange worse, or triggers a visible repair loop. unknown means evidence is absent, ambiguous, unrelated, or only silence. Do not infer private feelings or hidden reactions. Output no reasoning, Markdown, unknown fields, null fields, or trailing data."

type socialFeedbackRegistration struct {
	CharacterID    string
	ConversationID string
	TurnID         string
	EntryIDs       []string
	ReplyText      string
}

type pendingSocialFeedback struct {
	registration socialFeedbackRegistration
	observations []AmbientObservation
	timer        *time.Timer
}

type socialFeedbackSnapshot struct {
	registration socialFeedbackRegistration
	observations []AmbientObservation
}

type SocialFeedbackStats struct {
	Registered int64
	Dropped    int64
	Succeeded  int64
	Failed     int64
}

type SocialFeedbackEngine struct {
	host   *CompanionService
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan socialFeedbackSnapshot
	wg     sync.WaitGroup
	once   sync.Once

	mu      sync.Mutex
	closed  bool
	pending map[string]map[string]*pendingSocialFeedback
	registered atomic.Int64
	dropped    atomic.Int64
	succeeded  atomic.Int64
	failed     atomic.Int64
}

type socialFeedbackPromptPayload struct {
	ContextType  string                          `json:"contextType"`
	Reply        string                          `json:"reply"`
	Observations []socialLearnObservationPayload `json:"observations"`
}

type socialFeedbackResult struct {
	Outcome string `json:"outcome"`
}

func newSocialFeedbackEngine(host *CompanionService, capacity int) *SocialFeedbackEngine {
	if capacity < 1 {
		capacity = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := &SocialFeedbackEngine{
		host: host, ctx: ctx, cancel: cancel, queue: make(chan socialFeedbackSnapshot, capacity),
		pending: make(map[string]map[string]*pendingSocialFeedback),
	}
	engine.wg.Add(1)
	go engine.run()
	return engine
}

func (e *SocialFeedbackEngine) Register(registration socialFeedbackRegistration) bool {
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
		group = make(map[string]*pendingSocialFeedback)
		e.pending[registration.ConversationID] = group
	}
	if len(group) >= socialFeedbackMaxPendingPerGroup {
		e.dropped.Add(1)
		return false
	}
	pending := &pendingSocialFeedback{registration: registration}
	pending.timer = time.AfterFunc(socialFeedbackObservationWindow, func() {
		e.finalize(registration.ConversationID, registration.TurnID)
	})
	group[registration.TurnID] = pending
	e.registered.Add(1)
	return true
}

func (e *SocialFeedbackEngine) Observe(conversationID string, observation AmbientObservation) {
	if e == nil {
		return
	}
	e.mu.Lock()
	group := e.pending[conversationID]
	ready := make([]socialFeedbackSnapshot, 0)
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

func (e *SocialFeedbackEngine) finalize(conversationID, turnID string) {
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

func snapshotSocialFeedback(pending *pendingSocialFeedback) socialFeedbackSnapshot {
	return socialFeedbackSnapshot{
		registration: pending.registration,
		observations: append([]AmbientObservation(nil), pending.observations...),
	}
}

func (e *SocialFeedbackEngine) enqueue(snapshot socialFeedbackSnapshot) {
	select {
	case <-e.ctx.Done():
	case e.queue <- snapshot:
	default:
		e.dropped.Add(1)
	}
}

func (e *SocialFeedbackEngine) run() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case snapshot := <-e.queue:
			if err := e.process(e.ctx, snapshot); err != nil {
				e.failed.Add(1)
				if e.host != nil && e.host.logger != nil {
					e.host.logger.Warn("social feedback failed", zap.String("conversationId", snapshot.registration.ConversationID), zap.String("turnId", snapshot.registration.TurnID), zap.Error(err))
				}
				continue
			}
			e.succeeded.Add(1)
		}
	}
}

func (e *SocialFeedbackEngine) process(ctx context.Context, snapshot socialFeedbackSnapshot) error {
	if e.host == nil || e.host.memoryPort() == nil {
		return errors.New("social feedback runtime is not configured")
	}
	outcome := memory.SocialFeedbackUnknown
	if len(snapshot.observations) > 0 {
		if e.host.modelPort() == nil || e.host.configSource() == nil {
			return errors.New("social feedback model runtime is not configured")
		}
		input, err := buildSocialFeedbackInput(snapshot)
		if err != nil {
			return err
		}
		connection, err := e.host.configSource().ModelConnection()
		if err != nil {
			return err
		}
		cacheKey := ""
		if connection.Capabilities.PromptCacheKey {
			cacheKey = model.LaneCacheKey(snapshot.registration.ConversationID, model.PromptLaneSocialFeedback)
		}
		events, err := e.host.modelPort().ExecuteRequestContext(ctx, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{Lane: model.PromptLaneSocialFeedback, Model: connection.Model, Instructions: SocialFeedbackInstructions, MaxOutputTokens: SocialFeedbackMaxOutputTokens, PromptCacheKey: cacheKey},
			Input: input,
		})
		if err != nil {
			return fmt.Errorf("executing social feedback request: %w", err)
		}
		outcome, err = compileSocialFeedback(model.CollectTextFromEvents(events))
		if err != nil {
			return err
		}
	}
	_, err := e.host.memoryPort().RecordSocialReplyFeedback(ctx, memory.SocialReplyFeedbackInput{
		CharacterID: snapshot.registration.CharacterID, ConversationID: snapshot.registration.ConversationID,
		TurnID: snapshot.registration.TurnID, EntryIDs: snapshot.registration.EntryIDs,
		Outcome: outcome, ObservedMessageCount: len(snapshot.observations),
	})
	return err
}

func buildSocialFeedbackInput(snapshot socialFeedbackSnapshot) ([]model.PromptItem, error) {
	observations := make([]socialLearnObservationPayload, 0, len(snapshot.observations))
	for _, observation := range snapshot.observations {
		observations = append(observations, socialLearnObservationPayload{
			ContextType: "later_external_group_observation", MessageID: observation.MessageID,
			SenderID: observation.SenderID, SenderName: observation.SenderName, Text: observation.Text,
			TimestampUnixMS: observation.TimestampUnixMS,
		})
	}
	payload, err := json.Marshal(socialFeedbackPromptPayload{ContextType: "public_reply_outcome_evidence", Reply: snapshot.registration.ReplyText, Observations: observations})
	if err != nil {
		return nil, fmt.Errorf("serializing social feedback input: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func compileSocialFeedback(draft string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(draft)))
	decoder.DisallowUnknownFields()
	var result socialFeedbackResult
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

func (e *SocialFeedbackEngine) Close() {
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
		e.pending = make(map[string]map[string]*pendingSocialFeedback)
		e.mu.Unlock()
		e.cancel()
		e.wg.Wait()
	})
}

func (e *SocialFeedbackEngine) Stats() SocialFeedbackStats {
	if e == nil {
		return SocialFeedbackStats{}
	}
	return SocialFeedbackStats{
		Registered: e.registered.Load(), Dropped: e.dropped.Load(),
		Succeeded: e.succeeded.Load(), Failed: e.failed.Load(),
	}
}

func socialMemoryEntryIDs(context memory.SocialMemoryContext) []string {
	ids := make([]string, 0, len(context.Entries))
	for _, entry := range context.Entries {
		ids = append(ids, entry.ID)
	}
	return ids
}
