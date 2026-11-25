// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/bots"
	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	"github.com/dunkeltech/botbox/backend/internal/computermcp"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/guardrails"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// egressDeps is the per-bot proxy surface. AuthMode must not return credential material.
type egressDeps struct {
	Connect         http.Handler
	NativeGoogle    http.Handler
	NativeOnly      bool
	LLM             http.Handler
	MCP             http.Handler
	TeamMCP         http.Handler
	MemoryMCP       http.Handler
	AuthMode        func(context.Context) (string, error)
	OpenAIAuthMode  func(context.Context) (string, error)
	PlanReady       func(context.Context) error
	OpenAIReady     func(context.Context) error
	Health          func(context.Context) error
	CACert          []byte
	Broker          authbroker.Client
	BrokerWorkspace uuid.UUID
	BrokerBot       uuid.UUID
}

func databaseHandlerWithLifecycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	ring *secrets.KeyRing,
	workspace, bot uuid.UUID,
	ca *mitmproxy.BotCA,
	logger *slog.Logger,
	signal notify.Signal,
	pub *events.Publisher,
	broker authbroker.Client,
	nativeOnly bool,
) (http.Handler, func(), error) {
	if pool == nil || ring == nil || workspace == uuid.Nil || bot == uuid.Nil || ca == nil {
		return nil, nil, errors.New("proxy configuration incomplete")
	}
	q := db.New(pool)
	wsPG := pgtype.UUID{Bytes: workspace, Valid: true}
	botPG := pgtype.UUID{Bytes: bot, Valid: true}
	nativeGoogleEnabled := func(ctx context.Context) (bool, error) {
		config, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: wsPG, ID: botPG})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return err == nil && config.LlmMode == "cli" && config.LlmProvider == bots.AgyACPProvider, err
	}

	var nativeHosts func(context.Context) ([]string, error)
	if nativeOnly {
		nativeHosts = func(ctx context.Context) ([]string, error) {
			if enabled, err := nativeGoogleEnabled(ctx); !enabled {
				return nil, err
			}
			return nativeGoogleHosts[:], nil
		}
	}
	//nolint:contextcheck // OnAudit creates a bounded cleanup context per request outside startup ctx
	connect, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{
		Pool:               pool,
		KeyRing:            ring,
		WorkspaceID:        workspace,
		BotID:              bot,
		CA:                 ca,
		Logger:             logger,
		Notify:             signal,
		PolicyFreshnessMax: time.Second,
		PublicOnly:         true,
		NativeHosts:        nativeHosts,
		OnPolicyAdopted: func(ackCtx context.Context, revision int64) error {
			return q.AcknowledgeEgressPolicyRevision(ackCtx, db.AcknowledgeEgressPolicyRevisionParams{
				WorkspaceID: wsPG,
				BotID:       botPG,
				Column3:     revision,
			})
		},
	})
	if err != nil {
		return nil, nil, err
	}

	// Load and adopt the authoritative policy before accepting any traffic.
	initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
	err = connect.RevalidatePassthrough(initCtx)
	initCancel()
	if err != nil {
		return nil, nil, err
	}

	stopReval := connect.StartRevalidation(ctx, 500*time.Millisecond)
	unsub := func() {}

	if pub != nil {
		//nolint:contextcheck // NATS message handler creates a per-event timeout context
		subStop, subErr := pub.SubscribeEgressPolicyInvalidate(workspace, bot, func(evCtx context.Context, ev events.EgressInvalidationEvent) (int64, error) {
			if ev.Revision > 0 && ev.Revision <= connect.AcknowledgedRevision() {
				return connect.AcknowledgedRevision(), nil
			}
			if ev.Host != "" && ev.Revision == 0 {
				connect.InvalidateHost(ev.Host)
			}
			if err := connect.RevalidatePassthrough(evCtx); err != nil {
				return 0, err
			}
			return connect.AcknowledgedRevision(), nil
		})
		if subErr != nil {
			if logger != nil {
				logger.Warn("egress invalidation subscription unavailable", "err", subErr)
			}
		} else {
			unsub = subStop
		}
	}
	resolveCLI := secrets.CLIProviderResolver(q, ring, workspace, bot)
	plan := codexplan.NewService(pool, ring, nil, time.Now)
	resolve := func(ctx context.Context, botID, provider string) (*llmproxy.SecretConfig, error) {
		switch provider {
		case llmproxy.ProviderOpenAIPlan:
			if botID != bot.String() {
				return nil, llmproxy.ErrSecretNotFound
			}
			token, revision, err := plan.Access(ctx, workspace, bot)
			switch {
			case errors.Is(err, codexplan.ErrForbidden):
				if cfg, openErr := plan.ResolveOpenAISecret(ctx, workspace, bot); openErr == nil && cfg.AuthKind == llmproxy.AuthKindOpenAIPlan {
					if codexplan.CheckDesktopOpenAI(ctx, q, wsPG, botPG, cfg.PlanRevision) == nil {
						return cfg, nil
					}
				}
				return nil, llmproxy.ErrSecretNotFound
			case errors.Is(err, codexplan.ErrReconnect):
				return nil, llmproxy.ErrPlanReconnect
			case err != nil:
				return nil, err
			default:
				return &llmproxy.SecretConfig{UpstreamURL: codexplan.Upstream, PathPrefix: "/v1/responses", AuthKind: llmproxy.AuthKindOpenAIPlan, SecretValue: token, PlanRevision: revision}, nil
			}
		case llmproxy.ProviderOpenAI:
			if botID != bot.String() {
				return nil, llmproxy.ErrSecretNotFound
			}
			return plan.ResolveOpenAISecret(ctx, workspace, bot)
		case llmproxy.ProviderGoogle:
			if enabled, err := nativeGoogleEnabled(ctx); enabled || err != nil {
				return nil, llmproxy.ErrSecretNotFound
			}
			return resolveCLI(ctx, botID, provider)
		default:
			return resolveCLI(ctx, botID, provider)
		}
	}
	guardrailSvc := guardrails.NewService(pool, ring, signal, logger)
	guardrailUnsub := func() {}
	if pub != nil {
		subStop, subErr := pub.SubscribeGuardrailsInvalidate(workspace, func() {
			guardrailSvc.InvalidateWorkspace(workspace)
		})
		if subErr != nil {
			if logger != nil {
				logger.Warn("guardrails invalidation subscription unavailable", "err", subErr)
			}
		} else {
			guardrailUnsub = subStop
		}
	}

	llm, err := llmproxy.NewScopedHandler(workspace.String(), bot.String(), resolve, &llmproxy.Options{
		Logger:     logger,
		Admit:      budgets.ProxyAdmission(pool, workspace, bot, time.Now),
		Guardrails: guardrailSvc.ForBot(workspace, bot),
		Client:     &http.Client{Timeout: 10 * time.Minute},
	})
	if err != nil {
		guardrailUnsub()
		unsub()
		stopReval()
		return nil, nil, err
	}
	var nativeConnect http.Handler
	if nativeOnly {
		options := llmproxy.Options{
			Logger: logger, Admit: budgets.ProxyAdmission(pool, workspace, bot, time.Now),
			Guardrails: guardrailSvc.ForBot(workspace, bot),
		}
		if os.Getenv("EGRESS_PROXY_AGY_ACP_TOOLS") == "v1" {
			options.NativeComputerTools = computermcp.ModelTools()
		}
		nativeGoogle := llmproxy.NewNativeGoogleProxy(bot.String(), options)
		nativeConnect = newNativeGoogleConnect(connect, nativeGoogleEnabled, nativeGoogle.ServeInspected)
	}
	mcpSvc := mcpproxy.NewService(pool, ring, nil, signal, logger)
	mcpUnsub := func() {}
	if pub != nil {
		subStop, subErr := pub.SubscribeMCPPolicyInvalidate(workspace, bot, func() {
			mcpSvc.InvalidateBot(workspace, bot)
		})
		if subErr != nil {
			if logger != nil {
				logger.Warn("mcp invalidation subscription unavailable", "err", subErr)
			}
		} else {
			mcpUnsub = subStop
		}
	}
	stopAll := func() {
		mcpUnsub()
		guardrailUnsub()
		unsub()
		stopReval()
	}
	readyCheck := func(ctx context.Context) error {
		state, err := q.GetBotRuntime(ctx, db.GetBotRuntimeParams{WorkspaceID: wsPG, ID: botPG})
		if err != nil {
			return err
		}
		if state.Paused {
			return codexplan.ErrForbidden
		}
		return codexplan.CheckDesktopOpenAI(ctx, q, wsPG, botPG, "")
	}
	return newEgressHandler(egressDeps{
		Connect:      connect,
		NativeGoogle: nativeConnect,
		NativeOnly:   nativeOnly,
		LLM:          llm,
		MCP:          mcpSvc.Handler(workspace, bot),
		TeamMCP:      mcpproxy.TeamHandler(groups.TeamTools{Pool: pool, Signal: signal}, workspace, bot),
		MemoryMCP:    mcpproxy.MemoryHandler(memory.Service{Pool: pool, Ring: ring, Signal: signal}, workspace, bot),
		AuthMode: func(ctx context.Context) (string, error) {
			return llmproxy.AuthMode(ctx, resolve, bot.String(), llmproxy.ProviderAnthropic)
		},
		OpenAIAuthMode: func(ctx context.Context) (string, error) {
			mode, authErr := llmproxy.AuthMode(ctx, resolveCLI, bot.String(), llmproxy.ProviderOpenAI)
			if errors.Is(authErr, llmproxy.ErrPlanReconnect) {
				return "none", nil
			}
			if authErr != nil {
				return "", authErr
			}
			if mode == "none" {
				if status, _, _, planErr := plan.Status(ctx, wsPG, botPG, pgtype.UUID{}); planErr == nil && status == "connected" {
					return "oauth", nil
				}
			}
			return mode, nil
		},
		PlanReady:       readyCheck,
		OpenAIReady:     readyCheck,
		Health:          pool.Ping,
		CACert:          ca.CertPEM(),
		Broker:          broker,
		BrokerWorkspace: workspace,
		BrokerBot:       bot,
	}), stopAll, nil
}

func newEgressHandler(deps egressDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			// Native provider endpoints must use the metered LLM routes. Tunneling
			// them would bypass request settlement, even under an open egress policy.
			host, _, err := net.SplitHostPort(r.Host)
			if err == nil && isNativeGoogleHost(host) && deps.NativeGoogle != nil {
				deps.NativeGoogle.ServeHTTP(w, r)
				return
			}
			if err != nil || deps.NativeOnly || isLLMHost(host) {
				http.Error(w, "metered LLM endpoint required", http.StatusForbidden)
				return
			}
			if deps.Connect == nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			deps.Connect.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/ca.crt" {
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(deps.CACert)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			if deps.Health == nil || deps.Health(r.Context()) != nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if deps.NativeOnly {
			http.NotFound(w, r)
			return
		}
		// The desktop CLI asks which placeholder to present. The answer is a
		// mode name only; the encrypted token stays in this process.
		if r.Method == http.MethodGet && r.URL.Path == "/llm/anthropic/auth-mode" {
			serveAuthMode(w, r, deps.AuthMode)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/llm/openai/auth-mode" {
			serveAuthMode(w, r, deps.OpenAIAuthMode)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/llm/openai-plan/ready" {
			serveOpenAIReady(w, r, deps.PlanReady)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/llm/openai/ready" {
			serveOpenAIReady(w, r, deps.OpenAIReady)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/llm/") {
			if deps.LLM == nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			deps.LLM.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/broker/") {
			serveBrokerBridge(w, r, deps)
			return
		}
		if r.URL.Path == "/team-mcp" && deps.TeamMCP != nil {
			deps.TeamMCP.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/memory-mcp" && deps.MemoryMCP != nil {
			deps.MemoryMCP.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/") {
			if deps.MCP == nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			deps.MCP.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func serveAuthMode(w http.ResponseWriter, r *http.Request, fetch func(context.Context) (string, error)) {
	mode := ""
	var err error
	if fetch != nil {
		mode, err = fetch(r.Context())
	} else {
		err = errors.New("auth mode unavailable")
	}
	if err != nil || (mode != "oauth" && mode != "api_key" && mode != "none") {
		http.Error(w, "credential service unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, mode+"\n")
}

func serveOpenAIReady(w http.ResponseWriter, r *http.Request, ready func(context.Context) error) {
	w.Header().Set("Cache-Control", "no-store")
	if ready == nil {
		http.Error(w, "connection unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := ready(r.Context()); err != nil {
		if errors.Is(err, codexplan.ErrForbidden) || errors.Is(err, codexplan.ErrReconnect) {
			http.Error(w, "desktop connection unavailable", http.StatusForbidden)
		} else {
			http.Error(w, "connection unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveBrokerBridge forwards strictly limited bot-scoped browser operations to
// the external auth-broker using the proxy container's pinned workspace and bot IDs.
func serveBrokerBridge(w http.ResponseWriter, r *http.Request, deps egressDeps) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if deps.Broker == nil || deps.BrokerWorkspace == uuid.Nil || deps.BrokerBot == uuid.Nil {
		writeProxyJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"code":  "secret_request.unsupported_capability",
			"error": "isolated authentication broker is not configured",
		})
		return
	}
	defer r.Body.Close() //nolint:errcheck // best-effort close
	var in struct {
		BrokerSessionID string `json:"broker_session_id"`
		URL             string `json:"url"`
		Selector        string `json:"selector"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]string{"code": "validation_error", "error": "invalid broker request payload"})
		return
	}
	sessID, err := uuid.Parse(strings.TrimSpace(in.BrokerSessionID))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]string{"code": "validation_error", "error": "invalid broker_session_id"})
		return
	}

	switch r.URL.Path {
	case "/broker/navigate":
		sess, opErr := deps.Broker.BotNavigate(r.Context(), deps.BrokerWorkspace, deps.BrokerBot, sessID, in.URL)
		if opErr != nil {
			writeBrokerBridgeError(w, opErr)
			return
		}
		writeProxyJSON(w, http.StatusOK, map[string]any{
			"session_id": sess.SessionID.String(),
			"origin":     sess.Origin,
			"status":     sess.Status,
			"page_url":   sess.Binding.PageURL,
		})
	case "/broker/read":
		res, opErr := deps.Broker.BotRead(r.Context(), deps.BrokerWorkspace, deps.BrokerBot, sessID, in.Selector)
		if opErr != nil {
			writeBrokerBridgeError(w, opErr)
			return
		}
		writeProxyJSON(w, http.StatusOK, res)
	case "/broker/click":
		res, opErr := deps.Broker.BotClick(r.Context(), deps.BrokerWorkspace, deps.BrokerBot, sessID, in.Selector)
		if opErr != nil {
			writeBrokerBridgeError(w, opErr)
			return
		}
		writeProxyJSON(w, http.StatusOK, res)
	case "/broker/screenshot":
		img, binding, opErr := deps.Broker.BotScreenshot(r.Context(), deps.BrokerWorkspace, deps.BrokerBot, sessID)
		if opErr != nil {
			writeBrokerBridgeError(w, opErr)
			return
		}
		writeProxyJSON(w, http.StatusOK, map[string]any{
			"mime":      "image/png",
			"image":     base64.StdEncoding.EncodeToString(img),
			"page_url":  binding.PageURL,
			"origin":    binding.Origin,
			"sanitized": true,
		})
	default:
		http.NotFound(w, r)
	}
}

func writeBrokerBridgeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	code := "broker_error"
	switch {
	case errors.Is(err, authbroker.ErrTakeoverActive):
		status, code = http.StatusLocked, "takeover_active"
	case errors.Is(err, authbroker.ErrCaptureSuppressed):
		status, code = http.StatusConflict, "capture_suppressed"
	case errors.Is(err, authbroker.ErrNavigationForbidden):
		status, code = http.StatusForbidden, "navigation_forbidden"
	case errors.Is(err, authbroker.ErrActionForbidden):
		status, code = http.StatusForbidden, "action_forbidden"
	case errors.Is(err, authbroker.ErrSessionNotFound):
		status, code = http.StatusNotFound, "session_not_found"
	case errors.Is(err, authbroker.ErrSessionExpired):
		status, code = http.StatusGone, "session_expired"
	}
	writeProxyJSON(w, status, map[string]string{"code": code, "error": err.Error()})
}

func writeProxyJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func isLLMHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, domain := range []string{"openai.com", "anthropic.com", "openrouter.ai", "googleapis.com", "cloudcode-pa.googleapis.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func openDatabaseProxy(ctx context.Context, dsn string, ring *secrets.KeyRing, workspace, bot, natsURL string, ca *mitmproxy.BotCA, logger *slog.Logger, nativeOnly bool) (http.Handler, func(), error) {
	ws, err := uuid.Parse(workspace)
	if err != nil {
		return nil, nil, errors.New("invalid proxy workspace identity")
	}
	botID, err := uuid.Parse(bot)
	if err != nil {
		return nil, nil, errors.New("invalid proxy bot identity")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, errors.New("proxy database unavailable")
	}
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = pool.Ping(ready); err != nil {
		pool.Close()
		return nil, nil, errors.New("proxy database unavailable")
	}
	var brokerClient authbroker.Client
	if brokerURL := strings.TrimSpace(os.Getenv("EGRESS_PROXY_AUTH_BROKER_URL")); brokerURL != "" {
		brokerToken := strings.TrimSpace(os.Getenv("EGRESS_PROXY_AUTH_BROKER_TOKEN"))
		client, bErr := authbroker.NewHTTPClient(brokerURL, brokerToken)
		if bErr != nil {
			pool.Close()
			return nil, nil, errors.New("invalid proxy auth broker configuration")
		}
		brokerClient = client
	}
	// A missed signal is recovered from the notification list. The proxy still
	// has to block the host when NATS is down (issue #17).
	var signal notify.Signal
	closeAll := func() { pool.Close() }
	if strings.TrimSpace(natsURL) != "" {
		publisher, pubErr := events.OpenPublisher(natsURL)
		if pubErr != nil {
			logger.Warn("notification signal unavailable", "err", pubErr)
		} else {
			closeAll = func() {
				publisher.Close()
				pool.Close()
			}
			signal = func(sigCtx context.Context, workspaceID, conversationID uuid.UUID, ids []uuid.UUID) {
				data, err := notify.MarshalSignal(conversationID, ids)
				if err != nil {
					logger.Error("encode notification signal", "err", err)
					return
				}
				if err = publisher.PublishConversation(sigCtx, workspaceID, conversationID, data); err != nil {
					logger.Error("publish notification signal", "err", err)
				}
			}
			handler, stopHandler, err := databaseHandlerWithLifecycle(ctx, pool, ring, ws, botID, ca, logger, signal, publisher, brokerClient, nativeOnly)
			if err != nil {
				closeAll()
				return nil, nil, err
			}
			return handler, func() {
				stopHandler()
				closeAll()
			}, nil
		}
	}
	handler, stopHandler, err := databaseHandlerWithLifecycle(ctx, pool, ring, ws, botID, ca, logger, signal, nil, brokerClient, nativeOnly)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	return handler, func() {
		stopHandler()
		closeAll()
	}, nil
}
