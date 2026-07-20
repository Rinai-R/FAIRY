package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type ConversationRecord struct {
	ID              string `json:"id"`
	CharacterID     string `json:"characterId"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
	UpdatedAtUnixMS int64  `json:"updatedAtUnixMs"`
}

type MessageRecord struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	TurnID          string `json:"turnId"`
	Sequence        uint64 `json:"sequence"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type PromptWindowRecord struct {
	ConversationID        string  `json:"conversationId"`
	Revision              uint64  `json:"revision"`
	Summary               *string `json:"summary"`
	CutoffMessageSequence uint64  `json:"cutoffMessageSequence"`
	UpdatedAtUnixMS       int64   `json:"updatedAtUnixMs"`
}

type ConversationBootstrap struct {
	Conversation ConversationRecord `json:"conversation"`
	Messages     []MessageRecord    `json:"messages"`
	PromptWindow PromptWindowRecord `json:"promptWindow"`
}

type PersistedTurn struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversationId"`
	UserMessage    MessageRecord `json:"userMessage"`
}

func (s *Store) OpenOrCreateCharacterConversation(characterID string) (ConversationBootstrap, error) {
	return s.OpenOrCreateCharacterConversationContext(context.Background(), characterID)
}

func (s *Store) OpenOrCreateCharacterConversationContext(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	return s.openOrCreateCharacterConversationPostgres(ctx, characterID)
}

func (s *Store) LoadConversation(conversationID string) (ConversationBootstrap, error) {
	return s.LoadConversationContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationContext(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	return s.loadConversationPostgres(ctx, conversationID)
}

func (s *Store) BeginTurn(conversationID string, userMessage string) (PersistedTurn, error) {
	return s.BeginTurnContext(context.Background(), conversationID, userMessage)
}

func (s *Store) BeginTurnContext(ctx context.Context, conversationID string, userMessage string) (PersistedTurn, error) {
	return s.beginTurnPostgres(ctx, conversationID, userMessage)
}

func (s *Store) CompleteTurn(conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	return s.CompleteTurnContext(context.Background(), conversationID, turnID, assistantMessage)
}

func (s *Store) CompleteTurnContext(ctx context.Context, conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	return s.completeTurnPostgres(ctx, conversationID, turnID, assistantMessage)
}

func (s *Store) InterruptTurn(conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	return s.InterruptTurnContext(context.Background(), conversationID, turnID, publishedPrefix)
}

func (s *Store) InterruptTurnContext(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	return s.interruptTurnPostgres(ctx, conversationID, turnID, publishedPrefix)
}

func (s *Store) FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error {
	return s.FailTurnContext(context.Background(), conversationID, turnID, code, message, retryable)
}

func (s *Store) FailTurnContext(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error {
	return s.failTurnPostgres(ctx, conversationID, turnID, code, message, retryable)
}

func validateID(label string, value string) error {
	if value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func validateContent(label string, value string) error {
	if value == "" || strings.TrimSpace(value) == "" || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func nowUnixMS() int64 {
	return time.Now().UnixMilli()
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(data[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
