package main

import (
	"context"
	"errors"

	"fairy/app/edge"
	"fairy/transport/session"
)

type sessionPlane interface {
	OpenSession(context.Context, session.OpenSessionRequest) (session.OpenSessionResponse, error)
	Watch(context.Context, string) (<-chan session.TurnEvent, error)
	SubmitTurn(context.Context, string, session.SubmitTurnRequest) (session.SubmitTurnResponse, error)
	CancelTurn(context.Context, string, string) error
	ReportExpressionDelivery(context.Context, session.ExpressionDeliveryResult) error
	ObserveDesktop(context.Context, string, session.DesktopObservation) (session.DesktopObservationResponse, error)
	SetDesktopCaptureHandler(func(context.Context, session.DesktopCaptureRequest) session.DesktopCaptureResult) error
	Close() error
}

type sessionAssets interface {
	ListCharacters(context.Context) (session.CharacterCatalog, error)
	ListMessages(context.Context, string, uint64, int) (session.MessagePage, error)
	ReadStickerContent(context.Context, string) (session.StickerContent, error)
	VisualAsset(context.Context, string, string) ([]byte, error)
}

type edgeSessionAssets struct {
	runtime *edge.Runtime
}

func (a edgeSessionAssets) ListCharacters(ctx context.Context) (session.CharacterCatalog, error) {
	if a.runtime == nil {
		return session.CharacterCatalog{}, edge.ErrCharacterCatalogUnavailable
	}
	return a.runtime.ListCharacters(ctx)
}

func (a edgeSessionAssets) ListMessages(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (session.MessagePage, error) {
	if a.runtime == nil {
		return session.MessagePage{}, edge.ErrSessionUnavailable
	}
	return a.runtime.ListMessages(ctx, conversationID, beforeSequence, limit)
}

func (a edgeSessionAssets) ReadStickerContent(ctx context.Context, id string) (session.StickerContent, error) {
	if a.runtime == nil {
		return session.StickerContent{}, edge.ErrStickerStoreUnavailable
	}
	return a.runtime.ReadStickerContent(ctx, id)
}

func (a edgeSessionAssets) VisualAsset(ctx context.Context, packID, assetPath string) ([]byte, error) {
	if a.runtime == nil {
		return nil, edge.ErrVisualPacksRootUnavailable
	}
	return a.runtime.VisualAsset(ctx, packID, assetPath)
}

func (a edgeAdapter) OpenSessionTransport() (sessionPlane, sessionAssets, error) {
	if a.runtime == nil {
		return nil, nil, errors.New("edge runtime is unavailable")
	}
	facade := a.runtime.NewSession()
	if facade == nil {
		return nil, nil, errors.New("edge session facade is unavailable")
	}
	return facade, edgeSessionAssets{runtime: a.runtime}, nil
}

func (a edgeAdapter) InterruptTurn(ctx context.Context, conversationID, turnID string) error {
	_ = ctx
	if a.runtime == nil {
		return errors.New("edge session facade is unavailable")
	}
	return a.runtime.CancelTurn(conversationID, turnID)
}

var (
	_ sessionPlane  = (*session.SessionSocket)(nil)
	_ sessionAssets = (*session.Client)(nil)
)
