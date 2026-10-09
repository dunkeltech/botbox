package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func rollbackTx(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(ctx)
}

const maxJSONBodyBytes = 1 << 20

var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func toPGUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func fromPGUUID(id pgtype.UUID) uuid.UUID {
	return uuid.UUID(id.Bytes)
}

func toPGTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, logger, http.StatusBadRequest, "validation_error", "invalid JSON request body")
		return false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, logger, http.StatusBadRequest, "validation_error", "unexpected trailing JSON data")
		return false
	}
	return true
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, dst any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, logger, http.StatusBadRequest, "validation_error", "invalid JSON request body")
		return false
	}
	return true
}

func validateSlug(slug string) (string, bool) {
	trimmed := strings.TrimSpace(slug)
	if len(trimmed) < 1 || len(trimmed) > 100 || !slugPattern.MatchString(trimmed) {
		return "", false
	}
	return trimmed, true
}

func validateEmail(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) < 3 || len(normalized) > 255 || strings.ContainsAny(normalized, " \t\r\n") {
		return "", false
	}
	addr, err := mail.ParseAddress(normalized)
	if err != nil || addr.Address != normalized {
		return "", false
	}
	at := strings.LastIndexByte(normalized, '@')
	if at <= 0 || at >= len(normalized)-1 {
		return "", false
	}
	domain := normalized[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return normalized, true
}

func validateDisplayName(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 1 || len(trimmed) > 100 {
		return "", false
	}
	return trimmed, true
}

func validateWorkspaceName(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 1 || len(trimmed) > 100 {
		return "", false
	}
	return trimmed, true
}

func validateLocale(raw *string, fallback string) (string, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return fallback, true
	}
	trimmed := strings.TrimSpace(*raw)
	if len(trimmed) > 10 {
		return "", false
	}
	return trimmed, true
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func toAPIEmailPtr(v pgtype.Text) *openapi_types.Email {
	if !v.Valid {
		return nil
	}
	e := openapi_types.Email(v.String)
	return &e
}

func toAPIUser(u db.User) api.User {
	return api.User{
		Id:          fromPGUUID(u.ID),
		Email:       openapi_types.Email(u.Email),
		DisplayName: u.DisplayName,
		Locale:      u.Locale,
		CreatedAt:   u.CreatedAt.Time.UTC(),
	}
}

func toAPIWorkspaceFromRow(w db.Workspace, role string) api.WorkspaceSummary {
	return api.WorkspaceSummary{
		Id:            fromPGUUID(w.ID),
		Name:          w.Name,
		Slug:          w.Slug,
		DefaultDriver: api.DriverType(w.DefaultDriver),
		DefaultLocale: w.DefaultLocale,
		Role:          api.WorkspaceRole(role),
		CreatedAt:     w.CreatedAt.Time.UTC(),
	}
}

func toAPIWorkspaceFromUserRow(w db.GetWorkspaceByIDForUserRow) api.WorkspaceSummary {
	return api.WorkspaceSummary{
		Id:            fromPGUUID(w.ID),
		Name:          w.Name,
		Slug:          w.Slug,
		DefaultDriver: api.DriverType(w.DefaultDriver),
		DefaultLocale: w.DefaultLocale,
		Role:          api.WorkspaceRole(w.Role),
		CreatedAt:     w.CreatedAt.Time.UTC(),
	}
}

func toAPIWorkspaceList(rows []db.ListWorkspacesForUserRow) []api.WorkspaceSummary {
	out := make([]api.WorkspaceSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.WorkspaceSummary{
			Id:            fromPGUUID(r.ID),
			Name:          r.Name,
			Slug:          r.Slug,
			DefaultDriver: api.DriverType(r.DefaultDriver),
			DefaultLocale: r.DefaultLocale,
			Role:          api.WorkspaceRole(r.Role),
			CreatedAt:     r.CreatedAt.Time.UTC(),
		})
	}
	return out
}

func toAPIWorkspaceMember(m db.ListWorkspaceMembersRow) api.WorkspaceMember {
	return api.WorkspaceMember{
		WorkspaceId: fromPGUUID(m.WorkspaceID),
		UserId:      fromPGUUID(m.UserID),
		Email:       openapi_types.Email(m.Email),
		DisplayName: m.DisplayName,
		Locale:      m.Locale,
		Role:        api.WorkspaceRole(m.Role),
		JoinedAt:    m.JoinedAt.Time.UTC(),
	}
}

func toAPIWorkspaceMemberGet(m db.GetWorkspaceMemberRow) api.WorkspaceMember {
	return api.WorkspaceMember{
		WorkspaceId: fromPGUUID(m.WorkspaceID),
		UserId:      fromPGUUID(m.UserID),
		Email:       openapi_types.Email(m.Email),
		DisplayName: m.DisplayName,
		Locale:      m.Locale,
		Role:        api.WorkspaceRole(m.Role),
		JoinedAt:    m.JoinedAt.Time.UTC(),
	}
}

func toAPIInvitationSummary(inv db.Invitation) api.InvitationSummary {
	var acceptedAt *time.Time
	if inv.AcceptedAt.Valid {
		t := inv.AcceptedAt.Time.UTC()
		acceptedAt = &t
	}
	return api.InvitationSummary{
		Id:          fromPGUUID(inv.ID),
		WorkspaceId: fromPGUUID(inv.WorkspaceID),
		Email:       toAPIEmailPtr(inv.Email),
		Role:        api.WorkspaceRole(inv.Role),
		ExpiresAt:   inv.ExpiresAt.Time.UTC(),
		AcceptedAt:  acceptedAt,
		CreatedAt:   inv.CreatedAt.Time.UTC(),
	}
}

func toAPIBot(b db.Bot) api.Bot {
	return toAPIBotWithEnv(b, nil)
}

func toAPIBotWithEnv(b db.Bot, env *db.Environment) api.Bot {
	var ownerID *uuid.UUID
	if b.OwnerUserID.Valid {
		id := fromPGUUID(b.OwnerUserID)
		ownerID = &id
	}
	status := api.BotStatusStopped
	mood := api.AvatarMoodOffline
	if env != nil {
		switch env.Status {
		case string(compute.StatusRunning):
			status = api.BotStatusIdle
			if env.ControlHolderUserID.Valid {
				mood = api.AvatarMoodThinking
			} else {
				mood = api.AvatarMoodIdle
			}
		case string(compute.StatusCreating):
			status = api.BotStatusThinking
			mood = api.AvatarMoodThinking
		default:
			status = api.BotStatusStopped
			mood = api.AvatarMoodOffline
		}
	}
	return api.Bot{
		Id:            fromPGUUID(b.ID),
		WorkspaceId:   fromPGUUID(b.WorkspaceID),
		OwnerUserId:   ownerID,
		Name:          b.Name,
		Description:   b.Description,
		AvatarSeed:    b.AvatarSeed,
		LlmMode:       api.LlmMode(b.LlmMode),
		LlmProvider:   b.LlmProvider,
		LlmModel:      b.LlmModel,
		DriverType:    api.DriverType(b.DriverType),
		ReplyLanguage: b.ReplyLanguage,
		Status:        status,
		Mood:          mood,
		CreatedAt:     b.CreatedAt.Time.UTC(),
		UpdatedAt:     b.UpdatedAt.Time.UTC(),
	}
}

func recordAudit(ctx context.Context, q *db.Queries, workspaceID, actorUserID *uuid.UUID, action, targetType string, targetID *uuid.UUID, meta map[string]any) error {
	var wsPG pgtype.UUID
	if workspaceID != nil {
		wsPG = toPGUUID(*workspaceID)
	}
	var actorPG pgtype.UUID
	if actorUserID != nil {
		actorPG = toPGUUID(*actorUserID)
	}
	var targetTypePG pgtype.Text
	if targetType != "" {
		targetTypePG = pgtype.Text{String: targetType, Valid: true}
	}
	var targetIDPG pgtype.UUID
	if targetID != nil {
		targetIDPG = toPGUUID(*targetID)
	}
	metaBytes := []byte("{}")
	if len(meta) > 0 {
		encoded, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		metaBytes = encoded
	}

	_, err := q.CreateAuditLog(ctx, db.CreateAuditLogParams{
		WorkspaceID: wsPG,
		ActorUserID: actorPG,
		Action:      action,
		TargetType:  targetTypePG,
		TargetID:    targetIDPG,
		Metadata:    metaBytes,
	})
	return err
}
