package main

import (
	"math/rand/v2"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	replyPositionLimit    = 64
	replyMessageGapMin    = uint64(2)
	replyMessageGapMax    = uint64(5)
	replyElapsedThreshold = 15 * time.Minute
)

type replyPosition struct {
	messageID  string
	sequence   uint64
	receivedAt time.Time
}

type replyDistanceTracker struct {
	sequence uint64
	entries  []replyPosition
	byID     map[string]replyPosition
}

func (tracker *replyDistanceTracker) Observe(messageID string, receivedAt time.Time) {
	if tracker == nil || !validReplyMessageID(messageID) || receivedAt.IsZero() {
		return
	}
	if tracker.byID == nil {
		tracker.byID = make(map[string]replyPosition, replyPositionLimit)
	}
	if _, exists := tracker.byID[messageID]; exists {
		return
	}
	tracker.sequence++
	position := replyPosition{messageID: messageID, sequence: tracker.sequence, receivedAt: receivedAt}
	tracker.entries = append(tracker.entries, position)
	tracker.byID[messageID] = position
	if len(tracker.entries) <= replyPositionLimit {
		return
	}
	evicted := tracker.entries[0]
	tracker.entries = tracker.entries[1:]
	delete(tracker.byID, evicted.messageID)
}

func (tracker *replyDistanceTracker) ShouldQuote(messageID string, now time.Time, messageGap uint64) bool {
	if tracker == nil || !validReplyMessageID(messageID) || now.IsZero() {
		return false
	}
	position, exists := tracker.byID[messageID]
	if !exists || tracker.sequence < position.sequence || now.Before(position.receivedAt) {
		return false
	}
	messageDistanceReached := messageGap >= replyMessageGapMin && messageGap <= replyMessageGapMax && tracker.sequence-position.sequence >= messageGap
	return messageDistanceReached || now.Sub(position.receivedAt) >= replyElapsedThreshold
}

func randomReplyMessageGap() uint64 {
	return replyMessageGapMin + uint64(rand.IntN(int(replyMessageGapMax-replyMessageGapMin+1)))
}

func validReplyMessageID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

type turnReplyClaims struct {
	claimed          map[string]uint64
	sampleMessageGap func() uint64
}

func newTurnReplyClaims() *turnReplyClaims {
	return newTurnReplyClaimsWithSampler(randomReplyMessageGap)
}

func newTurnReplyClaimsWithSampler(sampleMessageGap func() uint64) *turnReplyClaims {
	return &turnReplyClaims{claimed: make(map[string]uint64), sampleMessageGap: sampleMessageGap}
}

func (claims *turnReplyClaims) Claim(turnID string) (uint64, bool) {
	if claims == nil || strings.TrimSpace(turnID) == "" {
		return 0, false
	}
	if _, exists := claims.claimed[turnID]; exists {
		return 0, false
	}
	messageGap := claims.sampleMessageGap()
	claims.claimed[turnID] = messageGap
	return messageGap, true
}

func (claims *turnReplyClaims) Release(turnID string) {
	if claims != nil {
		delete(claims.claimed, turnID)
	}
}

func terminalTurnState(state string) bool {
	switch state {
	case "completed", "failed", "interrupted":
		return true
	default:
		return false
	}
}

func (b *bot) observeReplyPosition(conversationID, messageID string, receivedAt time.Time) {
	if b == nil || strings.TrimSpace(conversationID) == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.replyPositions == nil {
		b.replyPositions = make(map[string]*replyDistanceTracker)
	}
	tracker := b.replyPositions[conversationID]
	if tracker == nil {
		tracker = &replyDistanceTracker{}
		b.replyPositions[conversationID] = tracker
	}
	tracker.Observe(messageID, receivedAt)
}

func (b *bot) shouldQuoteReply(conversationID, messageID string, now time.Time, messageGap uint64) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.replyPositions[conversationID].ShouldQuote(messageID, now, messageGap)
}
