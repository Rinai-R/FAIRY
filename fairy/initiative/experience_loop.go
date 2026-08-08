package initiative

import "fairy/model"

// ExperienceStats is a low-sensitivity projection of the two bounded public
// learning horizons. It intentionally contains no text, evidence IDs, stable
// hashes or provider cache keys.
type ExperienceStats struct {
	Learning             LearningStats `json:"learning"`
	Feedback             FeedbackStats `json:"feedback"`
	CacheIdentityVersion string        `json:"cacheIdentityVersion"`
}

// ExperienceLoop owns the public social learning horizons: episode learning
// from bounded observations and immediate outcome feedback for completed
// replies. Persistence remains owned by the injected domain hosts.
type ExperienceLoop struct {
	learning *LearningEngine
	feedback *FeedbackEngine
}

func NewExperienceLoop(learning LearningHost, feedback FeedbackHost) *ExperienceLoop {
	return newExperienceLoop(
		NewLearningEngine(learning, LearningQueueCapacity),
		NewFeedbackEngine(feedback, FeedbackQueueCapacity),
	)
}

func newExperienceLoop(learning *LearningEngine, feedback *FeedbackEngine) *ExperienceLoop {
	return &ExperienceLoop{learning: learning, feedback: feedback}
}

func (e *ExperienceLoop) CompleteReply(registration FeedbackRegistration) bool {
	return e != nil && e.feedback != nil && e.feedback.Register(registration)
}

func (e *ExperienceLoop) Observe(conversationID string, observation AmbientObservation) {
	if e != nil && e.feedback != nil {
		e.feedback.Observe(conversationID, observation)
	}
}

func (e *ExperienceLoop) EnqueueEpisode(conversationID string, messages []AmbientObservation) bool {
	if e == nil || e.learning == nil {
		return false
	}
	return e.learning.Enqueue(LearningSnapshot{ConversationID: conversationID, Messages: messages})
}

func (e *ExperienceLoop) Stats() ExperienceStats {
	stats := ExperienceStats{CacheIdentityVersion: model.PromptCacheKeyVersion}
	if e == nil {
		return stats
	}
	if e.learning != nil {
		stats.Learning = e.learning.Stats()
	}
	if e.feedback != nil {
		stats.Feedback = e.feedback.Stats()
	}
	return stats
}

func (e *ExperienceLoop) Close() {
	if e == nil {
		return
	}
	if e.learning != nil {
		e.learning.Close()
	}
	if e.feedback != nil {
		e.feedback.Close()
	}
}
