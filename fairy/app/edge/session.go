package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	appsession "fairy/app/session"
	"fairy/context/character"
	"fairy/transport/session"
)

const maxVisualAssetBytes = 16 << 20

var (
	ErrCharacterCatalogUnavailable = errors.New("character catalog is unavailable")
	ErrStickerStoreUnavailable     = errors.New("sticker store is unavailable")
	ErrVisualPacksRootUnavailable  = errors.New("visual packs root is unavailable")
	ErrSessionUnavailable          = appsession.ErrSessionUnavailable
)

// NewSession returns a connect-scoped in-process facade. Closing it does not
// shut down Core or the shared Session service.
func (r *Runtime) NewSession() *appsession.Facade {
	if r == nil || r.sessions == nil {
		return nil
	}
	return appsession.NewFacade(r.sessions)
}

func (r *Runtime) CancelTurn(conversationID, turnID string) error {
	if r == nil || r.sessions == nil {
		return ErrSessionUnavailable
	}
	return r.sessions.CancelTurn(conversationID, turnID)
}

func (r *Runtime) ListCharacters(context.Context) (session.CharacterCatalog, error) {
	if r == nil || r.core == nil || r.core.Character == nil {
		return session.CharacterCatalog{}, ErrCharacterCatalogUnavailable
	}
	catalog, err := r.core.Character.ListCharacters()
	if err != nil {
		return session.CharacterCatalog{}, err
	}
	return projectCharacterCatalog(catalog), nil
}

func (r *Runtime) ListMessages(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (session.MessagePage, error) {
	if r == nil || r.sessions == nil {
		return session.MessagePage{}, ErrSessionUnavailable
	}
	return r.sessions.ListMessages(ctx, conversationID, beforeSequence, limit)
}

func (r *Runtime) ReadStickerContent(ctx context.Context, id string) (session.StickerContent, error) {
	if r == nil || r.core == nil || r.core.Stickers == nil {
		return session.StickerContent{}, ErrStickerStoreUnavailable
	}
	content, err := r.core.Stickers.Content(ctx, id)
	if err != nil {
		return session.StickerContent{}, err
	}
	return session.StickerContent{
		MIMEType:      content.MIMEType,
		ContentSHA256: content.ContentSHA256,
		Bytes:         content.Bytes,
	}, nil
}

func (r *Runtime) VisualAsset(_ context.Context, packID, assetPath string) ([]byte, error) {
	if r == nil || r.core == nil || strings.TrimSpace(r.core.ConfigRoot) == "" {
		return nil, ErrVisualPacksRootUnavailable
	}
	if packID == "" || strings.ContainsAny(packID, `/\#?`) {
		return nil, errors.New("visual pack ID is invalid")
	}
	full, err := character.ResolveAssetFile(character.VisualPacksRoot(r.core.ConfigRoot), packID+"/"+assetPath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("reading visual asset: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat visual asset: %w", err)
	}
	if info.Size() > maxVisualAssetBytes {
		return nil, errors.New("visual asset exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxVisualAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading visual asset: %w", err)
	}
	if int64(len(data)) > maxVisualAssetBytes {
		return nil, errors.New("visual asset exceeds size limit")
	}
	return data, nil
}

func projectCharacterCatalog(catalog character.Catalog) session.CharacterCatalog {
	projected := session.CharacterCatalog{
		Characters: make([]session.CharacterRecord, 0, len(catalog.Characters)),
	}
	for _, record := range catalog.Characters {
		projected.Characters = append(projected.Characters, projectCharacterRecord(record))
	}
	if catalog.Active != nil {
		active := projectCharacterRecord(*catalog.Active)
		projected.Active = &active
	}
	return projected
}

func projectCharacterRecord(record character.Record) session.CharacterRecord {
	projected := session.CharacterRecord{
		CharacterID: record.CharacterID,
		Revision:    record.Revision,
		Name:        record.Name,
		Appearance:  session.CharacterAppearance{Status: record.Appearance.Status},
	}
	if record.Appearance.Visual != nil {
		visual := projectVisualManifest(*record.Appearance.Visual)
		projected.Appearance.Visual = &visual
	}
	return projected
}

func projectVisualManifest(manifest character.Manifest) session.VisualManifest {
	states := make([]session.VisualState, 0, len(manifest.States))
	for _, state := range manifest.States {
		states = append(states, session.VisualState{
			ID:          state.ID,
			Description: state.Description,
			ImagePath:   state.ImagePath,
		})
	}
	return session.VisualManifest{
		SchemaVersion: uint64(manifest.SchemaVersion),
		PackID:        manifest.PackID,
		DisplayName:   manifest.DisplayName,
		Renderer:      manifest.Renderer,
		Frame:         session.VisualFrame{Width: float64(manifest.Frame.Width), Height: float64(manifest.Frame.Height)},
		Scale:         manifest.Scale,
		Anchor:        session.VisualAnchor{X: float64(manifest.Anchor.X), Y: float64(manifest.Anchor.Y)},
		States:        states,
	}
}
