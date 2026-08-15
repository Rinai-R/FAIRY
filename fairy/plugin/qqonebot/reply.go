package qqonebot

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

type ReplyDistanceTracker struct {
	sequence uint64
	entries  []replyPosition
	byID     map[string]replyPosition
}

func (tracker *ReplyDistanceTracker) Observe(messageID string, receivedAt time.Time) {
	if tracker == nil || !ValidReplyMessageID(messageID) || receivedAt.IsZero() {
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

func (tracker *ReplyDistanceTracker) ShouldQuote(messageID string, now time.Time, messageGap uint64) bool {
	if tracker == nil || !ValidReplyMessageID(messageID) || now.IsZero() {
		return false
	}
	position, exists := tracker.byID[messageID]
	if !exists || tracker.sequence < position.sequence || now.Before(position.receivedAt) {
		return false
	}
	messageDistanceReached := messageGap >= replyMessageGapMin && messageGap <= replyMessageGapMax && tracker.sequence-position.sequence >= messageGap
	return messageDistanceReached || now.Sub(position.receivedAt) >= replyElapsedThreshold
}

func RandomReplyMessageGap() uint64 {
	return replyMessageGapMin + uint64(rand.IntN(int(replyMessageGapMax-replyMessageGapMin+1)))
}

func ValidReplyMessageID(value string) bool {
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

type TurnReplyClaims struct {
	claimed          map[string]uint64
	sampleMessageGap func() uint64
}

func NewTurnReplyClaims() *TurnReplyClaims {
	return NewTurnReplyClaimsWithSampler(RandomReplyMessageGap)
}

func NewTurnReplyClaimsWithSampler(sampleMessageGap func() uint64) *TurnReplyClaims {
	return &TurnReplyClaims{claimed: make(map[string]uint64), sampleMessageGap: sampleMessageGap}
}

func (claims *TurnReplyClaims) Claim(turnID string) (uint64, bool) {
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

func (claims *TurnReplyClaims) Release(turnID string) {
	if claims != nil {
		delete(claims.claimed, turnID)
	}
}

func TerminalTurnState(state string) bool {
	switch state {
	case "completed", "failed", "interrupted":
		return true
	default:
		return false
	}
}
