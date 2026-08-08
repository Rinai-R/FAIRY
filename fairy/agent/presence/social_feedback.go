package presence

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

	"fairy/context/social"
	"fairy/runtime/model"
)

const (
	SocialFeedbackMaxOutputTokens   uint32 = 768
	FeedbackQueueCapacity                  = 8
	socialFeedbackPendingCapacity          = 256
	socialFeedbackObservationLimit         = 6
	socialFeedbackObservationWindow        = 2 * time.Minute
)

const (
	SocialFeedbackEvaluatorRevision = "social-feedback-v2"
	SocialFeedbackInstructions      = "Evaluate each supplied social-memory candidate for one completed public-group reply. Output exactly one strict JSON object: {\"evaluations\":[{\"entryId\":\"s0\",\"adoption\":\"adopted|not_adopted|uncertain\",\"outcome\":\"positive|partial|negative|unknown\",\"credit\":\"entry|execution|context|unknown\",\"evidenceMessageIds\":[\"message-id\"]}]}. Cover every supplied alias exactly once and use no other alias. adoption says whether the reply used that candidate. A known outcome requires later external message evidence from the supplied window. credit says whether the outcome came from the candidate, reply execution, or context. not_adopted and uncertain must use unknown outcome, unknown credit, and no evidence. Silence or insufficient evidence is unknown. Do not output real entry IDs, scores, reasons, Markdown, unknown fields, null fields, or trailing data."
)

type FeedbackRegistration struct {
	CharacterID    string
	ConversationID string
	TurnID         string
	Candidates     []social.SocialFeedbackCandidate
	ReplyText      string
}

type pendingFeedback struct {
	registration FeedbackRegistration
	observations []AmbientObservation
	timer        *time.Timer
}

type feedbackSnapshot struct {
	registration FeedbackRegistration
	observations []AmbientObservation
}

type FeedbackStats struct {
	Registered                int64 `json:"registered"`
	Superseded                int64 `json:"superseded"`
	Dropped                   int64 `json:"dropped"`
	Succeeded                 int64 `json:"succeeded"`
	Failed                    int64 `json:"failed"`
	ModelCalls                int64 `json:"modelCalls"`
	InputTokens               int64 `json:"inputTokens"`
	CachedObservedInputTokens int64 `json:"cachedObservedInputTokens"`
	CachedInputTokens         int64 `json:"cachedInputTokens"`
	CacheWriteTokens          int64 `json:"cacheWriteTokens"`
	OutputTokens              int64 `json:"outputTokens"`
}

type FeedbackEngine struct {
	host   FeedbackHost
	ctx    context.Context
	cancel context.CancelFunc
	queue  *boundedQueue[feedbackSnapshot]
	wg     sync.WaitGroup
	once   sync.Once

	mu                        sync.Mutex
	closed                    bool
	pending                   map[string]map[string]*pendingFeedback
	pendingCapacity           int
	pendingCount              int
	window                    time.Duration
	timerCallbackHook         func()
	registered                atomic.Int64
	superseded                atomic.Int64
	dropped                   atomic.Int64
	succeeded                 atomic.Int64
	failed                    atomic.Int64
	modelCalls                atomic.Int64
	inputTokens               atomic.Int64
	cachedObservedInputTokens atomic.Int64
	cachedInputTokens         atomic.Int64
	cacheWriteTokens          atomic.Int64
	outputTokens              atomic.Int64
}

type feedbackPromptPayload struct {
	ContextType  string                          `json:"contextType"`
	Candidates   []feedbackCandidatePayload      `json:"candidates"`
	Reply        string                          `json:"reply"`
	Observations []socialLearnObservationPayload `json:"observations"`
}

type feedbackCandidatePayload struct {
	EntryID   string `json:"entryId"`
	Kind      string `json:"kind"`
	Situation string `json:"situation"`
	Content   string `json:"content"`
	RecallCue string `json:"recallCue"`
}

type feedbackResult struct {
	Evaluations []feedbackEvaluationResult `json:"evaluations"`
}

type feedbackEvaluationResult struct {
	EntryID            string   `json:"entryId"`
	Adoption           string   `json:"adoption"`
	Outcome            string   `json:"outcome"`
	Credit             string   `json:"credit"`
	EvidenceMessageIDs []string `json:"evidenceMessageIds"`
}

func NewFeedbackEngine(host FeedbackHost, capacity int) *FeedbackEngine {
	return newFeedbackEngine(host, capacity, socialFeedbackPendingCapacity, socialFeedbackObservationWindow)
}

func newFeedbackEngine(host FeedbackHost, queueCapacity, pendingCapacity int, window time.Duration) *FeedbackEngine {
	if queueCapacity < 1 {
		queueCapacity = 1
	}
	if pendingCapacity < 1 {
		pendingCapacity = 1
	}
	if window <= 0 {
		window = socialFeedbackObservationWindow
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := &FeedbackEngine{
		host: host, ctx: ctx, cancel: cancel,
		queue:           newBoundedQueue[feedbackSnapshot](queueCapacity),
		pending:         make(map[string]map[string]*pendingFeedback),
		pendingCapacity: pendingCapacity, window: window,
	}
	engine.wg.Add(1)
	go engine.run()
	return engine
}

func (e *FeedbackEngine) Register(registration FeedbackRegistration) bool {
	if e == nil || strings.TrimSpace(registration.CharacterID) == "" || strings.TrimSpace(registration.ConversationID) == "" || strings.TrimSpace(registration.TurnID) == "" {
		return false
	}
	if strings.TrimSpace(registration.ReplyText) == "" || len(registration.Candidates) == 0 || len(registration.Candidates) > social.MaxSocialFeedbackIDs {
		return false
	}
	seenCandidates := make(map[string]struct{}, len(registration.Candidates))
	registration.Candidates = append([]social.SocialFeedbackCandidate(nil), registration.Candidates...)
	for _, candidate := range registration.Candidates {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Content) == "" {
			return false
		}
		if _, exists := seenCandidates[candidate.ID]; exists {
			return false
		}
		seenCandidates[candidate.ID] = struct{}{}
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return false
	}
	group := e.pending[registration.ConversationID]
	if group != nil {
		if _, exists := group[registration.TurnID]; exists {
			e.dropped.Add(1)
			e.mu.Unlock()
			return false
		}
	}
	if e.pendingCount-len(group) >= e.pendingCapacity {
		e.dropped.Add(1)
		e.mu.Unlock()
		return false
	}
	superseded := make([]feedbackSnapshot, 0, len(group))
	for turnID, previous := range group {
		e.stopPendingTimer(previous)
		delete(group, turnID)
		e.pendingCount--
		superseded = append(superseded, snapshotSocialFeedback(previous))
	}
	group = make(map[string]*pendingFeedback, 1)
	e.pending[registration.ConversationID] = group
	pending := &pendingFeedback{registration: registration}
	e.wg.Add(1)
	pending.timer = time.AfterFunc(e.window, func() {
		defer e.wg.Done()
		if e.timerCallbackHook != nil {
			e.timerCallbackHook()
		}
		e.finalize(registration.ConversationID, registration.TurnID)
	})
	group[registration.TurnID] = pending
	e.pendingCount++
	e.registered.Add(1)
	e.superseded.Add(int64(len(superseded)))
	e.mu.Unlock()
	for _, snapshot := range superseded {
		e.enqueue(snapshot)
	}
	return true
}

func (e *FeedbackEngine) Observe(conversationID string, observation AmbientObservation) {
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
		e.stopPendingTimer(pending)
		delete(group, turnID)
		e.pendingCount--
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
		e.pendingCount--
		if len(group) == 0 {
			delete(e.pending, conversationID)
		}
	}
	e.mu.Unlock()
	if pending != nil {
		e.enqueue(snapshotSocialFeedback(pending))
	}
}

func (e *FeedbackEngine) stopPendingTimer(pending *pendingFeedback) {
	if pending != nil && pending.timer != nil && pending.timer.Stop() {
		e.wg.Done()
	}
}

func snapshotSocialFeedback(pending *pendingFeedback) feedbackSnapshot {
	return feedbackSnapshot{
		registration: pending.registration,
		observations: append([]AmbientObservation(nil), pending.observations...),
	}
}

func (e *FeedbackEngine) enqueue(snapshot feedbackSnapshot) {
	select {
	case <-e.ctx.Done():
		return
	default:
	}
	if !e.queue.tryPush(snapshot) {
		e.dropped.Add(1)
	}
}

func (e *FeedbackEngine) run() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case snapshot := <-e.queue.receive():
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
	evaluations := unknownSocialFeedbackEvaluations(snapshot.registration.Candidates)
	if len(snapshot.observations) > 0 {
		resolved, err := e.host.ResolveInteraction(snapshot.registration.ConversationID)
		if err != nil {
			return fmt.Errorf("resolving social feedback interaction: %w", err)
		}
		record, err := e.host.ActiveCharacter(snapshot.registration.CharacterID)
		if err != nil {
			return err
		}
		stablePrefix, err := buildSocialStablePrefix(record, resolved)
		if err != nil {
			return err
		}
		dynamicInput, err := buildSocialFeedbackInput(snapshot)
		if err != nil {
			return err
		}
		input := make([]model.PromptItem, 0, len(stablePrefix)+len(dynamicInput))
		input = append(input, stablePrefix...)
		input = append(input, dynamicInput...)
		connection, err := e.host.ModelConnection()
		if err != nil {
			return err
		}
		cacheKey := ""
		if connection.Capabilities.PromptCacheKey {
			cacheKey = model.LaneCacheKey(snapshot.registration.ConversationID, model.PromptLaneSocialFeedback)
		}
		cacheInput, err := model.NewCacheKeyInputWithStablePrefix(
			model.PromptLaneSocialFeedback, connection.Model, snapshot.registration.ConversationID,
			SocialFeedbackInstructions, stablePrefix,
		)
		if err != nil {
			return fmt.Errorf("building social feedback cache identity: %w", err)
		}
		cacheInput.CharacterRevision = record.Revision
		events, err := e.host.ExecuteRequest(ctx, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{Lane: model.PromptLaneSocialFeedback, Model: connection.Model, Instructions: SocialFeedbackInstructions, MaxOutputTokens: SocialFeedbackMaxOutputTokens, PromptCacheKey: cacheKey},
			Input: input, CacheInput: &cacheInput,
		})
		if err != nil {
			return fmt.Errorf("executing social feedback request: %w", err)
		}
		e.observeModelUsage(events)
		evaluations, err = compileSocialFeedback(model.CollectTextFromEvents(events), snapshot.registration.Candidates, snapshot.observations)
		if err != nil {
			return err
		}
	}
	_, err := e.host.RecordSocialFeedbackBatch(ctx, social.SocialFeedbackBatchInput{
		CharacterID: snapshot.registration.CharacterID, ConversationID: snapshot.registration.ConversationID,
		TurnID: snapshot.registration.TurnID, Evaluations: evaluations,
		ObservedMessageCount: len(snapshot.observations), EvaluatorRevision: SocialFeedbackEvaluatorRevision,
	})
	return err
}

func (e *FeedbackEngine) observeModelUsage(events []model.StreamEvent) {
	e.modelCalls.Add(1)
	for _, lane := range model.LaneUsageFromEvents(model.PromptLaneSocialFeedback, events, 0) {
		if lane.Usage.InputTokens != nil {
			e.inputTokens.Add(int64(*lane.Usage.InputTokens))
			if lane.Usage.CachedInputTokens.Status == "observed" {
				e.cachedObservedInputTokens.Add(int64(*lane.Usage.InputTokens))
			}
		}
		if lane.Usage.CachedInputTokens.Status == "observed" && lane.Usage.CachedInputTokens.Tokens != nil {
			e.cachedInputTokens.Add(int64(*lane.Usage.CachedInputTokens.Tokens))
		}
		if lane.Usage.CacheWriteTokens.Status == "observed" && lane.Usage.CacheWriteTokens.Tokens != nil {
			e.cacheWriteTokens.Add(int64(*lane.Usage.CacheWriteTokens.Tokens))
		}
		if lane.Usage.OutputTokens != nil {
			e.outputTokens.Add(int64(*lane.Usage.OutputTokens))
		}
	}
}

func buildSocialFeedbackInput(snapshot feedbackSnapshot) ([]model.PromptItem, error) {
	candidates := make([]feedbackCandidatePayload, 0, len(snapshot.registration.Candidates))
	for index, candidate := range snapshot.registration.Candidates {
		candidates = append(candidates, feedbackCandidatePayload{
			EntryID: fmt.Sprintf("s%d", index), Kind: candidate.Kind, Situation: candidate.Situation,
			Content: candidate.Content, RecallCue: candidate.RecallCue,
		})
	}
	observations := make([]socialLearnObservationPayload, 0, len(snapshot.observations))
	for _, observation := range snapshot.observations {
		observations = append(observations, socialLearnObservationPayload{
			ContextType: "later_external_group_observation", MessageID: observation.MessageID,
			SenderID: observation.SenderID, SenderName: observation.SenderName, Text: observation.Text,
			TimestampUnixMS: observation.TimestampUnixMS,
		})
	}
	payload, err := json.Marshal(feedbackPromptPayload{
		ContextType: "public_reply_outcome_evidence", Candidates: candidates,
		Reply: snapshot.registration.ReplyText, Observations: observations,
	})
	if err != nil {
		return nil, fmt.Errorf("serializing social feedback input: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func compileSocialFeedback(
	draft string,
	candidates []social.SocialFeedbackCandidate,
	observations []AmbientObservation,
) ([]social.SocialFeedbackEvaluation, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(draft)))
	decoder.DisallowUnknownFields()
	var result feedbackResult
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding social feedback result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("social feedback result contains trailing data")
	}
	if len(result.Evaluations) != len(candidates) {
		return nil, errors.New("social feedback result does not cover every candidate")
	}
	aliasToID := make(map[string]string, len(candidates))
	for index, candidate := range candidates {
		aliasToID[fmt.Sprintf("s%d", index)] = candidate.ID
	}
	allowedEvidence := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		allowedEvidence[observation.MessageID] = struct{}{}
	}
	seenAliases := make(map[string]struct{}, len(candidates))
	evaluations := make([]social.SocialFeedbackEvaluation, 0, len(candidates))
	for _, raw := range result.Evaluations {
		entryID, exists := aliasToID[raw.EntryID]
		if !exists {
			return nil, errors.New("social feedback result contains an unknown candidate alias")
		}
		if _, exists := seenAliases[raw.EntryID]; exists {
			return nil, errors.New("social feedback result contains a duplicate candidate alias")
		}
		seenAliases[raw.EntryID] = struct{}{}
		for _, evidenceID := range raw.EvidenceMessageIDs {
			if _, exists := allowedEvidence[evidenceID]; !exists {
				return nil, errors.New("social feedback result cites evidence outside the current window")
			}
		}
		evaluations = append(evaluations, social.SocialFeedbackEvaluation{
			EntryID: entryID, Adoption: raw.Adoption, Outcome: raw.Outcome, Credit: raw.Credit,
			EvidenceMessageIDs: append([]string{}, raw.EvidenceMessageIDs...),
		})
	}
	if err := social.ValidateSocialFeedbackBatch(social.SocialFeedbackBatchInput{
		CharacterID: "validation-character", ConversationID: "validation-conversation", TurnID: "validation-turn",
		Evaluations: evaluations, ObservedMessageCount: len(observations), EvaluatorRevision: SocialFeedbackEvaluatorRevision,
	}); err != nil {
		return nil, fmt.Errorf("validating social feedback result: %w", err)
	}
	return evaluations, nil
}

func unknownSocialFeedbackEvaluations(candidates []social.SocialFeedbackCandidate) []social.SocialFeedbackEvaluation {
	evaluations := make([]social.SocialFeedbackEvaluation, 0, len(candidates))
	for _, candidate := range candidates {
		evaluations = append(evaluations, social.SocialFeedbackEvaluation{
			EntryID: candidate.ID, Adoption: social.SocialFeedbackUncertain,
			Outcome: social.SocialFeedbackUnknown, Credit: social.SocialFeedbackCreditUnknown,
			EvidenceMessageIDs: []string{},
		})
	}
	return evaluations
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
				e.stopPendingTimer(pending)
			}
		}
		e.pending = make(map[string]map[string]*pendingFeedback)
		e.pendingCount = 0
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
		Registered: e.registered.Load(), Superseded: e.superseded.Load(), Dropped: e.dropped.Load(),
		Succeeded: e.succeeded.Load(), Failed: e.failed.Load(),
		ModelCalls: e.modelCalls.Load(), InputTokens: e.inputTokens.Load(),
		CachedObservedInputTokens: e.cachedObservedInputTokens.Load(),
		CachedInputTokens:         e.cachedInputTokens.Load(), CacheWriteTokens: e.cacheWriteTokens.Load(),
		OutputTokens: e.outputTokens.Load(),
	}
}
