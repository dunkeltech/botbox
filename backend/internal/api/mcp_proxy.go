package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	defaultMCPLogLimit = 50
	maxMCPLogLimit     = 200
)

func (s *server) mcpUpstream() *mcpproxy.UpstreamClient {
	if s.mcpUpstreamClient != nil {
		return s.mcpUpstreamClient
	}
	return mcpproxy.NewUpstreamClient()
}

func (s *server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	servers, err := s.queries.ListMCPServers(r.Context(), ws.ID)
	if err != nil {
		s.logger.Error("list mcp servers", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list MCP servers")
		return
	}
	allTools, err := s.queries.ListWorkspaceMCPServerTools(r.Context(), ws.ID)
	if err != nil {
		s.logger.Error("list mcp tools", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list MCP tools")
		return
	}
	toolsByServer := groupMCPToolsByServer(allTools)
	out := apigen.MCPServerList{Servers: make([]apigen.MCPServer, 0, len(servers))}
	for _, srv := range servers {
		out.Servers = append(out.Servers, toAPIMCPServer(srv, toolsByServer[fromPGUUID(srv.ID)]))
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleCreateMCPServer(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMCPServers)
	if !ok {
		return
	}
	var req apigen.CreateMCPServerRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) == 0 || len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "name must be 1..100 characters")
		return
	}
	slug, okSlug := mcpproxy.ValidateSlug(req.Slug)
	if !okSlug {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "slug must be 2..64 lowercase alphanumeric or '-' characters")
		return
	}
	endpointURL, _, urlErr := mcpproxy.ValidateEndpointURL(req.EndpointUrl)
	if urlErr != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", urlErr.Error())
		return
	}
	headerName, username, secretVal, authErr := validateMCPAuthInput(req.AuthScheme, req.AuthHeaderName, req.AuthUsername, req.SecretValue, true)
	if authErr != "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", authErr)
		return
	}
	toolDefs, toolErr := parseMCPToolInputs(req.Tools)
	if toolErr != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", toolErr.Error())
		return
	}

	wsID := fromPGUUID(ws.ID)
	serverID := uuid.New()
	var kekID, maskedPreview string
	var wrappedDEK, ciphertext []byte
	if req.AuthScheme != apigen.MCPAuthSchemeNone {
		env, encErr := s.keyRing.Encrypt([]byte(secretVal), secrets.SecretAAD(wsID, "mcp_server", serverID))
		if encErr != nil {
			s.logger.Error("encrypt mcp server secret", "err", encErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to encrypt MCP secret")
			return
		}
		kekID = env.KeyID
		wrappedDEK = env.WrappedDEK
		ciphertext = env.Ciphertext
		maskedPreview = secrets.MaskedPreview(secretVal)
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin create mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to register MCP server")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	row, err := q.CreateMCPServer(r.Context(), db.CreateMCPServerParams{
		ID:             toPGUUID(serverID),
		WorkspaceID:    ws.ID,
		Slug:           slug,
		Name:           name,
		EndpointUrl:    endpointURL,
		AuthScheme:     string(req.AuthScheme),
		AuthHeaderName: headerName,
		AuthUsername:   username,
		KekID:          kekID,
		WrappedDek:     wrappedDEK,
		EncryptedValue: ciphertext,
		MaskedPreview:  maskedPreview,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "mcp.server_conflict", "an MCP server with this slug already exists")
			return
		}
		s.logger.Error("create mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to register MCP server")
		return
	}

	storedTools := make([]db.McpServerTool, 0, len(toolDefs))
	for _, def := range toolDefs {
		tRow, tErr := q.UpsertMCPServerTool(r.Context(), db.UpsertMCPServerToolParams{
			ServerID:    row.ID,
			WorkspaceID: ws.ID,
			ToolName:    def.ToolName,
			Description: def.Description,
			InputSchema: def.InputSchema,
		})
		if tErr != nil {
			s.logger.Error("insert mcp server tool", "err", tErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to store MCP tools")
			return
		}
		storedTools = append(storedTools, tRow)
	}

	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "mcp_server.created", "mcp_server", &serverID, map[string]any{
		"slug": slug,
		"name": name,
	})
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit create mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to register MCP server")
		return
	}
	writeJSON(w, s.logger, http.StatusCreated, toAPIMCPServer(row, storedTools))
}

func (s *server) handleUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMCPServers)
	if !ok {
		return
	}
	serverID, err := uuid.Parse(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid MCP server identifier")
		return
	}
	var req apigen.UpdateMCPServerRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	existing, err := s.queries.GetMCPServer(r.Context(), db.GetMCPServerParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(serverID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "mcp.server_not_found", "MCP server not found")
		return
	}
	if err != nil {
		s.logger.Error("load mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load MCP server")
		return
	}

	name := existing.Name
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if len(trimmed) == 0 || len(trimmed) > 100 || strings.ContainsAny(trimmed, "\r\n\x00") {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "name must be 1..100 characters")
			return
		}
		name = trimmed
	}
	endpointURL := existing.EndpointUrl
	if req.EndpointUrl != nil {
		canon, _, urlErr := mcpproxy.ValidateEndpointURL(*req.EndpointUrl)
		if urlErr != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", urlErr.Error())
			return
		}
		endpointURL = canon
	}

	scheme := apigen.MCPAuthScheme(existing.AuthScheme)
	if req.AuthScheme != nil {
		scheme = *req.AuthScheme
	}
	hdrPtr := req.AuthHeaderName
	if hdrPtr == nil && existing.AuthHeaderName != "" {
		h := existing.AuthHeaderName
		hdrPtr = &h
	}
	userPtr := req.AuthUsername
	if userPtr == nil && existing.AuthUsername != "" {
		u := existing.AuthUsername
		userPtr = &u
	}
	requireSecret := req.AuthScheme != nil && *req.AuthScheme != apigen.MCPAuthSchemeNone && len(existing.EncryptedValue) == 0
	headerName, username, secretVal, authErr := validateMCPAuthInput(scheme, hdrPtr, userPtr, req.SecretValue, requireSecret)
	if authErr != "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", authErr)
		return
	}

	toolDefs, toolErr := parseMCPToolInputs(req.Tools)
	if toolErr != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", toolErr.Error())
		return
	}

	wsID := fromPGUUID(ws.ID)
	kekID := existing.KekID
	wrappedDEK := existing.WrappedDek
	ciphertext := existing.EncryptedValue
	maskedPreview := existing.MaskedPreview
	if scheme == apigen.MCPAuthSchemeNone {
		kekID = ""
		wrappedDEK = nil
		ciphertext = nil
		maskedPreview = ""
	} else if secretVal != "" {
		env, encErr := s.keyRing.Encrypt([]byte(secretVal), secrets.SecretAAD(wsID, "mcp_server", serverID))
		if encErr != nil {
			s.logger.Error("re-encrypt mcp server secret", "err", encErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to encrypt MCP secret")
			return
		}
		kekID = env.KeyID
		wrappedDEK = env.WrappedDEK
		ciphertext = env.Ciphertext
		maskedPreview = secrets.MaskedPreview(secretVal)
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update MCP server")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	updated, err := q.UpdateMCPServer(r.Context(), db.UpdateMCPServerParams{
		WorkspaceID:    ws.ID,
		ID:             existing.ID,
		Name:           name,
		EndpointUrl:    endpointURL,
		AuthScheme:     string(scheme),
		AuthHeaderName: headerName,
		AuthUsername:   username,
		KekID:          kekID,
		WrappedDek:     wrappedDEK,
		EncryptedValue: ciphertext,
		MaskedPreview:  maskedPreview,
	})
	if err != nil {
		s.logger.Error("update mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update MCP server")
		return
	}
	if req.Tools != nil {
		if err = q.DeleteMCPServerTools(r.Context(), db.DeleteMCPServerToolsParams{WorkspaceID: ws.ID, ServerID: existing.ID}); err != nil {
			s.logger.Error("clear mcp tools", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update MCP tools")
			return
		}
		for _, def := range toolDefs {
			if _, tErr := q.UpsertMCPServerTool(r.Context(), db.UpsertMCPServerToolParams{
				ServerID:    existing.ID,
				WorkspaceID: ws.ID,
				ToolName:    def.ToolName,
				Description: def.Description,
				InputSchema: def.InputSchema,
			}); tErr != nil {
				s.logger.Error("upsert mcp tool", "err", tErr)
				writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update MCP tools")
				return
			}
		}
	}
	tools, err := q.ListMCPServerTools(r.Context(), db.ListMCPServerToolsParams{WorkspaceID: ws.ID, ServerID: existing.ID})
	if err != nil {
		s.logger.Error("list mcp tools after update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load MCP tools")
		return
	}
	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "mcp_server.updated", "mcp_server", &serverID, map[string]any{
		"slug": updated.Slug,
	})
	if err = q.DeleteBotSessionsByWorkspace(r.Context(), ws.ID); err != nil {
		s.logger.Error("reset workspace cli sessions after mcp update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update MCP server")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit update mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update MCP server")
		return
	}
	writeJSON(w, s.logger, http.StatusOK, toAPIMCPServer(updated, tools))
}

func (s *server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMCPServers)
	if !ok {
		return
	}
	serverID, err := uuid.Parse(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid MCP server identifier")
		return
	}
	n, err := s.queries.DeleteMCPServer(r.Context(), db.DeleteMCPServerParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(serverID),
	})
	if err != nil {
		s.logger.Error("delete mcp server", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete MCP server")
		return
	}
	if n == 0 {
		writeError(w, s.logger, http.StatusNotFound, "mcp.server_not_found", "MCP server not found")
		return
	}
	wsID := fromPGUUID(ws.ID)
	_ = s.queries.DeleteBotSessionsByWorkspace(r.Context(), ws.ID)
	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "mcp_server.deleted", "mcp_server", &serverID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleSyncMCPServer(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMCPServers)
	if !ok {
		return
	}
	serverID, err := uuid.Parse(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid MCP server identifier")
		return
	}
	srv, err := s.queries.GetMCPServer(r.Context(), db.GetMCPServerParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(serverID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "mcp.server_not_found", "MCP server not found")
		return
	}
	if err != nil {
		s.logger.Error("load mcp server for sync", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load MCP server")
		return
	}

	wsID := fromPGUUID(ws.ID)
	var headerName, headerValue, secretRaw string
	if srv.AuthScheme != "" && srv.AuthScheme != "none" {
		plain, decErr := s.keyRing.Decrypt(secrets.EnvelopeCiphertext{
			KeyID:      srv.KekID,
			WrappedDEK: srv.WrappedDek,
			Ciphertext: srv.EncryptedValue,
		}, secrets.SecretAAD(wsID, "mcp_server", serverID))
		if decErr != nil {
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to decrypt MCP credential")
			return
		}
		defer clear(plain)
		secretRaw = string(plain)
		headerName, headerValue, decErr = mcpproxy.BuildAuthHeader(srv.AuthScheme, srv.AuthHeaderName, srv.AuthUsername, secretRaw)
		if decErr != nil {
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to prepare MCP credential")
			return
		}
	}

	tools, syncErr := s.mcpUpstream().DiscoverTools(r.Context(), srv.EndpointUrl, headerName, headerValue, secretRaw)
	if syncErr != nil {
		errMsg := syncErr.Error()
		if len(errMsg) > 250 {
			errMsg = errMsg[:250]
		}
		_, _ = s.queries.UpdateMCPServerSyncStatus(r.Context(), db.UpdateMCPServerSyncStatusParams{
			WorkspaceID:   ws.ID,
			ID:            srv.ID,
			LastSyncedAt:  srv.LastSyncedAt,
			LastSyncError: errMsg,
		})
		writeError(w, s.logger, http.StatusBadGateway, "mcp.sync_failed", "failed to sync tools from upstream MCP server")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin sync mcp tools", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save discovered tools")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	if err = q.DeleteMCPServerTools(r.Context(), db.DeleteMCPServerToolsParams{WorkspaceID: ws.ID, ServerID: srv.ID}); err != nil {
		s.logger.Error("replace mcp tools", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save discovered tools")
		return
	}
	stored := make([]db.McpServerTool, 0, len(tools))
	for _, t := range tools {
		row, tErr := q.UpsertMCPServerTool(r.Context(), db.UpsertMCPServerToolParams{
			ServerID:    srv.ID,
			WorkspaceID: ws.ID,
			ToolName:    t.ToolName,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
		if tErr != nil {
			s.logger.Error("upsert discovered mcp tool", "err", tErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save discovered tools")
			return
		}
		stored = append(stored, row)
	}
	updatedSrv, err := q.UpdateMCPServerSyncStatus(r.Context(), db.UpdateMCPServerSyncStatusParams{
		WorkspaceID:   ws.ID,
		ID:            srv.ID,
		LastSyncedAt:  toPGTimestamptz(s.now()),
		LastSyncError: "",
	})
	if err != nil {
		s.logger.Error("update mcp sync status", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save discovered tools")
		return
	}
	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "mcp_server.synced", "mcp_server", &serverID, map[string]any{
		"tool_count": len(stored),
	})
	if err = q.DeleteBotSessionsByWorkspace(r.Context(), ws.ID); err != nil {
		s.logger.Error("reset workspace cli sessions after mcp sync", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save discovered tools")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit sync mcp tools", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save discovered tools")
		return
	}
	writeJSON(w, s.logger, http.StatusOK, toAPIMCPServer(updatedSrv, stored))
}

func (s *server) handleGetBotMCP(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, _, ok := s.loadWorkspaceBotForSecret(w, r, false)
	if !ok {
		return
	}
	servers, err := s.queries.ListMCPServers(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list workspace mcp servers", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot MCP configuration")
		return
	}
	allTools, err := s.queries.ListWorkspaceMCPServerTools(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list workspace mcp tools", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot MCP configuration")
		return
	}
	assignments, err := s.queries.ListBotMCPAssignments(r.Context(), db.ListBotMCPAssignmentsParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
	})
	if err != nil {
		s.logger.Error("list bot mcp assignments", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot MCP configuration")
		return
	}
	toolPolicies, err := s.queries.ListBotMCPToolPolicies(r.Context(), db.ListBotMCPToolPoliciesParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
	})
	if err != nil {
		s.logger.Error("list bot mcp tool policies", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot MCP configuration")
		return
	}
	liveProxy, err := isBotEnvironmentLive(r.Context(), s.queries, wsRow.ID, botRow.ID)
	if err != nil {
		s.logger.Error("check environment status for mcp", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot MCP configuration")
		return
	}

	toolsByServer := groupMCPToolsByServer(allTools)
	assignByServer := make(map[uuid.UUID]db.BotMcpAssignment, len(assignments))
	for _, a := range assignments {
		assignByServer[fromPGUUID(a.ServerID)] = a
	}
	policiesByServer := make(map[uuid.UUID]map[string]apigen.MCPToolPolicyMode)
	for _, p := range toolPolicies {
		sid := fromPGUUID(p.ServerID)
		if policiesByServer[sid] == nil {
			policiesByServer[sid] = make(map[string]apigen.MCPToolPolicyMode)
		}
		policiesByServer[sid][p.ToolName] = apigen.MCPToolPolicyMode(p.Policy)
	}

	out := apigen.BotMCPConfigResponse{
		WorkspaceId: fromPGUUID(wsRow.ID),
		BotId:       fromPGUUID(botRow.ID),
		Servers:     make([]apigen.BotMCPServerPolicy, 0, len(servers)),
	}
	for _, srv := range servers {
		sid := fromPGUUID(srv.ID)
		var assign *db.BotMcpAssignment
		if a, exists := assignByServer[sid]; exists {
			copyA := a
			assign = &copyA
		}
		out.Servers = append(out.Servers, toAPIBotMCPServerPolicy(srv, assign, toolsByServer[sid], policiesByServer[sid], liveProxy))
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleUpdateBotMCPServer(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, actorID, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}
	serverID, err := uuid.Parse(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid MCP server identifier")
		return
	}
	var req apigen.UpdateBotMCPServerPolicyRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	defaultPolicy := apigen.Deny
	if req.DefaultPolicy != nil {
		if !req.DefaultPolicy.Valid() {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "defaultPolicy must be allow, ask, or deny")
			return
		}
		defaultPolicy = *req.DefaultPolicy
	}

	cleanedPolicies := make(map[string]apigen.MCPToolPolicyMode)
	if req.ToolPolicies != nil {
		for rawName, mode := range *req.ToolPolicies {
			toolName, validName := mcpproxy.ValidateToolName(rawName)
			if !validName || !mode.Valid() {
				writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid tool policy entry")
				return
			}
			cleanedPolicies[toolName] = mode
		}
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update bot mcp policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	if err = q.LockBotMCPPolicy(r.Context(), db.LockBotMCPPolicyParams{
		Column1: wsRow.ID,
		Column2: botRow.ID,
	}); err != nil {
		s.logger.Error("lock bot mcp policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}

	srv, err := q.GetMCPServer(r.Context(), db.GetMCPServerParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(serverID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "mcp.server_not_found", "MCP server not found")
		return
	}
	if err != nil {
		s.logger.Error("load mcp server for bot policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}

	currentRev := int64(0)
	existingAssign, err := q.GetBotMCPAssignment(r.Context(), db.GetBotMCPAssignmentParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		ServerID:    srv.ID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("load bot mcp assignment", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}
	if err == nil {
		currentRev = existingAssign.Revision
		if req.DefaultPolicy == nil {
			defaultPolicy = apigen.MCPToolPolicyMode(existingAssign.DefaultPolicy)
		}
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != currentRev {
		writeError(w, s.logger, http.StatusConflict, "policy_conflict", "MCP policy changed concurrently; reload and retry")
		return
	}
	enabled := req.Enabled
	if !enabled && currentRev == 0 && hasPermittedMCPTool(defaultPolicy, cleanedPolicies) {
		// Initial tool grant enables the bot server assignment so configured tools are active right away.
		enabled = true
	}

	nextRev := currentRev + 1
	updatedAssign, err := q.UpsertBotMCPAssignment(r.Context(), db.UpsertBotMCPAssignmentParams{
		WorkspaceID:   wsRow.ID,
		BotID:         botRow.ID,
		ServerID:      srv.ID,
		Enabled:       enabled,
		DefaultPolicy: string(defaultPolicy),
		Revision:      nextRev,
	})
	if err != nil {
		s.logger.Error("upsert bot mcp assignment", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}

	if req.ToolPolicies != nil {
		if err = q.DeleteBotMCPToolPoliciesForServer(r.Context(), db.DeleteBotMCPToolPoliciesForServerParams{
			WorkspaceID: wsRow.ID,
			BotID:       botRow.ID,
			ServerID:    srv.ID,
		}); err != nil {
			s.logger.Error("clear bot mcp tool policies", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
			return
		}
		for toolName, mode := range cleanedPolicies {
			if _, pErr := q.UpsertBotMCPToolPolicy(r.Context(), db.UpsertBotMCPToolPolicyParams{
				WorkspaceID: wsRow.ID,
				BotID:       botRow.ID,
				ServerID:    srv.ID,
				ToolName:    toolName,
				Policy:      string(mode),
			}); pErr != nil {
				s.logger.Error("upsert bot mcp tool policy", "err", pErr)
				writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
				return
			}
		}
	}

	storedPolicies, err := q.ListBotMCPToolPoliciesForServer(r.Context(), db.ListBotMCPToolPoliciesForServerParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		ServerID:    srv.ID,
	})
	if err != nil {
		s.logger.Error("reload bot mcp tool policies", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}
	tools, err := q.ListMCPServerTools(r.Context(), db.ListMCPServerToolsParams{
		WorkspaceID: wsRow.ID,
		ServerID:    srv.ID,
	})
	if err != nil {
		s.logger.Error("reload mcp server tools", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}
	liveProxy, err := isBotEnvironmentLive(r.Context(), q, wsRow.ID, botRow.ID)
	if err != nil {
		s.logger.Error("check environment status for mcp update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}

	wsID := fromPGUUID(wsRow.ID)
	botID := fromPGUUID(botRow.ID)
	_ = recordAudit(r.Context(), q, &wsID, &actorID, "bot.mcp_policy_updated", "bot", &botID, map[string]any{
		"server_slug": srv.Slug,
		"enabled":     updatedAssign.Enabled,
		"revision":    nextRev,
	})
	if err = q.DeleteBotSessionsByBot(r.Context(), db.DeleteBotSessionsByBotParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
	}); err != nil {
		s.logger.Error("reset bot cli sessions after mcp policy update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit bot mcp policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save MCP policy")
		return
	}

	s.invalidateMCPPolicy(r.Context(), wsID, botID)

	policyMap := make(map[string]apigen.MCPToolPolicyMode, len(storedPolicies))
	for _, p := range storedPolicies {
		policyMap[p.ToolName] = apigen.MCPToolPolicyMode(p.Policy)
	}
	writeJSON(w, s.logger, http.StatusOK, toAPIBotMCPServerPolicy(srv, &updatedAssign, tools, policyMap, liveProxy))
}

func (s *server) handleListBotMCPLogs(w http.ResponseWriter, r *http.Request) {
	wsRow, botRow, _, ok := s.loadWorkspaceBotForSecret(w, r, false)
	if !ok {
		return
	}
	actionFilter := strings.TrimSpace(r.URL.Query().Get("action"))
	if actionFilter != "" && !apigen.MCPCallLogAction(actionFilter).Valid() {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid MCP log action filter")
		return
	}
	limit := defaultMCPLogLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, convErr := strconv.Atoi(rawLimit)
		if convErr != nil || parsed < 1 || parsed > maxMCPLogLimit {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	offset := 0
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, convErr := strconv.Atoi(rawOffset)
		if convErr != nil || parsed < 0 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "offset must be non-negative")
			return
		}
		offset = parsed
	}

	rows, err := s.queries.ListBotMCPCallLogs(r.Context(), db.ListBotMCPCallLogsParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		Column3:     actionFilter,
		Limit:       int32(limit),  // #nosec G115 -- validated 1..200
		Offset:      int32(offset), // #nosec G115 -- validated >= 0
	})
	if err != nil {
		s.logger.Error("list bot mcp logs", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load MCP logs")
		return
	}

	out := apigen.MCPCallLogListResponse{Logs: make([]apigen.MCPCallLogEntry, 0, len(rows))}
	for _, row := range rows {
		var srvPtr *openapi_types.UUID
		if row.ServerID.Valid {
			id := fromPGUUID(row.ServerID)
			srvPtr = &id
		}
		out.Logs = append(out.Logs, apigen.MCPCallLogEntry{
			Id:         fromPGUUID(row.ID),
			BotId:      fromPGUUID(row.BotID),
			ServerId:   srvPtr,
			ServerSlug: row.ServerSlug,
			ToolName:   row.ToolName,
			Action:     apigen.MCPCallLogAction(row.Action),
			StatusCode: int(row.StatusCode),
			LatencyMs:  int(row.LatencyMs),
			CreatedAt:  row.CreatedAt.Time.UTC(),
		})
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) invalidateMCPPolicy(ctx context.Context, workspaceID, botID uuid.UUID) {
	if pub, ok := s.turnQueue.(interface {
		PublishMCPPolicyInvalidate(context.Context, uuid.UUID, uuid.UUID) error
	}); ok {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := pub.PublishMCPPolicyInvalidate(cleanCtx, workspaceID, botID); err != nil {
			s.logger.Warn("mcp policy invalidation unavailable", "workspace_id", workspaceID, "bot_id", botID)
		}
	}
}

func validateMCPAuthInput(
	scheme apigen.MCPAuthScheme,
	rawHeaderName, rawUsername, rawSecret *string,
	requireSecret bool,
) (headerName, username, secretVal, errMsg string) {
	if !scheme.Valid() {
		return "", "", "", "authScheme must be none, bearer, header, or basic"
	}
	if rawHeaderName != nil {
		headerName = strings.TrimSpace(*rawHeaderName)
	}
	if rawUsername != nil {
		username = strings.TrimSpace(*rawUsername)
	}
	if rawSecret != nil {
		secretVal = *rawSecret
	}

	switch scheme {
	case apigen.MCPAuthSchemeNone:
		return "", "", "", ""
	case apigen.MCPAuthSchemeBearer:
		if headerName == "" {
			headerName = "Authorization"
		}
	case apigen.MCPAuthSchemeBasic:
		if headerName == "" {
			headerName = "Authorization"
		}
		if username == "" {
			return "", "", "", "authUsername is required when authScheme is basic"
		}
		if strings.Contains(username, ":") || strings.ContainsAny(username, "\r\n") {
			return "", "", "", "authUsername cannot contain ':' or newlines"
		}
	case apigen.MCPAuthSchemeHeader:
		if headerName == "" {
			return "", "", "", "authHeaderName is required when authScheme is header"
		}
	}
	if !httpHeaderNameRegex.MatchString(headerName) || forbiddenHeaderNames[strings.ToLower(headerName)] {
		return "", "", "", "authHeaderName is invalid or reserved"
	}
	if requireSecret || secretVal != "" {
		if msg, ok := validateSecretValue(secretVal); !ok {
			return "", "", "", msg
		}
	}
	return headerName, username, secretVal, ""
}

func parseMCPToolInputs(raw *[]apigen.MCPToolDefinitionInput) ([]mcpproxy.ToolDef, error) {
	if raw == nil {
		return nil, nil
	}
	defs := make([]mcpproxy.ToolDef, 0, len(*raw))
	for _, item := range *raw {
		desc := ""
		if item.Description != nil {
			desc = *item.Description
		}
		var schemaBytes json.RawMessage
		if item.InputSchema != nil {
			encoded, err := json.Marshal(*item.InputSchema)
			if err != nil {
				return nil, errors.New("invalid tool inputSchema")
			}
			schemaBytes = encoded
		}
		defs = append(defs, mcpproxy.ToolDef{
			ToolName:    item.ToolName,
			Description: desc,
			InputSchema: schemaBytes,
		})
	}
	return mcpproxy.NormalizeToolDefinitions(defs)
}

func groupMCPToolsByServer(rows []db.McpServerTool) map[uuid.UUID][]db.McpServerTool {
	grouped := make(map[uuid.UUID][]db.McpServerTool)
	for _, row := range rows {
		sid := fromPGUUID(row.ServerID)
		grouped[sid] = append(grouped[sid], row)
	}
	return grouped
}

func toAPIMCPServerTools(rows []db.McpServerTool) []apigen.MCPServerTool {
	out := make([]apigen.MCPServerTool, 0, len(rows))
	for _, t := range rows {
		var schema map[string]any
		if json.Unmarshal(t.InputSchema, &schema) != nil || schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, apigen.MCPServerTool{
			ToolName:     t.ToolName,
			Description:  t.Description,
			InputSchema:  schema,
			DiscoveredAt: t.DiscoveredAt.Time.UTC(),
		})
	}
	return out
}

func toAPIMCPServer(srv db.McpServer, tools []db.McpServerTool) apigen.MCPServer {
	var syncedAt *time.Time
	if srv.LastSyncedAt.Valid {
		t := srv.LastSyncedAt.Time.UTC()
		syncedAt = &t
	}
	return apigen.MCPServer{
		Id:             fromPGUUID(srv.ID),
		WorkspaceId:    fromPGUUID(srv.WorkspaceID),
		Slug:           srv.Slug,
		Name:           srv.Name,
		EndpointUrl:    srv.EndpointUrl,
		AuthScheme:     apigen.MCPAuthScheme(srv.AuthScheme),
		AuthHeaderName: srv.AuthHeaderName,
		AuthUsername:   srv.AuthUsername,
		HasSecret:      len(srv.EncryptedValue) > 0,
		MaskedPreview:  srv.MaskedPreview,
		Tools:          toAPIMCPServerTools(tools),
		LastSyncedAt:   syncedAt,
		LastSyncError:  srv.LastSyncError,
		CreatedAt:      srv.CreatedAt.Time.UTC(),
		UpdatedAt:      srv.UpdatedAt.Time.UTC(),
	}
}

func toAPIBotMCPServerPolicy(
	srv db.McpServer,
	assign *db.BotMcpAssignment,
	tools []db.McpServerTool,
	policyMap map[string]apigen.MCPToolPolicyMode,
	liveProxy bool,
) apigen.BotMCPServerPolicy {
	if policyMap == nil {
		policyMap = map[string]apigen.MCPToolPolicyMode{}
	}
	enabled := false
	defaultPolicy := apigen.Deny
	var rev, ackRev int64
	updatedAt := srv.UpdatedAt.Time.UTC()
	if assign != nil {
		enabled = assign.Enabled
		defaultPolicy = apigen.MCPToolPolicyMode(assign.DefaultPolicy)
		rev = assign.Revision
		ackRev = assign.AcknowledgedRevision
		updatedAt = assign.UpdatedAt.Time.UTC()
	}
	return apigen.BotMCPServerPolicy{
		ServerId:             fromPGUUID(srv.ID),
		ServerSlug:           srv.Slug,
		ServerName:           srv.Name,
		EndpointUrl:          srv.EndpointUrl,
		AuthScheme:           apigen.MCPAuthScheme(srv.AuthScheme),
		HasSecret:            len(srv.EncryptedValue) > 0,
		Enabled:              enabled,
		DefaultPolicy:        defaultPolicy,
		Revision:             rev,
		AcknowledgedRevision: ackRev,
		EnforcementStatus:    computeEnforcementStatus(liveProxy, rev, ackRev),
		ToolPolicies:         policyMap,
		Tools:                toAPIMCPServerTools(tools),
		UpdatedAt:            updatedAt,
	}
}

func hasPermittedMCPTool(defaultPolicy apigen.MCPToolPolicyMode, policies map[string]apigen.MCPToolPolicyMode) bool {
	if defaultPolicy == apigen.Allow || defaultPolicy == apigen.Ask {
		return true
	}
	for _, mode := range policies {
		if mode == apigen.Allow || mode == apigen.Ask {
			return true
		}
	}
	return false
}
