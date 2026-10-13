package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/desk"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// PersistDesktopActions upserts agent journal entries. The conflict update
// keeps the first screenshot so a repeated pull cannot wipe retained bytes.
// Params and images are stored and are not written to logs.
func PersistDesktopActions(ctx context.Context, q *db.Queries, workspaceID, botID uuid.UUID, records []desk.StoredAction) (string, uint64, error) {
	if q == nil || len(records) == 0 {
		return "", 0, nil
	}
	var session string
	var through uint64
	for _, rec := range records {
		if rec.Seq == 0 || rec.Session == "" || rec.Kind == "" {
			continue
		}
		params := rec.Params
		if len(params) == 0 || !json.Valid(params) {
			params = []byte(`{}`)
		}
		image := rec.Image
		mime := rec.Mime
		if len(image) == 0 {
			mime = ""
		} else if mime == "" {
			mime = "image/png"
		}
		at := rec.At.UTC()
		if rec.At.IsZero() {
			at = time.Now().UTC()
		}
		if rec.Seq > uint64(math.MaxInt64) {
			return session, through, fmt.Errorf("desktop action sequence overflows")
		}
		if _, err := q.InsertDesktopAction(ctx, db.InsertDesktopActionParams{
			ID:             pgID(uuid.New()),
			WorkspaceID:    pgID(workspaceID),
			BotID:          pgID(botID),
			SessionID:      rec.Session,
			AgentSeq:       int64(rec.Seq), // #nosec G115 -- rejected sequences above MaxInt64 above
			Kind:           rec.Kind,
			Params:         params,
			Result:         rec.Result,
			ErrorCode:      rec.ErrorCode,
			Screenshot:     image,
			ScreenshotMime: mime,
			CoordX:         rec.X,
			CoordY:         rec.Y,
			HasCoord:       rec.HasPoint,
			CreatedAt:      pgtype.Timestamptz{Time: at, Valid: true},
		}); err != nil {
			return session, through, err
		}
		session = rec.Session
		if rec.Seq > through {
			through = rec.Seq
		}
	}
	return session, through, nil
}
