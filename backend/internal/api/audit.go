// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/observability"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	defaultAuditLogLimit = 50
	maxAuditLogLimit     = 200
)

func (s *server) handleListWorkspaceAuditLogs(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewAuditLog)
	if !ok {
		return
	}

	var actionFilter pgtype.Text
	if raw := strings.TrimSpace(r.URL.Query().Get("action")); raw != "" {
		actionFilter = pgtype.Text{String: raw, Valid: true}
	}
	var targetTypeFilter pgtype.Text
	if raw := strings.TrimSpace(r.URL.Query().Get("targetType")); raw != "" {
		targetTypeFilter = pgtype.Text{String: raw, Valid: true}
	}

	sinceFilter, err := parseOptionalRFC3339(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "since must be a valid RFC3339 timestamp")
		return
	}
	untilFilter, err := parseOptionalRFC3339(r.URL.Query().Get("until"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "until must be a valid RFC3339 timestamp")
		return
	}
	if sinceFilter.Valid && untilFilter.Valid && sinceFilter.Time.After(untilFilter.Time) {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "since must not be after until")
		return
	}

	limit := defaultAuditLogLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > maxAuditLogLimit {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	var offset int32
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, parseErr := strconv.ParseInt(rawOffset, 10, 32)
		if parseErr != nil || parsed < 0 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "offset must be between 0 and 2147483647")
			return
		}
		offset = int32(parsed) // #nosec G115 -- ParseInt validates the 32-bit range.
	}

	total, err := s.queries.CountAuditLogsByWorkspaceFiltered(r.Context(), db.CountAuditLogsByWorkspaceFilteredParams{
		WorkspaceID: ws.ID,
		Action:      actionFilter,
		TargetType:  targetTypeFilter,
		Since:       sinceFilter,
		Until:       untilFilter,
	})
	if err != nil {
		s.logger.Error("count audit logs", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load audit logs")
		return
	}

	rows, err := s.queries.ListAuditLogsByWorkspaceFiltered(r.Context(), db.ListAuditLogsByWorkspaceFilteredParams{
		WorkspaceID: ws.ID,
		Action:      actionFilter,
		TargetType:  targetTypeFilter,
		Since:       sinceFilter,
		Until:       untilFilter,
		PageLimit:   int32(limit), // #nosec G115 -- validated 1..200
		PageOffset:  offset,
	})
	if err != nil {
		s.logger.Error("list audit logs", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load audit logs")
		return
	}

	entries := make([]apigen.AuditLogEntry, 0, len(rows))
	for _, row := range rows {
		wsID := openapi_types.UUID(fromPGUUID(row.WorkspaceID))
		entry := apigen.AuditLogEntry{
			Id:          openapi_types.UUID(fromPGUUID(row.ID)),
			WorkspaceId: wsID,
			Action:      row.Action,
			Metadata:    sanitizeAuditMetadata(row.Metadata),
			CreatedAt:   row.CreatedAt.Time.UTC(),
		}
		if row.ActorUserID.Valid {
			actorID := openapi_types.UUID(fromPGUUID(row.ActorUserID))
			entry.ActorUserId = &actorID
		}
		if row.ActorEmail != "" {
			email := row.ActorEmail
			entry.ActorEmail = &email
		}
		if row.ActorDisplayName != "" {
			display := row.ActorDisplayName
			entry.ActorDisplayName = &display
		}
		if row.TargetType != "" {
			tt := row.TargetType
			entry.TargetType = &tt
		}
		if row.TargetID.Valid {
			tid := openapi_types.UUID(fromPGUUID(row.TargetID))
			entry.TargetId = &tid
		}
		entries = append(entries, entry)
	}

	writeJSON(w, s.logger, http.StatusOK, apigen.AuditLogListResponse{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  int(offset),
	})
}

func parseOptionalRFC3339(raw string) (pgtype.Timestamptz, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return pgtype.Timestamptz{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	return pgtype.Timestamptz{Time: parsed.UTC(), Valid: true}, nil
}

func sanitizeAuditMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return scrubAuditMap(parsed)
}

var auditSensitiveMetadataKeys = map[string]bool{
	"password":          true,
	"password_hash":     true,
	"secret":            true,
	"secret_value":      true,
	"token":             true,
	"access_token":      true,
	"refresh_token":     true,
	"auth_broker_token": true,
	"authorization":     true,
	"api_key":           true,
	"apikey":            true,
	"kek":               true,
	"private_key":       true,
	"client_secret":     true,
	"wrapped_dek":       true,
	"encrypted_value":   true,
	"setup_secret":      true,
}

func scrubAuditMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if auditSensitiveMetadataKeys[strings.ToLower(strings.TrimSpace(k))] {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = scrubAuditValue(v)
	}
	return out
}

func scrubAuditValue(v any) any {
	switch tv := v.(type) {
	case string:
		return observability.ScrubString(tv)
	case map[string]any:
		return scrubAuditMap(tv)
	case []any:
		out := make([]any, len(tv))
		for i, item := range tv {
			out[i] = scrubAuditValue(item)
		}
		return out
	default:
		return v
	}
}
