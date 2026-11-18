// SPDX-License-Identifier: AGPL-3.0-only

package mcpproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// PolicyAllow exposes and immediately executes the MCP command.
	PolicyAllow = "allow"
	// PolicyAsk exposes the MCP command and gates execution behind human chat approval.
	PolicyAsk = "ask"
	// PolicyDeny hides the MCP command and rejects calls fail-closed.
	PolicyDeny = "deny"

	maxProxyBodyBytes   = 1 << 20
	policyFreshnessMax  = time.Second
	maxArgPreviewBytes  = 512
	mcpProtocolVersion  = "2025-03-26"
	defaultServerPrefix = "botbox-mcp-proxy"
)

var (
	// ErrToolDenied indicates the MCP command is blocked, disabled, or unknown for the bot.
	ErrToolDenied = errors.New("mcp.tool_denied")
	// ErrApprovalRequired indicates the ask-gated command needs human confirmation in chat.
	ErrApprovalRequired = errors.New("mcp.approval_required")
	// ErrServerNotFound indicates no registered MCP server exists for the requested slug.
	ErrServerNotFound = errors.New("mcp.server_not_found")
	// ErrUpstreamFailed indicates the upstream HTTPS MCP server could not complete the request.
	ErrUpstreamFailed = errors.New("mcp.upstream_failed")
)

// ExposedTool describes one MCP tool visible to a bot after policy filtering.
type ExposedTool struct {
	Name             string
	ServerID         uuid.UUID
	ServerSlug       string
	ServerName       string
	OriginalToolName string
	Description      string
	InputSchema      json.RawMessage
	Policy           string
}

// CallResult holds the outcome of executing an authorized MCP tool through the proxy.
type CallResult struct {
	Result  json.RawMessage
	Text    string
	IsError bool
	Action  string
}

type cachedBotPolicy struct {
	loadedAt time.Time
	tools    []ExposedTool
	servers  map[string]botServerSnapshot
}

type botServerSnapshot struct {
	Server        db.McpServer
	Assignment    db.BotMcpAssignment
	ToolsByName   map[string]db.McpServerTool
	PolicyByTool  map[string]string
	EffectiveMode map[string]string
}

// Service enforces per-bot MCP policies, injects write-only credentials,
// records audit logs, and gates ask-level commands behind chat approvals.
type Service struct {
	Pool     *pgxpool.Pool
	KeyRing  *secrets.KeyRing
	Upstream *UpstreamClient
	Notify   notify.Signal
	Logger   *slog.Logger
	Now      func() time.Time

	mu    sync.RWMutex
	cache map[string]cachedBotPolicy
}

// NewService constructs a fail-closed MCP proxy service backed by Postgres and KeyRing.
func NewService(pool *pgxpool.Pool, ring *secrets.KeyRing, upstream *UpstreamClient, signal notify.Signal, logger *slog.Logger) *Service {
	if upstream == nil {
		upstream = NewUpstreamClient()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		Pool:     pool,
		KeyRing:  ring,
		Upstream: upstream,
		Notify:   signal,
		Logger:   logger,
		Now:      time.Now,
		cache:    make(map[string]cachedBotPolicy),
	}
}

// InvalidateBot drops any cached MCP policy snapshot for the identified bot.
func (s *Service) InvalidateBot(workspaceID, botID uuid.UUID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.cache, cacheKey(workspaceID, botID))
	s.mu.Unlock()
}

func cacheKey(workspaceID, botID uuid.UUID) string {
	return workspaceID.String() + "/" + botID.String()
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ListBotTools returns only allow and ask tools for the bot; denied and unconfigured tools stay hidden.
func (s *Service) ListBotTools(ctx context.Context, workspaceID, botID uuid.UUID, serverSlugFilter string) ([]ExposedTool, error) {
	snap, err := s.loadPolicySnapshot(ctx, workspaceID, botID)
	if err != nil {
		return nil, err
	}
	out := make([]ExposedTool, 0, len(snap.tools))
	for _, tool := range snap.tools {
		if serverSlugFilter != "" {
			if tool.ServerSlug != serverSlugFilter {
				continue
			}
			copyTool := tool
			copyTool.Name = tool.OriginalToolName
			out = append(out, copyTool)
			continue
		}
		out = append(out, tool)
	}
	return out, nil
}

// CallBotTool evaluates policy, enforces human approval gates, injects credentials,
// executes the upstream MCP call, scrubs secret reflections, and writes mcp_call_log.
func (s *Service) CallBotTool(
	ctx context.Context,
	workspaceID, botID uuid.UUID,
	serverSlug, toolName string,
	rawArgs json.RawMessage,
) (*CallResult, error) {
	start := s.now()
	if s == nil || s.Pool == nil {
		return nil, errors.New("mcp proxy is not configured")
	}
	normSlug, okSlug := ValidateSlug(serverSlug)
	normTool, okTool := ValidateToolName(toolName)
	if !okSlug || !okTool {
		s.recordCallLog(ctx, workspaceID, botID, uuid.Nil, serverSlug, toolName, "blocked", http.StatusForbidden, start)
		return nil, ErrToolDenied
	}

	cleanArgs, argsHash, err := canonicalizeArgs(rawArgs)
	if err != nil {
		s.recordCallLog(ctx, workspaceID, botID, uuid.Nil, normSlug, normTool, "blocked", http.StatusBadRequest, start)
		return nil, errors.New("mcp.invalid_arguments")
	}

	snap, err := s.loadPolicySnapshot(ctx, workspaceID, botID)
	if err != nil {
		return nil, err
	}
	srvSnap, exists := snap.servers[normSlug]
	if !exists || !srvSnap.Assignment.Enabled {
		s.recordCallLog(ctx, workspaceID, botID, uuid.Nil, normSlug, normTool, "blocked", http.StatusForbidden, start)
		return nil, ErrToolDenied
	}

	srvID := uuid.UUID(srvSnap.Server.ID.Bytes)
	policy := srvSnap.EffectiveMode[normTool]
	if _, known := srvSnap.ToolsByName[normTool]; !known || (policy != PolicyAllow && policy != PolicyAsk) {
		s.recordCallLog(ctx, workspaceID, botID, srvID, normSlug, normTool, "blocked", http.StatusForbidden, start)
		return nil, ErrToolDenied
	}

	action := "allowed"
	if policy == PolicyAsk {
		approved, grantErr := s.consumeApprovedGrant(ctx, workspaceID, botID, srvID, normTool, argsHash)
		if grantErr != nil {
			return nil, grantErr
		}
		if !approved {
			preview := fmt.Sprintf("%s / %s · %s", normSlug, normTool, truncatePreview(string(cleanArgs)))
			if _, _, openErr := approvals.OpenMCPTool(ctx, s.Pool, approvals.MCPToolRequest{
				WorkspaceID: workspaceID,
				BotID:       botID,
				ServerID:    srvID,
				ServerSlug:  normSlug,
				ToolName:    normTool,
				ArgsHash:    argsHash,
				Preview:     preview,
			}, s.Notify); openErr != nil {
				s.Logger.Warn("failed to record mcp_tool approval card", "err", openErr)
			}
			s.recordCallLog(ctx, workspaceID, botID, srvID, normSlug, normTool, "ask_pending", http.StatusConflict, start)
			return nil, ErrApprovalRequired
		}
		action = "approved_by_human"
	}

	headerName, headerValue, secretRaw, err := s.decryptServerCredential(workspaceID, srvSnap.Server)
	if err != nil {
		s.recordCallLog(ctx, workspaceID, botID, srvID, normSlug, normTool, "upstream_error", http.StatusBadGateway, start)
		return nil, ErrUpstreamFailed
	}

	upstream := s.Upstream
	if upstream == nil {
		upstream = NewUpstreamClient()
	}
	res, err := upstream.CallUpstreamTool(ctx, srvSnap.Server.EndpointUrl, headerName, headerValue, secretRaw, normTool, cleanArgs)
	if err != nil {
		s.recordCallLog(ctx, workspaceID, botID, srvID, normSlug, normTool, "upstream_error", http.StatusBadGateway, start)
		return nil, fmt.Errorf("%w: %v", ErrUpstreamFailed, err)
	}

	s.recordCallLog(ctx, workspaceID, botID, srvID, normSlug, normTool, action, http.StatusOK, start)
	return &CallResult{
		Result:  res.Result,
		Text:    res.Text,
		IsError: res.IsError,
		Action:  action,
	}, nil
}

func (s *Service) consumeApprovedGrant(ctx context.Context, workspaceID, botID, serverID uuid.UUID, toolName, argsHash string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	grant, err := q.GetApprovedMCPToolGrant(ctx, db.GetApprovedMCPToolGrantParams{
		WorkspaceID: pgUUID(workspaceID),
		BotID:       pgUUID(botID),
		ServerID:    pgUUID(serverID),
		ToolName:    toolName,
		ArgsSha256:  argsHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n, err := q.ConsumeMCPToolGrant(ctx, db.ConsumeMCPToolGrantParams{
		WorkspaceID: pgUUID(workspaceID),
		ID:          grant.ID,
	})
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) decryptServerCredential(workspaceID uuid.UUID, srv db.McpServer) (headerName, headerValue, secretRaw string, err error) {
	if srv.AuthScheme == "" || srv.AuthScheme == "none" {
		return "", "", "", nil
	}
	if s.KeyRing == nil {
		return "", "", "", errors.New("secrets key ring unavailable")
	}
	plain, err := s.KeyRing.Decrypt(secrets.EnvelopeCiphertext{
		KeyID:      srv.KekID,
		WrappedDEK: srv.WrappedDek,
		Ciphertext: srv.EncryptedValue,
	}, secrets.SecretAAD(workspaceID, "mcp_server", uuid.UUID(srv.ID.Bytes)))
	if err != nil {
		return "", "", "", err
	}
	secretStr := string(plain)
	name, val, err := BuildAuthHeader(srv.AuthScheme, srv.AuthHeaderName, srv.AuthUsername, secretStr)
	if err != nil {
		return "", "", "", err
	}
	return name, val, secretStr, nil
}

func (s *Service) loadPolicySnapshot(ctx context.Context, workspaceID, botID uuid.UUID) (cachedBotPolicy, error) {
	if s == nil || s.Pool == nil {
		return cachedBotPolicy{}, errors.New("mcp proxy database unavailable")
	}
	key := cacheKey(workspaceID, botID)
	now := s.now()
	s.mu.RLock()
	cached, ok := s.cache[key]
	s.mu.RUnlock()
	if ok && now.Sub(cached.loadedAt) < policyFreshnessMax {
		return cached, nil
	}

	q := db.New(s.Pool)
	wsPG := pgUUID(workspaceID)
	botPG := pgUUID(botID)

	servers, err := q.ListMCPServers(ctx, wsPG)
	if err != nil {
		return cachedBotPolicy{}, err
	}
	assignments, err := q.ListBotMCPAssignments(ctx, db.ListBotMCPAssignmentsParams{WorkspaceID: wsPG, BotID: botPG})
	if err != nil {
		return cachedBotPolicy{}, err
	}
	allTools, err := q.ListWorkspaceMCPServerTools(ctx, wsPG)
	if err != nil {
		return cachedBotPolicy{}, err
	}
	overrides, err := q.ListBotMCPToolPolicies(ctx, db.ListBotMCPToolPoliciesParams{WorkspaceID: wsPG, BotID: botPG})
	if err != nil {
		return cachedBotPolicy{}, err
	}

	serverByID := make(map[uuid.UUID]db.McpServer, len(servers))
	for _, srv := range servers {
		serverByID[uuid.UUID(srv.ID.Bytes)] = srv
	}
	toolsByServer := make(map[uuid.UUID][]db.McpServerTool)
	for _, t := range allTools {
		sid := uuid.UUID(t.ServerID.Bytes)
		toolsByServer[sid] = append(toolsByServer[sid], t)
	}
	policiesByServer := make(map[uuid.UUID]map[string]string)
	for _, p := range overrides {
		sid := uuid.UUID(p.ServerID.Bytes)
		if policiesByServer[sid] == nil {
			policiesByServer[sid] = make(map[string]string)
		}
		policiesByServer[sid][p.ToolName] = p.Policy
	}

	snap := cachedBotPolicy{
		loadedAt: now,
		servers:  make(map[string]botServerSnapshot, len(assignments)),
	}
	for _, assign := range assignments {
		sid := uuid.UUID(assign.ServerID.Bytes)
		srv, found := serverByID[sid]
		if !found {
			continue
		}
		if assign.AcknowledgedRevision < assign.Revision {
			_ = q.AcknowledgeBotMCPPolicyRevision(ctx, db.AcknowledgeBotMCPPolicyRevisionParams{
				WorkspaceID: wsPG,
				BotID:       botPG,
				ServerID:    assign.ServerID,
				Column4:     assign.Revision,
			})
		}

		toolMap := make(map[string]db.McpServerTool)
		effective := make(map[string]string)
		pMap := policiesByServer[sid]
		for _, tool := range toolsByServer[sid] {
			toolMap[tool.ToolName] = tool
			pol := assign.DefaultPolicy
			if override, has := pMap[tool.ToolName]; has {
				pol = override
			}
			if pol != PolicyAllow && pol != PolicyAsk && pol != PolicyDeny {
				pol = PolicyDeny
			}
			effective[tool.ToolName] = pol
			if assign.Enabled && (pol == PolicyAllow || pol == PolicyAsk) {
				snap.tools = append(snap.tools, ExposedTool{
					Name:             FormatPrefixedToolName(srv.Slug, tool.ToolName),
					ServerID:         sid,
					ServerSlug:       srv.Slug,
					ServerName:       srv.Name,
					OriginalToolName: tool.ToolName,
					Description:      tool.Description,
					InputSchema:      tool.InputSchema,
					Policy:           pol,
				})
			}
		}
		snap.servers[srv.Slug] = botServerSnapshot{
			Server:        srv,
			Assignment:    assign,
			ToolsByName:   toolMap,
			PolicyByTool:  pMap,
			EffectiveMode: effective,
		}
	}

	sort.Slice(snap.tools, func(i, j int) bool {
		return snap.tools[i].Name < snap.tools[j].Name
	})

	s.mu.Lock()
	s.cache[key] = snap
	s.mu.Unlock()
	return snap, nil
}

func (s *Service) recordCallLog(
	ctx context.Context,
	workspaceID, botID, serverID uuid.UUID,
	serverSlug, toolName, action string,
	statusCode int,
	start time.Time,
) {
	if s == nil || s.Pool == nil {
		return
	}
	latency := int(s.now().Sub(start).Milliseconds())
	if latency < 0 {
		latency = 0
	}
	var srvPG pgtype.UUID
	if serverID != uuid.Nil {
		srvPG = pgUUID(serverID)
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := db.New(s.Pool).CreateMCPCallLog(logCtx, db.CreateMCPCallLogParams{
		WorkspaceID: pgUUID(workspaceID),
		BotID:       pgUUID(botID),
		ServerID:    srvPG,
		ServerSlug:  truncateStr(serverSlug, 64),
		ToolName:    truncateStr(toolName, 64),
		Action:      action,
		StatusCode:  int32(statusCode), // #nosec G115 -- bounded HTTP status code
		LatencyMs:   int32(latency),    // #nosec G115 -- bounded request latency
	})
	if err != nil && s.Logger != nil {
		s.Logger.Error("persist mcp call log failed", "err", err)
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Handler returns a Streamable HTTP JSON-RPC 2.0 handler pinned to one workspace and bot.
// Requests to /mcp expose all assigned servers prefixed as mcp__<slug>__<tool>.
// Requests to /mcp/<slug> expose that single server's unprefixed tools.
func (s *Service) Handler(workspaceID, botID uuid.UUID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		serverSlug := ""
		if r.URL.Path != "/mcp" {
			trimmed := strings.TrimPrefix(r.URL.Path, "/mcp/")
			trimmed = strings.Trim(trimmed, "/")
			if trimmed != "" {
				validSlug, ok := ValidateSlug(trimmed)
				if !ok {
					writeProxyRPCError(w, http.StatusNotFound, nil, -32601, "unknown mcp server")
					return
				}
				serverSlug = validSlug
			}
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBodyBytes+1))
		if err != nil {
			writeProxyRPCError(w, http.StatusBadRequest, nil, -32700, "failed to read request body")
			return
		}
		if len(raw) > maxProxyBodyBytes {
			writeProxyRPCError(w, http.StatusRequestEntityTooLarge, nil, -32600, "request body exceeds limit")
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeProxyRPCError(w, http.StatusOK, json.RawMessage("null"), -32700, "parse error")
			return
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			writeProxyRPCError(w, http.StatusOK, normRPCID(req.ID), -32600, "invalid JSON-RPC request")
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		id := normRPCID(req.ID)
		switch req.Method {
		case "initialize":
			writeProxyRPCOK(w, id, map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]string{
					"name":    defaultServerPrefix,
					"version": "0.1.0",
				},
			})
		case "tools/list":
			tools, listErr := s.ListBotTools(r.Context(), workspaceID, botID, serverSlug)
			if listErr != nil {
				writeProxyRPCError(w, http.StatusOK, id, -32603, "mcp policy unavailable")
				return
			}
			if serverSlug != "" {
				snap, snapErr := s.loadPolicySnapshot(r.Context(), workspaceID, botID)
				if snapErr != nil {
					writeProxyRPCError(w, http.StatusOK, id, -32603, "mcp policy unavailable")
					return
				}
				srvSnap, exists := snap.servers[serverSlug]
				if !exists || !srvSnap.Assignment.Enabled {
					writeProxyRPCError(w, http.StatusOK, id, -32003, "mcp.server_not_assigned")
					return
				}
			}
			items := make([]map[string]any, 0, len(tools))
			for _, t := range tools {
				items = append(items, map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": json.RawMessage(t.InputSchema),
				})
			}
			writeProxyRPCOK(w, id, map[string]any{"tools": items})
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
				writeProxyRPCError(w, http.StatusOK, id, -32602, "invalid tools/call params")
				return
			}
			targetSlug := serverSlug
			targetTool := params.Name
			if targetSlug == "" {
				parsedSlug, parsedTool, ok := ParsePrefixedToolName(params.Name)
				if !ok {
					writeProxyRPCError(w, http.StatusOK, id, -32003, "mcp.tool_denied: command is not permitted for this bot")
					return
				}
				targetSlug = parsedSlug
				targetTool = parsedTool
			}
			res, callErr := s.CallBotTool(r.Context(), workspaceID, botID, targetSlug, targetTool, params.Arguments)
			if callErr != nil {
				switch {
				case errors.Is(callErr, ErrToolDenied):
					writeProxyRPCError(w, http.StatusOK, id, -32003, "mcp.tool_denied: command is not permitted for this bot")
				case errors.Is(callErr, ErrApprovalRequired):
					writeProxyRPCOK(w, id, map[string]any{
						"isError": true,
						"content": []map[string]any{{
							"type": "text",
							"text": fmt.Sprintf("mcp.approval_required: human approval requested in chat for %s/%s; wait for confirmation before retrying.", targetSlug, targetTool),
						}},
					})
				case errors.Is(callErr, ErrUpstreamFailed):
					writeProxyRPCOK(w, id, map[string]any{
						"isError": true,
						"content": []map[string]any{{
							"type": "text",
							"text": "mcp.upstream_failed: upstream MCP server request failed",
						}},
					})
				default:
					writeProxyRPCError(w, http.StatusOK, id, -32602, callErr.Error())
				}
				return
			}
			if len(res.Result) > 0 && json.Valid(res.Result) {
				writeProxyRPCOK(w, id, json.RawMessage(res.Result))
				return
			}
			writeProxyRPCOK(w, id, map[string]any{
				"isError": res.IsError,
				"content": []map[string]any{{"type": "text", "text": res.Text}},
			})
		default:
			writeProxyRPCError(w, http.StatusOK, id, -32601, "method not found")
		}
	})
}

func canonicalizeArgs(raw json.RawMessage) (json.RawMessage, string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return nil, "", errors.New("arguments must be a JSON object")
	}
	canonical, err := json.Marshal(obj)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func truncatePreview(s string) string {
	if len(s) <= maxArgPreviewBytes {
		return s
	}
	return s[:maxArgPreviewBytes] + "..."
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func normRPCID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func writeProxyRPCOK(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      normRPCID(id),
		Result:  result,
	})
}

func writeProxyRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      normRPCID(id),
		Error:   &rpcError{Code: code, Message: msg},
	})
}
