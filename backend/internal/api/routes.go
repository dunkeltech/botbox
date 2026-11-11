package api

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	chatRL            *auth.RateLimiter
	chatEvents        ChatEvents
	turnQueue         TurnQueue
	logger            *slog.Logger
	pool              *pgxpool.Pool
	queries           *db.Queries
	jwt               *auth.JWTService
	authProvider      auth.Provider
	argon2            auth.Argon2Params
	setup             *auth.SetupManager
	setupRL           *auth.RateLimiter
	loginIPRL         *auth.RateLimiter
	loginEmailRL      *auth.RateLimiter
	refreshRL         *auth.RateLimiter
	compute           compute.Driver
	authBroker        authbroker.Client
	ticketSigner      *vncrelay.Signer
	keyRing           *secrets.KeyRing
	watchersMu        sync.RWMutex
	watchers          map[uuid.UUID]map[uuid.UUID]watcherEntry
	cookieSecure      bool
	publicBaseURL     string
	egressAckTimeout  time.Duration
	mcpUpstreamClient *mcpproxy.UpstreamClient
	codexPlan         *codexplan.Service
	codexPairRL       *auth.RateLimiter
	now               func() time.Time
}

func newServer(cfg Config, logger *slog.Logger) *server {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	params := cfg.Argon2Params
	if params == (auth.Argon2Params{}) {
		params = auth.DefaultArgon2Params()
	}

	jwtSvc := cfg.JWT
	if jwtSvc == nil {
		if key, err := auth.GenerateEd25519Key(); err == nil {
			if svc, svcErr := auth.NewJWTService(key, auth.DefaultAccessTokenTTL, now); svcErr == nil {
				jwtSvc = svc
			}
		} else {
			logger.Error("failed to initialize fallback jwt signer", "err", err)
		}
	}

	var queries *db.Queries
	if cfg.Pool != nil {
		queries = db.New(cfg.Pool)
	}

	authProvider := cfg.AuthProvider
	if authProvider == nil && queries != nil && jwtSvc != nil {
		lp, err := auth.NewLocalProvider(&pgUserStore{queries: queries}, jwtSvc, params)
		if err != nil {
			logger.Error("failed to initialize local auth provider", "err", err)
		} else {
			authProvider = lp
		}
	}

	setupRL := cfg.SetupRateLimiter
	if setupRL == nil {
		setupRL = auth.NewRateLimiter(10, 15*time.Minute, now)
	}
	loginIPRL := cfg.LoginIPRateLimiter
	if loginIPRL == nil {
		loginIPRL = auth.NewRateLimiter(20, time.Minute, now)
	}
	loginEmailRL := cfg.LoginEmailRateLimiter
	if loginEmailRL == nil {
		loginEmailRL = auth.NewRateLimiter(10, 15*time.Minute, now)
	}
	refreshRL := cfg.RefreshRateLimiter
	if refreshRL == nil {
		refreshRL = auth.NewRateLimiter(30, time.Minute, now)
	}

	computeDrv := cfg.Compute
	if computeDrv == nil {
		if memDrv, err := compute.NewMemoryDriver(); err != nil {
			logger.Error("failed to initialize fallback memory compute driver", "err", err)
		} else {
			computeDrv = memDrv
		}
	}

	ticketSigner := cfg.TicketSigner
	if ticketSigner == nil {
		ticketSigner = mustEphemeralTicketSigner()
	}

	keyRing := cfg.KeyRing
	if keyRing == nil {
		keyRing = mustEphemeralKeyRing(logger)
	}

	chatRL := cfg.ChatRateLimiter
	if chatRL == nil {
		chatRL = auth.NewRateLimiter(30, time.Minute, now)
	}
	egressAckTimeout := cfg.EgressAckTimeout
	if egressAckTimeout <= 0 {
		egressAckTimeout = 1500 * time.Millisecond
	}
	return &server{
		chatRL:            chatRL,
		chatEvents:        cfg.ChatEvents,
		turnQueue:         cfg.TurnQueue,
		logger:            logger,
		pool:              cfg.Pool,
		queries:           queries,
		jwt:               jwtSvc,
		authProvider:      authProvider,
		argon2:            params,
		setup:             cfg.Setup,
		setupRL:           setupRL,
		loginIPRL:         loginIPRL,
		loginEmailRL:      loginEmailRL,
		refreshRL:         refreshRL,
		compute:           computeDrv,
		authBroker:        cfg.AuthBroker,
		ticketSigner:      ticketSigner,
		keyRing:           keyRing,
		watchers:          make(map[uuid.UUID]map[uuid.UUID]watcherEntry),
		cookieSecure:      cfg.CookieSecure,
		publicBaseURL:     cfg.PublicBaseURL,
		egressAckTimeout:  egressAckTimeout,
		mcpUpstreamClient: cfg.MCPUpstream,
		codexPlan:         codexplan.NewService(cfg.Pool, keyRing, cfg.CodexPlanOAuth, now),
		codexPairRL:       auth.NewRateLimiter(30, time.Minute, now),
		now:               now,
	}
}

func (s *server) mountRoutes(r chi.Router) {
	r.With(s.requireCSRF).Post("/api/codex-plan/pairing/complete", s.handleCompleteCodexPlan)

	// First-run setup wizard (returns 404 permanently once users exist).
	r.Group(func(r chi.Router) {
		r.Use(s.requireCSRF)
		r.Get("/api/setup/status", s.handleSetupStatus)
		r.Post("/api/setup/complete", s.handleSetupComplete)
	})

	// Unauthenticated / token-cookie auth endpoints.
	r.Group(func(r chi.Router) {
		r.Use(s.requireCSRF)
		r.Post("/api/auth/login", s.handleLogin)
		r.Post("/api/auth/refresh", s.handleRefresh)
		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/invitations/{token}", s.handlePreviewInvitation)
		r.Post("/api/invitations/{token}/accept", s.handleAcceptInvitation)
	})

	// Authenticated control-plane routes.
	r.Group(func(r chi.Router) {
		r.Use(s.requireCSRF)
		r.Use(s.requireAuth)

		r.Get("/api/auth/me", s.handleGetMe)

		r.Get("/api/workspaces", s.handleListWorkspaces)
		r.Post("/api/workspaces", s.handleCreateWorkspace)

		r.Route("/api/workspaces/{workspaceID}", func(r chi.Router) {
			r.Get("/", s.handleGetWorkspace)
			r.Patch("/", s.handleUpdateWorkspace)
			r.Delete("/", s.handleDeleteWorkspace)
			r.Get("/gitlab-instances", s.handleListGitLabInstances)
			r.Get("/bots/{botID}/gitlab-instances", s.handleListBotGitLabInstances)
			r.Put("/bots/{botID}/gitlab-instances", s.handleSetBotGitLabInstances)
			r.Post("/gitlab-instances", s.handleCreateGitLabInstance)
			r.Delete("/gitlab-instances", s.handleDeleteGitLabInstance)

			r.Get("/members", s.handleListMembers)
			r.Patch("/members/{userID}", s.handleUpdateMemberRole)
			r.Delete("/members/{userID}", s.handleRemoveMember)

			r.Get("/invitations", s.handleListInvitations)
			r.Post("/invitations", s.handleCreateInvitation)
			r.Delete("/invitations/{invitationID}", s.handleRevokeInvitation)

			r.Get("/budgets", s.handleBudgetSummary)
			r.Put("/budgets", s.handleSetBudget)
			r.Delete("/budgets", s.handleDeleteBudget)
			r.Put("/subscription-budget-policy", s.handleSetSubscriptionBudgetPolicy)
			r.Get("/model-prices", s.handleListModelPrices)
			r.Put("/model-prices", s.handleSetModelPrice)
			r.Get("/conversations", s.handleListDMs)
			r.Post("/bots/{botID}/dm", s.handleOpenDM)
			r.Get("/conversations/{conversationID}/messages", s.handleChatHistory)
			r.Post("/conversations/{conversationID}/messages", s.handleSendChat)
			r.Post("/conversations/{conversationID}/turns/{turnID}/stop", s.handleStopChatTurn)
			r.Get("/conversations/{conversationID}/messages/{messageID}/tool-image", s.handleChatToolImage)
			r.Get("/groups", s.handleListGroups)
			r.Post("/groups", s.handleCreateGroup)
			r.Get("/groups/{groupID}", s.handleGetGroup)
			r.Patch("/groups/{groupID}", s.handleUpdateGroup)
			r.Delete("/groups/{groupID}", s.handleDeleteGroup)
			r.Get("/groups/{groupID}/messages", s.handleListGroupMessages)
			r.Post("/groups/{groupID}/messages", s.handleSendGroupMessage)
			r.Post("/groups/{groupID}/turns/{turnID}/stop", s.handleStopGroupTurn)
			r.Get("/groups/{groupID}/messages/{messageID}/tool-image", s.handleGroupToolImage)
			r.Post("/groups/{groupID}/read", s.handleMarkGroupRead)
			r.Get("/approvals", s.handleListApprovals)
			r.Post("/approvals/{approvalID}/decision", s.handleDecideApproval)
			r.Post("/secret-requests/{requestID}/fulfill", s.handleFulfillSecretRequest)
			r.Post("/secret-requests/{requestID}/deny", s.handleDenySecretRequest)
			r.Post("/secret-requests/{requestID}/broker-takeover/start", s.handleStartBrokerTakeover)
			r.Get("/secret-requests/{requestID}/broker-takeover/frame", s.handleBrokerTakeoverFrame)
			r.Post("/secret-requests/{requestID}/broker-takeover/input", s.handleBrokerTakeoverInput)
			r.Post("/secret-requests/{requestID}/broker-takeover/complete", s.handleCompleteBrokerTakeover)
			r.Get("/notifications", s.handleListNotifications)
			r.Post("/notifications/read", s.handleMarkNotificationsRead)
			r.Post("/notifications/{notificationID}/read", s.handleMarkNotificationRead)
			r.Get("/bots", s.handleListBots)
			r.Post("/bots", s.handleCreateBot)
			r.Get("/bots/{botID}/runtime", s.handleBotRuntime)
			r.Put("/bots/{botID}/runtime", s.handleBotRuntime)
			r.Get("/bots/{botID}", s.handleGetBot)
			r.Patch("/bots/{botID}", s.handleUpdateBot)
			r.Delete("/bots/{botID}", s.handleDeleteBot)

			r.Get("/bots/{botID}/environment", s.handleGetBotEnvironment)
			r.Post("/bots/{botID}/environment/start", s.handleStartBotEnvironment)
			r.Post("/bots/{botID}/environment/stop", s.handleStopBotEnvironment)
			r.Post("/bots/{botID}/environment/restart", s.handleRestartBotEnvironment)
			r.Post("/bots/{botID}/control/take", s.handleTakeBotControl)
			r.Post("/bots/{botID}/control/release", s.handleReleaseBotControl)
			r.Get("/bots/{botID}/desktop-actions", s.handleListDesktopActions)
			r.Get("/bots/{botID}/desktop-actions/{actionID}/screenshot", s.handleGetDesktopActionScreenshot)
			r.Post("/bots/{botID}/vnc-ticket", s.handleCreateVNCTicket)
			r.Post("/bots/{botID}/terminal/exec", s.handleExecBotTerminal)
			r.Get("/bots/{botID}/files", s.handleListBotFiles)
			r.Get("/bots/{botID}/files/content", s.handleReadBotFile)
			r.Put("/bots/{botID}/files/content", s.handleWriteBotFile)

			r.Get("/secrets", s.handleListWorkspaceSecrets)
			r.Post("/secrets", s.handleCreateWorkspaceSecret)
			r.Put("/secrets/{secretID}", s.handleUpdateWorkspaceSecret)
			r.Delete("/secrets/{secretID}", s.handleDeleteWorkspaceSecret)

			r.Get("/bots/{botID}/codex-plan", s.handleGetCodexPlan)
			r.Post("/bots/{botID}/codex-plan/pairing", s.handleStartCodexPlan)
			r.Delete("/bots/{botID}/codex-plan", s.handleDisconnectCodexPlan)
			r.Get("/bots/{botID}/secrets", s.handleListBotSecrets)
			r.Post("/bots/{botID}/secrets", s.handleCreateBotSecret)
			r.Put("/bots/{botID}/secrets/{secretID}", s.handleUpdateBotSecret)
			r.Delete("/bots/{botID}/secrets/{secretID}", s.handleDeleteBotSecret)

			r.Get("/bots/{botID}/egress", s.handleGetBotEgressPolicy)
			r.Patch("/bots/{botID}/egress", s.handleUpdateBotEgressPolicy)
			r.Get("/bots/{botID}/egress/logs", s.handleListBotEgressLogs)

			r.Get("/mcp-servers", s.handleListMCPServers)
			r.Post("/mcp-servers", s.handleCreateMCPServer)
			r.Patch("/mcp-servers/{serverID}", s.handleUpdateMCPServer)
			r.Delete("/mcp-servers/{serverID}", s.handleDeleteMCPServer)
			r.Post("/mcp-servers/{serverID}/sync", s.handleSyncMCPServer)

			r.Get("/guardrails", s.handleGetWorkspaceGuardrails)
			r.Put("/guardrails/policy", s.handleUpdateWorkspaceGuardrailPolicy)
			r.Post("/guardrails/rules", s.handleCreateWorkspaceGuardrailRule)
			r.Patch("/guardrails/rules/{ruleID}", s.handleUpdateWorkspaceGuardrailRule)
			r.Delete("/guardrails/rules/{ruleID}", s.handleDeleteWorkspaceGuardrailRule)
			r.Post("/guardrails/test", s.handleTestWorkspaceGuardrailRule)
			r.Get("/guardrails/events", s.handleListWorkspaceGuardrailEvents)

			r.Get("/bots/{botID}/mcp", s.handleGetBotMCP)
			r.Put("/bots/{botID}/mcp/{serverID}", s.handleUpdateBotMCPServer)
			r.Get("/bots/{botID}/mcp/logs", s.handleListBotMCPLogs)

			r.Get("/bots/{botID}/memories", s.handleListBotMemories)
			r.Post("/bots/{botID}/memories", s.handleCreateBotMemory)
			r.Delete("/bots/{botID}/memories", s.handleClearBotMemories)
			r.Put("/bots/{botID}/memories/policy", s.handleUpdateBotMemoryPolicy)
			r.Patch("/bots/{botID}/memories/{memoryID}", s.handleUpdateBotMemory)
			r.Delete("/bots/{botID}/memories/{memoryID}", s.handleDeleteBotMemory)
		})
	})
}

func (s *server) ensureDB(w http.ResponseWriter) bool {
	if s.pool == nil || s.queries == nil {
		writeError(w, s.logger, http.StatusServiceUnavailable, "dependency_unavailable", "database connection is unavailable")
		return false
	}
	return true
}
