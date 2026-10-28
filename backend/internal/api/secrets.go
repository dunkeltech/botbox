package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/egresspolicy"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/netguard"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	maxSecretNameLen      = 128
	maxSecretValueBytes   = 65536
	defaultEgressLogLimit = 100
	maxEgressLogLimit     = 200
)

var (
	secretNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	httpHeaderNameRegex = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]{1,128}$`)
)

var forbiddenHeaderNames = map[string]bool{
	"host":                true,
	"content-length":      true,
	"transfer-encoding":   true,
	"connection":          true,
	"keep-alive":          true,
	"upgrade":             true,
	"te":                  true,
	"trailer":             true,
	"proxy-connection":    true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
}

func mustEphemeralKeyRing(logger *slog.Logger) *secrets.KeyRing {
	raw := make([]byte, secrets.AES256KeySize)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		panic(fmt.Sprintf("generate ephemeral secrets KEK: %v", err))
	}
	ring, err := secrets.NewKeyRing("ephemeral", map[string][]byte{"ephemeral": raw})
	if err != nil {
		panic(fmt.Sprintf("init ephemeral secrets keyring: %v", err))
	}
	if logger != nil {
		logger.Warn("secrets key ring unset; using ephemeral in-memory KEK")
	}
	return ring
}

func secretAAD(workspaceID uuid.UUID, scope string, secretID uuid.UUID) []byte {
	return secrets.SecretAAD(workspaceID, scope, secretID)
}

type validatedSecretInput struct {
	name            string
	domainMatch     string
	matchSubdomains bool
	pathPrefix      string
	authScheme      string
	headerName      string
	username        string
}

func validateSecretInput(
	rawName, rawDomain string,
	matchSubdomains *bool,
	rawPathPrefix *string,
	scheme api.SecretAuthScheme,
	rawHeaderName *string,
	rawUsername *string,
) (validatedSecretInput, string) {
	name := strings.TrimSpace(rawName)
	if !secretNamePattern.MatchString(name) || len(name) > maxSecretNameLen {
		return validatedSecretInput{}, "name must start with a letter or digit and use only letters, digits, '.', '_' or '-' (max 128 chars)"
	}

	domain, ok := normalizeAndValidateHost(rawDomain)
	if !ok {
		return validatedSecretInput{}, "domainMatch must be a valid DNS hostname without scheme, port, wildcard or path"
	}

	subdomains := false
	if matchSubdomains != nil {
		subdomains = *matchSubdomains
	}

	pathPrefix := ""
	if rawPathPrefix != nil && strings.TrimSpace(*rawPathPrefix) != "" {
		pathPrefix = strings.TrimSpace(*rawPathPrefix)
		if len(pathPrefix) > 255 || !strings.HasPrefix(pathPrefix, "/") || strings.ContainsAny(pathPrefix, "?# \t\r\n") {
			return validatedSecretInput{}, "pathPrefix must start with '/' and exclude query strings or whitespace"
		}
	}

	if !scheme.Valid() {
		return validatedSecretInput{}, "authScheme must be bearer, header or basic"
	}

	headerName := ""
	if rawHeaderName != nil {
		headerName = strings.TrimSpace(*rawHeaderName)
	}
	username := ""
	if rawUsername != nil {
		username = strings.TrimSpace(*rawUsername)
	}

	switch scheme {
	case api.Bearer:
		if headerName == "" {
			headerName = "Authorization"
		}
	case api.Basic:
		if headerName == "" {
			headerName = "Authorization"
		}
		if username == "" {
			return validatedSecretInput{}, "username is required when authScheme is basic"
		}
		if strings.Contains(username, ":") || strings.ContainsAny(username, "\r\n") {
			return validatedSecretInput{}, "username cannot contain ':' or newlines"
		}
	case api.Header:
		if headerName == "" {
			return validatedSecretInput{}, "headerName is required when authScheme is header"
		}
	}

	if !httpHeaderNameRegex.MatchString(headerName) || forbiddenHeaderNames[strings.ToLower(headerName)] {
		return validatedSecretInput{}, "headerName is invalid or reserved"
	}

	return validatedSecretInput{
		name:            name,
		domainMatch:     domain,
		matchSubdomains: subdomains,
		pathPrefix:      pathPrefix,
		authScheme:      string(scheme),
		headerName:      headerName,
		username:        username,
	}, ""
}

func validateSecretValue(val string) (string, bool) {
	if strings.TrimSpace(val) == "" || len(val) > maxSecretValueBytes {
		return "secret value must be non-empty and at most 64 KiB", false
	}
	return "", true
}

func normalizeAndValidateHost(raw string) (string, bool) {
	return egresspolicy.NormalizeAndValidateHost(raw)
}

func (s *server) loadWorkspaceBotForSecret(w http.ResponseWriter, r *http.Request, requireManage bool) (db.GetWorkspaceByIDForUserRow, db.Bot, uuid.UUID, bool) {
	action := workspaces.ActionViewWorkspace
	if requireManage {
		action = ""
	}
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, action)
	if !ok {
		return db.GetWorkspaceByIDForUserRow{}, db.Bot{}, uuid.Nil, false
	}

	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return db.GetWorkspaceByIDForUserRow{}, db.Bot{}, uuid.Nil, false
	}

	botRow, err := s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(botID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
			return db.GetWorkspaceByIDForUserRow{}, db.Bot{}, uuid.Nil, false
		}
		s.logger.Error("load bot for secrets", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot")
		return db.GetWorkspaceByIDForUserRow{}, db.Bot{}, uuid.Nil, false
	}

	if requireManage {
		isBotOwner := botRow.OwnerUserID.Valid && fromPGUUID(botRow.OwnerUserID) == claims.Subject
		if !workspaces.Can(actorRole, workspaces.ActionManageBotSecrets, isBotOwner) {
			writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient permissions to manage bot secrets or egress")
			return db.GetWorkspaceByIDForUserRow{}, db.Bot{}, uuid.Nil, false
		}
	}

	return wsRow, botRow, claims.Subject, true
}

func (s *server) handleListWorkspaceSecrets(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}

	rows, err := s.queries.ListWorkspaceSecretsByWorkspace(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list workspace secrets", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list workspace secrets")
		return
	}

	out := make([]api.SecretMetadata, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIWorkspaceSecretMetadata(row))
	}
	writeJSON(w, s.logger, http.StatusOK, api.SecretListResponse{Secrets: out})
}

func (s *server) handleCreateWorkspaceSecret(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}

	var req api.CreateSecretRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	parsed, errMsg := validateSecretInput(req.Name, req.DomainMatch, req.MatchSubdomains, req.PathPrefix, req.AuthScheme, req.HeaderName, req.Username)
	if errMsg != "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", errMsg)
		return
	}
	if valErr, validVal := validateSecretValue(req.Value); !validVal {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", valErr)
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	secretID := uuid.New()
	env, err := s.keyRing.Encrypt([]byte(req.Value), secretAAD(wsID, string(api.SecretScopeWorkspace), secretID))
	if err != nil {
		s.logger.Error("encrypt workspace secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to encrypt secret")
		return
	}

	row, err := s.queries.CreateWorkspaceSecret(r.Context(), db.CreateWorkspaceSecretParams{
		ID:              toPGUUID(secretID),
		WorkspaceID:     wsRow.ID,
		Name:            parsed.name,
		DomainMatch:     parsed.domainMatch,
		MatchSubdomains: parsed.matchSubdomains,
		PathPrefix:      parsed.pathPrefix,
		AuthScheme:      parsed.authScheme,
		HeaderName:      parsed.headerName,
		Username:        parsed.username,
		KekID:           env.KeyID,
		WrappedDek:      env.WrappedDEK,
		EncryptedValue:  env.Ciphertext,
		MaskedPreview:   secrets.MaskedPreview(req.Value),
		CreatedBy:       toPGUUID(claims.Subject),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "secret.already_exists", "a workspace secret with this name already exists")
			return
		}
		s.logger.Error("create workspace secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to store secret")
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "secret.workspace_created", "workspace_secret", &secretID, map[string]any{
		"name":   parsed.name,
		"domain": parsed.domainMatch,
	})
	writeJSON(w, s.logger, http.StatusCreated, toAPIWorkspaceSecretMetadata(row))
}

func (s *server) handleUpdateWorkspaceSecret(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}

	secretID, err := uuid.Parse(chi.URLParam(r, "secretID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret identifier")
		return
	}

	var req api.UpdateSecretRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	existing, err := s.queries.GetWorkspaceSecretByWorkspaceAndID(r.Context(), db.GetWorkspaceSecretByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(secretID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "secret.not_found", "secret not found")
			return
		}
		s.logger.Error("load workspace secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load secret")
		return
	}

	parsed, errMsg := validateSecretInput(req.Name, req.DomainMatch, req.MatchSubdomains, req.PathPrefix, req.AuthScheme, req.HeaderName, req.Username)
	if errMsg != "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", errMsg)
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	kekID := existing.KekID
	wrappedDEK := existing.WrappedDek
	encryptedVal := existing.EncryptedValue
	maskedPreview := existing.MaskedPreview

	if req.Value != nil && *req.Value != "" {
		if valErr, validVal := validateSecretValue(*req.Value); !validVal {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", valErr)
			return
		}
		env, encErr := s.keyRing.Encrypt([]byte(*req.Value), secretAAD(wsID, string(api.SecretScopeWorkspace), secretID))
		if encErr != nil {
			s.logger.Error("re-encrypt workspace secret", "err", encErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to encrypt secret")
			return
		}
		kekID = env.KeyID
		wrappedDEK = env.WrappedDEK
		encryptedVal = env.Ciphertext
		maskedPreview = secrets.MaskedPreview(*req.Value)
	}

	updated, err := s.queries.UpdateWorkspaceSecretByWorkspaceAndID(r.Context(), db.UpdateWorkspaceSecretByWorkspaceAndIDParams{
		WorkspaceID:     wsRow.ID,
		ID:              existing.ID,
		Name:            parsed.name,
		DomainMatch:     parsed.domainMatch,
		MatchSubdomains: parsed.matchSubdomains,
		PathPrefix:      parsed.pathPrefix,
		AuthScheme:      parsed.authScheme,
		HeaderName:      parsed.headerName,
		Username:        parsed.username,
		KekID:           kekID,
		WrappedDek:      wrappedDEK,
		EncryptedValue:  encryptedVal,
		MaskedPreview:   maskedPreview,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "secret.already_exists", "a workspace secret with this name already exists")
			return
		}
		s.logger.Error("update workspace secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update secret")
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "secret.workspace_updated", "workspace_secret", &secretID, map[string]any{
		"name":   parsed.name,
		"domain": parsed.domainMatch,
	})
	writeJSON(w, s.logger, http.StatusOK, toAPIWorkspaceSecretMetadata(updated))
}

func (s *server) handleDeleteWorkspaceSecret(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}

	secretID, err := uuid.Parse(chi.URLParam(r, "secretID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret identifier")
		return
	}

	rowsAffected, err := s.queries.DeleteWorkspaceSecretByWorkspaceAndID(r.Context(), db.DeleteWorkspaceSecretByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(secretID),
	})
	if err != nil {
		s.logger.Error("delete workspace secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete secret")
		return
	}
	if rowsAffected == 0 {
		writeError(w, s.logger, http.StatusNotFound, "secret.not_found", "secret not found")
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "secret.workspace_deleted", "workspace_secret", &secretID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListBotSecrets(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, _, ok := s.loadWorkspaceBotForSecret(w, r, false)
	if !ok {
		return
	}

	botSecrets, err := s.queries.ListBotSecretsByWorkspaceAndBot(r.Context(), db.ListBotSecretsByWorkspaceAndBotParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
	})
	if err != nil {
		s.logger.Error("list bot secrets", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list secrets")
		return
	}

	wsSecrets, err := s.queries.ListWorkspaceSecretsByWorkspace(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list inherited workspace secrets", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list secrets")
		return
	}

	out := make([]api.SecretMetadata, 0, len(botSecrets)+len(wsSecrets))
	for _, row := range botSecrets {
		out = append(out, toAPIBotSecretMetadata(row))
	}
	for _, row := range wsSecrets {
		out = append(out, toAPIWorkspaceSecretMetadata(row))
	}
	writeJSON(w, s.logger, http.StatusOK, api.SecretListResponse{Secrets: out})
}

func (s *server) handleCreateBotSecret(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, actorID, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}

	var req api.CreateSecretRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	parsed, errMsg := validateSecretInput(req.Name, req.DomainMatch, req.MatchSubdomains, req.PathPrefix, req.AuthScheme, req.HeaderName, req.Username)
	if errMsg != "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", errMsg)
		return
	}
	if valErr, validVal := validateSecretValue(req.Value); !validVal {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", valErr)
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	secretID := uuid.New()
	env, err := s.keyRing.Encrypt([]byte(req.Value), secretAAD(wsID, string(api.SecretScopeBot), secretID))
	if err != nil {
		s.logger.Error("encrypt bot secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to encrypt secret")
		return
	}

	row, err := s.queries.CreateBotSecret(r.Context(), db.CreateBotSecretParams{
		ID:              toPGUUID(secretID),
		WorkspaceID:     wsRow.ID,
		BotID:           botRow.ID,
		Name:            parsed.name,
		DomainMatch:     parsed.domainMatch,
		MatchSubdomains: parsed.matchSubdomains,
		PathPrefix:      parsed.pathPrefix,
		AuthScheme:      parsed.authScheme,
		HeaderName:      parsed.headerName,
		Username:        parsed.username,
		KekID:           env.KeyID,
		WrappedDek:      env.WrappedDEK,
		EncryptedValue:  env.Ciphertext,
		MaskedPreview:   secrets.MaskedPreview(req.Value),
		CreatedBy:       toPGUUID(actorID),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "secret.already_exists", "a secret with this name already exists on this bot")
			return
		}
		s.logger.Error("create bot secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to store secret")
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &actorID, "secret.bot_created", "bot_secret", &secretID, map[string]any{
		"bot_id": fromPGUUID(botRow.ID).String(),
		"name":   parsed.name,
		"domain": parsed.domainMatch,
	})
	writeJSON(w, s.logger, http.StatusCreated, toAPIBotSecretMetadata(row))
}

func (s *server) handleUpdateBotSecret(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, actorID, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}

	secretID, err := uuid.Parse(chi.URLParam(r, "secretID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret identifier")
		return
	}

	var req api.UpdateSecretRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	existing, err := s.queries.GetBotSecretByWorkspaceBotAndID(r.Context(), db.GetBotSecretByWorkspaceBotAndIDParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		ID:          toPGUUID(secretID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "secret.not_found", "secret not found")
			return
		}
		s.logger.Error("load bot secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load secret")
		return
	}

	parsed, errMsg := validateSecretInput(req.Name, req.DomainMatch, req.MatchSubdomains, req.PathPrefix, req.AuthScheme, req.HeaderName, req.Username)
	if errMsg != "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", errMsg)
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	kekID := existing.KekID
	wrappedDEK := existing.WrappedDek
	encryptedVal := existing.EncryptedValue
	maskedPreview := existing.MaskedPreview

	if req.Value != nil && *req.Value != "" {
		if valErr, validVal := validateSecretValue(*req.Value); !validVal {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", valErr)
			return
		}
		env, encErr := s.keyRing.Encrypt([]byte(*req.Value), secretAAD(wsID, string(api.SecretScopeBot), secretID))
		if encErr != nil {
			s.logger.Error("re-encrypt bot secret", "err", encErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to encrypt secret")
			return
		}
		kekID = env.KeyID
		wrappedDEK = env.WrappedDEK
		encryptedVal = env.Ciphertext
		maskedPreview = secrets.MaskedPreview(*req.Value)
	}

	updated, err := s.queries.UpdateBotSecretByWorkspaceBotAndID(r.Context(), db.UpdateBotSecretByWorkspaceBotAndIDParams{
		WorkspaceID:     wsRow.ID,
		BotID:           botRow.ID,
		ID:              existing.ID,
		Name:            parsed.name,
		DomainMatch:     parsed.domainMatch,
		MatchSubdomains: parsed.matchSubdomains,
		PathPrefix:      parsed.pathPrefix,
		AuthScheme:      parsed.authScheme,
		HeaderName:      parsed.headerName,
		Username:        parsed.username,
		KekID:           kekID,
		WrappedDek:      wrappedDEK,
		EncryptedValue:  encryptedVal,
		MaskedPreview:   maskedPreview,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "secret.already_exists", "a secret with this name already exists on this bot")
			return
		}
		s.logger.Error("update bot secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update secret")
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &actorID, "secret.bot_updated", "bot_secret", &secretID, map[string]any{
		"bot_id": fromPGUUID(botRow.ID).String(),
		"name":   parsed.name,
		"domain": parsed.domainMatch,
	})
	writeJSON(w, s.logger, http.StatusOK, toAPIBotSecretMetadata(updated))
}

func (s *server) handleDeleteBotSecret(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, actorID, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}

	secretID, err := uuid.Parse(chi.URLParam(r, "secretID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret identifier")
		return
	}

	rowsAffected, err := s.queries.DeleteBotSecretByWorkspaceBotAndID(r.Context(), db.DeleteBotSecretByWorkspaceBotAndIDParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		ID:          toPGUUID(secretID),
	})
	if err != nil {
		s.logger.Error("delete bot secret", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete secret")
		return
	}
	if rowsAffected == 0 {
		writeError(w, s.logger, http.StatusNotFound, "secret.not_found", "secret not found")
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	_ = recordAudit(r.Context(), s.queries, &wsID, &actorID, "secret.bot_deleted", "bot_secret", &secretID, map[string]any{
		"bot_id": fromPGUUID(botRow.ID).String(),
	})
	w.WriteHeader(http.StatusNoContent)
}

type egressPolicyRevisionNotifier interface {
	PublishEgressPolicyRevision(context.Context, uuid.UUID, uuid.UUID, int64) (int64, error)
}

func isBotEnvironmentLive(ctx context.Context, q *db.Queries, wsPG, botPG pgtype.UUID) (bool, error) {
	envRow, err := q.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: wsPG,
		ID:          botPG,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return envRow.Status == "running" || envRow.Status == "creating", nil
}

func computeEnforcementStatus(liveProxy bool, revision, ackedRevision int64) api.EgressEnforcementStatus {
	if !liveProxy {
		return api.EgressEnforcementStatusNotRunning
	}
	if ackedRevision >= revision {
		return api.EgressEnforcementStatusApplied
	}
	return api.EgressEnforcementStatusPending
}

func buildEgressPolicyResponse(
	wsID, botID uuid.UUID,
	policy api.EgressPolicyMode,
	allowedHosts, blockedHosts, effectiveHosts []string,
	revision, ackedRevision int64,
	liveProxy bool,
	updatedAt time.Time,
) api.EgressPolicyResponse {
	if allowedHosts == nil {
		allowedHosts = []string{}
	}
	if blockedHosts == nil {
		blockedHosts = []string{}
	}
	if effectiveHosts == nil {
		effectiveHosts = []string{}
	}
	return api.EgressPolicyResponse{
		BotId:                botID,
		WorkspaceId:          wsID,
		Policy:               policy,
		AllowedHosts:         allowedHosts,
		BlockedHosts:         blockedHosts,
		EffectiveSecretHosts: effectiveHosts,
		Revision:             revision,
		AcknowledgedRevision: ackedRevision,
		EnforcementStatus:    computeEnforcementStatus(liveProxy, revision, ackedRevision),
		UpdatedAt:            updatedAt,
	}
}

func (s *server) handleGetBotEgressPolicy(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, _, ok := s.loadWorkspaceBotForSecret(w, r, false)
	if !ok {
		return
	}

	policy := api.Allowlist
	allowedHosts := []string{}
	blockedHosts := []string{}
	var revision, ackedRevision int64
	updatedAt := botRow.UpdatedAt.Time.UTC()

	ruleRow, err := s.queries.GetEgressRuleByWorkspaceAndBot(r.Context(), db.GetEgressRuleByWorkspaceAndBotParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("get egress rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load egress policy")
		return
	}
	if err == nil {
		policy = api.EgressPolicyMode(ruleRow.Policy)
		if ruleRow.AllowedHosts != nil {
			allowedHosts = ruleRow.AllowedHosts
		}
		if ruleRow.BlockedHosts != nil {
			blockedHosts = ruleRow.BlockedHosts
		}
		revision = ruleRow.Revision
		ackedRevision = ruleRow.AcknowledgedRevision
		updatedAt = ruleRow.UpdatedAt.Time.UTC()
	}

	effectiveHosts, err := collectEffectiveSecretHostsWithQueries(r.Context(), s.queries, wsRow.ID, botRow.ID)
	if err != nil {
		s.logger.Error("collect secret hosts", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load egress policy")
		return
	}
	liveProxy, err := isBotEnvironmentLive(r.Context(), s.queries, wsRow.ID, botRow.ID)
	if err != nil {
		s.logger.Error("load environment status for egress policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load egress policy")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, buildEgressPolicyResponse(
		fromPGUUID(wsRow.ID),
		fromPGUUID(botRow.ID),
		policy,
		allowedHosts,
		blockedHosts,
		effectiveHosts,
		revision,
		ackedRevision,
		liveProxy,
		updatedAt,
	))
}

type updateEgressPolicyPayload struct {
	Policy           api.EgressPolicyMode `json:"policy"`
	AllowedHosts     json.RawMessage      `json:"allowedHosts"`
	BlockedHosts     json.RawMessage      `json:"blockedHosts"`
	ExpectedRevision json.RawMessage      `json:"expectedRevision"`
}

func isNullJSON(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (s *server) handleUpdateBotEgressPolicy(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, actorID, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}

	var rawReq updateEgressPolicyPayload
	if !decodeJSON(w, r, s.logger, &rawReq) {
		return
	}
	if len(rawReq.ExpectedRevision) == 0 {
		writeError(w, s.logger, http.StatusPreconditionRequired, "precondition_required", "expectedRevision is required")
		return
	}
	if isNullJSON(rawReq.ExpectedRevision) {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "expectedRevision must be a non-negative integer")
		return
	}
	var expectedRevision int64
	if err := json.Unmarshal(rawReq.ExpectedRevision, &expectedRevision); err != nil || expectedRevision < 0 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "expectedRevision must be a non-negative integer")
		return
	}
	if !rawReq.Policy.Valid() {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "policy must be allowlist or open")
		return
	}
	if len(rawReq.AllowedHosts) == 0 || isNullJSON(rawReq.AllowedHosts) {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "allowedHosts must be an array of host patterns")
		return
	}
	var allowedHosts []string
	if err := json.Unmarshal(rawReq.AllowedHosts, &allowedHosts); err != nil || allowedHosts == nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "allowedHosts must be an array of host patterns")
		return
	}

	var hasBlockedHosts bool
	var blockedHosts []string
	if len(rawReq.BlockedHosts) > 0 {
		if isNullJSON(rawReq.BlockedHosts) {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "blockedHosts must be an array of host patterns")
			return
		}
		if err := json.Unmarshal(rawReq.BlockedHosts, &blockedHosts); err != nil || blockedHosts == nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "blockedHosts must be an array of host patterns")
			return
		}
		hasBlockedHosts = true
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update egress policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save egress policy")
		return
	}
	defer rollbackTx(r.Context(), tx)
	txq := s.queries.WithTx(tx)

	wsID := fromPGUUID(wsRow.ID)
	botUUID := fromPGUUID(botRow.ID)
	res, err := egresspolicy.ApplyUpdateTx(r.Context(), txq, egresspolicy.UpdateInput{
		WorkspaceID:      wsID,
		BotID:            botUUID,
		ActorID:          actorID,
		Policy:           mitmproxy.EgressPolicy(rawReq.Policy),
		AllowedHosts:     allowedHosts,
		HasBlockedHosts:  hasBlockedHosts,
		BlockedHosts:     blockedHosts,
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		var valErr egresspolicy.ValidationError
		switch {
		case errors.Is(err, egresspolicy.ErrConflict):
			writeError(w, s.logger, http.StatusConflict, "policy_conflict", "egress policy changed concurrently; reload and retry")
		case errors.As(err, &valErr):
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", valErr.Message)
		default:
			s.logger.Error("apply egress policy update", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save egress policy")
		}
		return
	}

	// Prepare all fallible response metadata before committing so a committed
	// write is never masked by a post-commit query error.
	effectiveHosts, err := collectEffectiveSecretHostsWithQueries(r.Context(), txq, wsRow.ID, botRow.ID)
	if err != nil {
		s.logger.Error("collect secret hosts", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load egress policy")
		return
	}
	liveProxy, err := isBotEnvironmentLive(r.Context(), txq, wsRow.ID, botRow.ID)
	if err != nil {
		s.logger.Error("load environment status for egress policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save egress policy")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit egress policy update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save egress policy")
		return
	}

	ackedRevision := res.Rule.AcknowledgedRevision
	if liveProxy && ackedRevision < res.Rule.Revision {
		if acked, ok := s.awaitEgressPolicyAck(r.Context(), wsID, botUUID, res.Rule.Revision); ok {
			ackedRevision = acked
		}
	}

	updatedAt := res.Rule.UpdatedAt.Time.UTC()
	if updatedAt.IsZero() {
		updatedAt = botRow.UpdatedAt.Time.UTC()
	}
	resp := buildEgressPolicyResponse(
		wsID,
		botUUID,
		api.EgressPolicyMode(res.Rule.Policy),
		res.Rule.AllowedHosts,
		res.Rule.BlockedHosts,
		effectiveHosts,
		res.Rule.Revision,
		ackedRevision,
		liveProxy,
		updatedAt,
	)
	statusCode := http.StatusOK
	if resp.EnforcementStatus == api.EgressEnforcementStatusPending {
		statusCode = http.StatusAccepted
	}
	writeJSON(w, s.logger, statusCode, resp)
}

func (s *server) awaitEgressPolicyAck(ctx context.Context, wsID, botID uuid.UUID, revision int64) (int64, bool) {
	notifier, ok := s.turnQueue.(egressPolicyRevisionNotifier)
	if !ok {
		return 0, false
	}
	ackTimeout := s.egressAckTimeout
	if ackTimeout <= 0 {
		ackTimeout = 1500 * time.Millisecond
	}
	ackCtx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()

	ackedRev, err := notifier.PublishEgressPolicyRevision(ackCtx, wsID, botID, revision)
	if err != nil || ackedRev < revision {
		return 0, false
	}
	// Persist monotonic ack as well in case a lightweight test notifier replied without DB access.
	_ = s.queries.AcknowledgeEgressPolicyRevision(context.WithoutCancel(ctx), db.AcknowledgeEgressPolicyRevisionParams{
		WorkspaceID: toPGUUID(wsID),
		BotID:       toPGUUID(botID),
		Column3:     ackedRev,
	})
	return ackedRev, true
}

func (s *server) handleListBotEgressLogs(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, _, ok := s.loadWorkspaceBotForSecret(w, r, false)
	if !ok {
		return
	}

	q := r.URL.Query()
	actionFilter := strings.TrimSpace(q.Get("action"))
	if actionFilter != "" {
		act := api.EgressLogAction(actionFilter)
		if !act.Valid() {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid action filter")
			return
		}
	}

	limit := int32(defaultEgressLogLimit)
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, parseErr := strconv.ParseInt(rawLimit, 10, 32)
		if parseErr != nil || parsed < 1 || parsed > maxEgressLogLimit {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "limit must be between 1 and 200")
			return
		}
		limit = int32(parsed)
	}

	offset := int32(0)
	if rawOffset := strings.TrimSpace(q.Get("offset")); rawOffset != "" {
		parsed, parseErr := strconv.ParseInt(rawOffset, 10, 32)
		if parseErr != nil || parsed < 0 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "offset must be non-negative")
			return
		}
		offset = int32(parsed)
	}

	rows, err := s.queries.ListEgressLogsByWorkspaceAndBot(r.Context(), db.ListEgressLogsByWorkspaceAndBotParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		Column3:     actionFilter,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		s.logger.Error("list egress logs", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load egress logs")
		return
	}

	out := make([]api.EgressLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIEgressLogEntry(row))
	}
	writeJSON(w, s.logger, http.StatusOK, api.EgressLogListResponse{Logs: out})
}

func collectEffectiveSecretHostsWithQueries(ctx context.Context, q *db.Queries, workspacePGID, botPGID pgtype.UUID) ([]string, error) {
	botRows, err := q.ListBotSecretsByWorkspaceAndBot(ctx, db.ListBotSecretsByWorkspaceAndBotParams{
		WorkspaceID: workspacePGID,
		BotID:       botPGID,
	})
	if err != nil {
		return nil, err
	}
	wsRows, err := q.ListWorkspaceSecretsByWorkspace(ctx, workspacePGID)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, row := range botRows {
		addHostPattern(seen, row.DomainMatch, row.MatchSubdomains)
	}
	for _, row := range wsRows {
		addHostPattern(seen, row.DomainMatch, row.MatchSubdomains)
	}

	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts, nil
}

func addHostPattern(dst map[string]bool, domain string, matchSubdomains bool) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return
	}
	dst[d] = true
	if matchSubdomains {
		dst["*."+d] = true
	}
}

func toAPIBotSecretMetadata(row db.BotSecret) api.SecretMetadata {
	botID := openapi_types.UUID(fromPGUUID(row.BotID))
	var lastUsedAt *time.Time
	if row.LastUsedAt.Valid {
		t := row.LastUsedAt.Time.UTC()
		lastUsedAt = &t
	}
	return api.SecretMetadata{
		Id:              fromPGUUID(row.ID),
		WorkspaceId:     fromPGUUID(row.WorkspaceID),
		BotId:           &botID,
		Scope:           api.SecretScopeBot,
		Name:            row.Name,
		DomainMatch:     row.DomainMatch,
		MatchSubdomains: row.MatchSubdomains,
		PathPrefix:      row.PathPrefix,
		AuthScheme:      api.SecretAuthScheme(row.AuthScheme),
		HeaderName:      row.HeaderName,
		Username:        row.Username,
		MaskedPreview:   row.MaskedPreview,
		UsedCount:       row.UsedCount,
		LastUsedAt:      lastUsedAt,
		CreatedAt:       row.CreatedAt.Time.UTC(),
		UpdatedAt:       row.UpdatedAt.Time.UTC(),
	}
}

func toAPIWorkspaceSecretMetadata(row db.WorkspaceSecret) api.SecretMetadata {
	var lastUsedAt *time.Time
	if row.LastUsedAt.Valid {
		t := row.LastUsedAt.Time.UTC()
		lastUsedAt = &t
	}
	return api.SecretMetadata{
		Id:              fromPGUUID(row.ID),
		WorkspaceId:     fromPGUUID(row.WorkspaceID),
		BotId:           nil,
		Scope:           api.SecretScopeWorkspace,
		Name:            row.Name,
		DomainMatch:     row.DomainMatch,
		MatchSubdomains: row.MatchSubdomains,
		PathPrefix:      row.PathPrefix,
		AuthScheme:      api.SecretAuthScheme(row.AuthScheme),
		HeaderName:      row.HeaderName,
		Username:        row.Username,
		MaskedPreview:   row.MaskedPreview,
		UsedCount:       row.UsedCount,
		LastUsedAt:      lastUsedAt,
		CreatedAt:       row.CreatedAt.Time.UTC(),
		UpdatedAt:       row.UpdatedAt.Time.UTC(),
	}
}

func toAPIEgressLogEntry(row db.EgressLog) api.EgressLogEntry {
	var secretID *openapi_types.UUID
	if row.SecretID.Valid {
		id := openapi_types.UUID(fromPGUUID(row.SecretID))
		secretID = &id
	}
	return api.EgressLogEntry{
		Id:               fromPGUUID(row.ID),
		BotId:            fromPGUUID(row.BotID),
		WorkspaceId:      fromPGUUID(row.WorkspaceID),
		Method:           row.Method,
		Host:             row.Host,
		Port:             row.Port,
		Path:             row.Path,
		Action:           api.EgressLogAction(row.Action),
		StatusCode:       int(row.StatusCode),
		SecretId:         secretID,
		SecretScope:      row.SecretScope,
		SecretName:       row.SecretName,
		BytesTransferred: row.BytesTransferred,
		LatencyMs:        int(row.LatencyMs),
		CreatedAt:        row.CreatedAt.Time.UTC(),
	}
}

// BotEgressProxyDeps configures a database-backed per-bot selective MITM egress proxy handler.
type BotEgressProxyDeps struct {
	Pool               *pgxpool.Pool
	KeyRing            *secrets.KeyRing
	WorkspaceID        uuid.UUID
	BotID              uuid.UUID
	CA                 *mitmproxy.BotCA
	UpstreamRootCAs    *x509.CertPool
	DialContext        func(ctx context.Context, network, addr string) (net.Conn, error)
	PublicResolver     netguard.Resolver
	Logger             *slog.Logger
	Now                func() time.Time
	PolicyFreshnessMax time.Duration
	OnPolicyAdopted    func(context.Context, int64) error
	// Notify publishes ids after a new host card commits. Nil leaves the row
	// and the client reloads the list (issue #17).
	Notify notify.Signal
}

// NewBotEgressProxyHandler builds a dynamic per-bot CONNECT egress handler backed by
// Postgres egress_rules, envelope-encrypted bot/workspace secrets, and egress_log persistence.
func NewBotEgressProxyHandler(deps BotEgressProxyDeps) (*mitmproxy.Handler, error) {
	if deps.Pool == nil {
		return nil, errors.New("database pool is required")
	}
	if deps.KeyRing == nil {
		return nil, errors.New("secrets key ring is required")
	}
	if deps.WorkspaceID == uuid.Nil || deps.BotID == uuid.Nil {
		return nil, errors.New("workspaceID and botID are required")
	}
	if deps.CA == nil {
		return nil, errors.New("bot CA is required")
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	queries := db.New(deps.Pool)
	wsPG := toPGUUID(deps.WorkspaceID)
	botPG := toPGUUID(deps.BotID)

	return mitmproxy.NewHandler(mitmproxy.Config{
		CA:                 deps.CA,
		UpstreamRootCAs:    deps.UpstreamRootCAs,
		DialContext:        deps.DialContext,
		PublicResolver:     deps.PublicResolver,
		Logger:             logger,
		Now:                nowFn,
		PolicyFreshnessMax: deps.PolicyFreshnessMax,
		OnPolicyAdopted:    deps.OnPolicyAdopted,
		DynamicPolicy: func(ctx context.Context) (mitmproxy.PolicySnapshot, error) {
			snap := mitmproxy.PolicySnapshot{
				Policy: mitmproxy.PolicyAllowlist,
			}

			egressRow, err := queries.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{
				WorkspaceID: wsPG,
				BotID:       botPG,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return mitmproxy.PolicySnapshot{}, fmt.Errorf("load egress rule: %w", err)
			}
			allowedSet := make(map[string]bool)
			if err == nil {
				snap.Revision = egressRow.Revision
				snap.GitLabAllowedHosts = append([]string(nil), egressRow.AllowedHosts...)
				if egressRow.Policy == string(mitmproxy.PolicyOpen) {
					snap.Policy = mitmproxy.PolicyOpen
				}
				for _, host := range egressRow.AllowedHosts {
					allowedSet[strings.ToLower(strings.TrimSpace(host))] = true
				}
				if len(egressRow.BlockedHosts) > 0 {
					snap.BlockedHosts = append([]string(nil), egressRow.BlockedHosts...)
				}
			}

			botRows, err := queries.ListBotSecretsByWorkspaceAndBot(ctx, db.ListBotSecretsByWorkspaceAndBotParams{
				WorkspaceID: wsPG,
				BotID:       botPG,
			})
			if err != nil {
				return mitmproxy.PolicySnapshot{}, fmt.Errorf("load bot secrets: %w", err)
			}
			wsRows, err := queries.ListWorkspaceSecretsByWorkspace(ctx, wsPG)
			if err != nil {
				return mitmproxy.PolicySnapshot{}, fmt.Errorf("load workspace secrets: %w", err)
			}
			leaseRows, err := queries.ListActiveSecretLeasesForBot(ctx, db.ListActiveSecretLeasesForBotParams{
				WorkspaceID: wsPG,
				BotID:       botPG,
				AsOf:        pgtype.Timestamptz{Time: nowFn(), Valid: true},
			})
			if err != nil {
				return mitmproxy.PolicySnapshot{}, fmt.Errorf("load active secret leases: %w", err)
			}

			access, err := loadGitLabEgressAccess(ctx, queries, wsPG, botPG)
			if err != nil {
				return mitmproxy.PolicySnapshot{}, fmt.Errorf("load GitLab access: %w", err)
			}

			rules := make([]mitmproxy.SecretRule, 0, len(botRows)+len(wsRows)+len(leaseRows))
			for _, row := range leaseRows {
				rule, decErr := decryptSecretRule(deps.KeyRing, deps.WorkspaceID, fromPGUUID(row.ID), "lease", row.PlaceholderEnv, row.DomainMatch, false, "", row.HeaderName, row.AuthScheme, row.Username, row.KekID, row.WrappedDek, row.EncryptedValue)
				if decErr != nil {
					return mitmproxy.PolicySnapshot{}, decErr
				}
				rule.AdapterID = row.AdapterID
				rule.Port = row.Port
				rule.PlaceholderToken = row.PlaceholderValue
				if row.ExpiresAt.Valid {
					rule.ExpiresAt = row.ExpiresAt.Time.UTC()
				}
				if access.bind(&rule) {
					rules = append(rules, rule)
				}
			}

			for _, row := range botRows {
				rule, decErr := decryptSecretRule(deps.KeyRing, deps.WorkspaceID, fromPGUUID(row.ID), string(api.SecretScopeBot), row.Name, row.DomainMatch, row.MatchSubdomains, row.PathPrefix, row.HeaderName, row.AuthScheme, row.Username, row.KekID, row.WrappedDek, row.EncryptedValue)
				if decErr != nil {
					return mitmproxy.PolicySnapshot{}, decErr
				}
				rule.AdapterID = row.AdapterID
				if row.AdapterID == "" {
					addHostPattern(allowedSet, row.DomainMatch, row.MatchSubdomains)
				}
				if access.bind(&rule) {
					rules = append(rules, rule)
				}
			}

			for _, row := range wsRows {
				if access.blockedSecrets[row.ID] {
					continue
				}
				rule, decErr := decryptSecretRule(deps.KeyRing, deps.WorkspaceID, fromPGUUID(row.ID), string(api.SecretScopeWorkspace), row.Name, row.DomainMatch, row.MatchSubdomains, row.PathPrefix, row.HeaderName, row.AuthScheme, row.Username, row.KekID, row.WrappedDek, row.EncryptedValue)
				if decErr != nil {
					return mitmproxy.PolicySnapshot{}, decErr
				}
				rule.AdapterID = row.AdapterID
				if row.AdapterID == "" {
					addHostPattern(allowedSet, row.DomainMatch, row.MatchSubdomains)
				}
				if access.bind(&rule) {
					rules = append(rules, rule)
				}
			}

			allowedList := make([]string, 0, len(allowedSet))
			for host := range allowedSet {
				allowedList = append(allowedList, host)
			}
			snap.AllowedHosts = allowedList
			snap.Rules = rules
			for host := range access.publicHosts {
				snap.PublicHosts = append(snap.PublicHosts, host)
			}
			return snap, nil
		},
		OnAudit: func(ev mitmproxy.AuditEvent) {
			auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var secretPGID pgtype.UUID
			var parsedSecretID uuid.UUID
			if ev.SecretID != "" {
				if id, parseErr := uuid.Parse(ev.SecretID); parseErr == nil {
					parsedSecretID = id
					secretPGID = toPGUUID(id)
				}
			}

			bytesCount := max(ev.Bytes, 0)
			latency := int32(0)
			if ev.LatencyMs > math.MaxInt32 {
				latency = math.MaxInt32
			} else if ev.LatencyMs > 0 {
				latency = int32(ev.LatencyMs) // #nosec G115 -- clamped to [0, math.MaxInt32]
			}
			statusCode := int32(0)
			if ev.Status > math.MaxInt32 {
				statusCode = math.MaxInt32
			} else if ev.Status > 0 {
				statusCode = int32(ev.Status) // #nosec G115 -- HTTP status clamped to [0, math.MaxInt32]
			}

			if _, logErr := queries.CreateEgressLog(auditCtx, db.CreateEgressLogParams{
				WorkspaceID:      wsPG,
				BotID:            botPG,
				Method:           ev.Method,
				Host:             ev.Host,
				Port:             ev.Port,
				Path:             ev.Path,
				Action:           string(ev.Action),
				StatusCode:       statusCode,
				SecretID:         secretPGID,
				SecretScope:      ev.SecretScope,
				SecretName:       ev.SecretName,
				BytesTransferred: bytesCount,
				LatencyMs:        latency,
			}); logErr != nil {
				logger.Error("persist egress audit log failed", slog.Any("error", logErr))
			}

			// Only a real host denial on 443 opens a card. A forbidden port uses the
			// same audit action and must not ask a human to allow the host (ADR-0015).
			if ev.Action == mitmproxy.ActionBlockedHost && ev.Port == "443" {
				if noteErr := approvals.NoteBlockedHost(auditCtx, deps.Pool, deps.WorkspaceID, deps.BotID, ev.Host, deps.Notify); noteErr != nil {
					logger.Error("record blocked host approval failed", slog.Any("error", noteErr))
				}
			}

			if ev.Action == mitmproxy.ActionMITMTokenAdded && parsedSecretID != uuid.Nil {
				switch ev.SecretScope {
				case string(api.SecretScopeBot):
					if useErr := queries.RecordBotSecretUsage(auditCtx, db.RecordBotSecretUsageParams{
						WorkspaceID: wsPG,
						ID:          secretPGID,
					}); useErr != nil {
						logger.Error("increment bot secret usage failed", slog.Any("error", useErr))
					}
				case string(api.SecretScopeWorkspace):
					if useErr := queries.RecordWorkspaceSecretUsage(auditCtx, db.RecordWorkspaceSecretUsageParams{
						WorkspaceID: wsPG,
						ID:          secretPGID,
					}); useErr != nil {
						logger.Error("increment workspace secret usage failed", slog.Any("error", useErr))
					}
				}
			}
		},
	})
}

func decryptSecretRule(
	ring *secrets.KeyRing,
	workspaceID, secretID uuid.UUID,
	scope, name, domainMatch string,
	matchSubdomains bool,
	pathPrefix, headerName, authScheme, username, kekID string,
	wrappedDEK, encryptedValue []byte,
) (mitmproxy.SecretRule, error) {
	plain, err := ring.Decrypt(secrets.EnvelopeCiphertext{
		KeyID:      kekID,
		WrappedDEK: wrappedDEK,
		Ciphertext: encryptedValue,
	}, secretAAD(workspaceID, scope, secretID))
	if err != nil {
		return mitmproxy.SecretRule{}, fmt.Errorf("decrypt %s secret %s: %w", scope, secretID, err)
	}
	val := string(plain)
	clear(plain)

	return mitmproxy.SecretRule{
		ID:              secretID.String(),
		Scope:           scope,
		Name:            name,
		DomainMatch:     domainMatch,
		MatchSubdomains: matchSubdomains,
		PathPrefix:      pathPrefix,
		HeaderName:      headerName,
		Scheme:          mapDBAuthScheme(authScheme),
		Username:        username,
		Value:           val,
	}, nil
}

func mapDBAuthScheme(scheme string) mitmproxy.AuthScheme {
	switch scheme {
	case string(api.Header):
		return mitmproxy.SchemeRaw
	case string(api.Basic):
		return mitmproxy.SchemeBasic
	default:
		return mitmproxy.SchemeBearer
	}
}
