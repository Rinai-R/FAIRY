package api

import (
	"errors"
	"time"

	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/coredb"
	"fairy/desktopcapture"
	"fairy/initiative"
	"fairy/memory"
	"fairy/observability"
	"fairy/session"
	"fairy/speech"
	"fairy/sticker"

	"go.uber.org/zap"
)

var (
	ErrEventSubscriberOverflow         = errors.New("event subscriber overflow")
	ErrParticipationSubscriberOverflow = errors.New("participation subscriber overflow")
)

type EventSubscription struct {
	Events   <-chan session.Event
	Failures <-chan error
	Cancel   func()
}

func (s EventSubscription) Unsubscribe() {
	if s.Cancel != nil {
		s.Cancel()
	}
}

type ParticipationSubscription struct {
	Events   <-chan initiative.Event
	Failures <-chan error
	Cancel   func()
}

func (s ParticipationSubscription) Unsubscribe() {
	if s.Cancel != nil {
		s.Cancel()
	}
}

// Dependencies is API's consumption-side view of the Core composition root.
// API owns no construction or shutdown of these process-scoped services.
type Dependencies struct {
	ConfigRoot string
	Logger     *zap.Logger
	StartedAt  time.Time

	Database    *coredb.Pool
	VectorIndex *memory.VectorClient
	MemoryStore *memory.Store
	Identity    *memory.IdentityStore
	Memory      *memory.MemoryService
	Secret      *config.SecretStore
	Companion   *companion.CompanionService
	Initiative  *initiative.Service
	Character   *character.CharacterService
	Config      *config.ConfigService
	Speech      *speech.SpeechService
	Profile     *config.ProfileService
	Stickers    *sticker.Store
	Captures    *desktopcapture.CaptureHub
	Logs        *observability.LogStore
	HTTPMetrics *observability.HTTPMetrics
	Messages    *observability.MessageMetrics

	BootstrapStatus          func() (any, error)
	SubscribeTurnEvents      func(conversationID string) EventSubscription
	SubscribeParticipation   func(conversationID string) ParticipationSubscription
	TurnEventSubscriberCount func() uint64
}
