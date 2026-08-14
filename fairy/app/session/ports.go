package appsession

import (
	"context"
	"errors"

	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/transport/session"
)

var (
	ErrEventSubscriberOverflow         = errors.New("event subscriber overflow")
	ErrParticipationSubscriberOverflow = errors.New("participation subscriber overflow")
	ErrEventSubscriberCapacity         = errors.New("event subscriber capacity reached")
	ErrParticipationSubscriberCapacity = errors.New("participation subscriber capacity reached")
	ErrWatchCapacity                   = errors.New("session watch capacity exhausted")
	ErrAssociationCapacity             = errors.New("session association capacity exhausted")
	ErrTurnCapacity                    = errors.New("session turn submission capacity exhausted")
	ErrConnectionClosed                = errors.New("session connection is closed")
	ErrDeliveryUnavailable             = errors.New("delivery report is unavailable for this session")
	ErrSessionUnavailable              = errors.New("session service is unavailable")
)

const (
	ConnectionWatchCapacity       = 64
	ConnectionAssociationCapacity = 64
	ConnectionTurnCapacity        = 16
)

type TurnSubmission struct {
	ConversationID string
	Input          string
	MessageID      string
}

type TurnRuntime interface {
	OutputCapabilities(string) session.OutputCapabilities
	ReportExpressionDelivery(session.ExpressionDeliveryResult) error
	BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error
	UnbindOutputCapabilities(ownerID, conversationID string)
	SubmitTurn(TurnSubmission) (any, error)
	CancelTurn(conversationID, turnID string) error
	BindInteraction(conversationID string, binding session.Binding) error
}

type InitiativeRuntime interface {
	ObserveAmbient(conversationID string, observation session.AmbientObservation) error
	ObserveDesktop(conversationID string, observation session.DesktopObservation) (DesktopObservationResult, error)
	DecideParticipation(context.Context, string, session.ParticipationRequest) (session.ParticipationResponse, error)
}

type Secret interface {
	DigestEndpointKey(session.EndpointKind, string) (string, error)
	DigestPrincipal(session.PrincipalRef) (string, error)
}

type CharacterCatalog interface {
	ListCharacters() (character.Catalog, error)
}

type Transcript interface {
	OpenOrCreateEndpointConversationContext(context.Context, string, session.Binding, string) (history.ConversationBootstrap, error)
	ListConversationMessagesBeforeContext(context.Context, string, uint64, int) (history.MessagePage, error)
}

type CaptureRouter interface {
	Register(conversationID string, endpoint session.EndpointKind, interaction session.Context, send func(session.DesktopCaptureRequest) error) (string, func(), error)
	AcceptResult(context.Context, string, session.DesktopCaptureResult) error
}

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
	Events   <-chan session.ParticipationEvent
	Failures <-chan error
	Cancel   func()
}

func (s ParticipationSubscription) Unsubscribe() {
	if s.Cancel != nil {
		s.Cancel()
	}
}

type DesktopObservationStep struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Depends  []string `json:"dependsOn,omitempty"`
	OmitCode string   `json:"omitCode,omitempty"`
}

type DesktopObservationDiagnostic struct {
	Node   string `json:"node"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type DesktopObservationResult struct {
	Nodes       []DesktopObservationStep       `json:"nodes"`
	Action      string                         `json:"action"`
	OmitReasons []string                       `json:"omitReasons,omitempty"`
	Diagnostics []DesktopObservationDiagnostic `json:"diagnostics,omitempty"`
}

type Watch struct {
	Turns         EventSubscription
	Participation ParticipationSubscription
}

type Dependencies struct {
	Secret                 Secret
	Characters             CharacterCatalog
	Transcript             Transcript
	Turns                  TurnRuntime
	Initiative             InitiativeRuntime
	Captures               CaptureRouter
	SubscribeTurnEvents    func(string) (EventSubscription, error)
	SubscribeParticipation func(string) (ParticipationSubscription, error)
}

type Service struct {
	secret                 Secret
	characters             CharacterCatalog
	transcript             Transcript
	turns                  TurnRuntime
	initiative             InitiativeRuntime
	captures               CaptureRouter
	subscribeTurns         func(string) (EventSubscription, error)
	subscribeParticipation func(string) (ParticipationSubscription, error)
}

func New(deps Dependencies) *Service {
	return &Service{
		secret:                 deps.Secret,
		characters:             deps.Characters,
		transcript:             deps.Transcript,
		turns:                  deps.Turns,
		initiative:             deps.Initiative,
		captures:               deps.Captures,
		subscribeTurns:         deps.SubscribeTurnEvents,
		subscribeParticipation: deps.SubscribeParticipation,
	}
}
