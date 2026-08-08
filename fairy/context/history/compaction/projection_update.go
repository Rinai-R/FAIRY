package compaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrPromptWindowRevisionChanged = errors.New("prompt window revision changed")

func updatePromptProjection(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision, nextWindowRevision, nextProjectionRevision int64,
	state []byte,
	now int64,
) error {
	changed, err := tx.Exec(ctx, `
UPDATE prompt_windows
SET revision = $4, projection_revision = $5, projection_state = $6, updated_at_ms = $7
WHERE conversation_id = $1 AND revision = $2 AND projection_revision = $3`,
		conversationID, expectedWindowRevision, expectedProjectionRevision,
		nextWindowRevision, nextProjectionRevision, state, now)
	if err != nil {
		return fmt.Errorf("updating prompt projection: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return ErrPromptWindowRevisionChanged
	}
	return nil
}
