package api

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	logger        *slog.Logger
	pool          *pgxpool.Pool
	queries       *db.Queries
	jwt           *auth.JWTService
	authProvider  auth.Provider
	argon2        auth.Argon2Params
	setup         *auth.SetupManager
	setupRL       *auth.RateLimiter
	loginIPRL     *auth.RateLimiter
	loginEmailRL  *auth.RateLimiter
	refreshRL     *auth.RateLimiter
	compute       compute.Driver
	ticketSigner  *vncrelay.Signer
	watchersMu    sync.RWMutex
	watchers      map[uuid.UUID]map[uuid.UUID]watcherEntry
	cookieSecure  bool
	publicBaseURL string
	now           func() time.Time
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

	return &server{
		logger:        logger,
		pool:          cfg.Pool,
		queries:       queries,
		jwt:           jwtSvc,
		authProvider:  authProvider,
		argon2:        params,
		setup:         cfg.Setup,
		setupRL:       setupRL,
		loginIPRL:     loginIPRL,
		loginEmailRL:  loginEmailRL,
		refreshRL:     refreshRL,
		compute:       computeDrv,
		ticketSigner:  ticketSigner,
		watchers:      make(map[uuid.UUID]map[uuid.UUID]watcherEntry),
		cookieSecure:  cfg.CookieSecure,
		publicBaseURL: cfg.PublicBaseURL,
		now:           now,
	}
}

func (s *server) mountRoutes(r chi.Router) {
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

			r.Get("/members", s.handleListMembers)
			r.Patch("/members/{userID}", s.handleUpdateMemberRole)
			r.Delete("/members/{userID}", s.handleRemoveMember)

			r.Get("/invitations", s.handleListInvitations)
			r.Post("/invitations", s.handleCreateInvitation)
			r.Delete("/invitations/{invitationID}", s.handleRevokeInvitation)

			r.Get("/bots", s.handleListBots)
			r.Post("/bots", s.handleCreateBot)
			r.Get("/bots/{botID}", s.handleGetBot)
			r.Patch("/bots/{botID}", s.handleUpdateBot)
			r.Delete("/bots/{botID}", s.handleDeleteBot)

			r.Get("/bots/{botID}/environment", s.handleGetBotEnvironment)
			r.Post("/bots/{botID}/environment/start", s.handleStartBotEnvironment)
			r.Post("/bots/{botID}/environment/stop", s.handleStopBotEnvironment)
			r.Post("/bots/{botID}/environment/restart", s.handleRestartBotEnvironment)
			r.Post("/bots/{botID}/control/take", s.handleTakeBotControl)
			r.Post("/bots/{botID}/control/release", s.handleReleaseBotControl)
			r.Post("/bots/{botID}/vnc-ticket", s.handleCreateVNCTicket)
			r.Post("/bots/{botID}/terminal/exec", s.handleExecBotTerminal)
			r.Get("/bots/{botID}/files", s.handleListBotFiles)
			r.Get("/bots/{botID}/files/content", s.handleReadBotFile)
			r.Put("/bots/{botID}/files/content", s.handleWriteBotFile)
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
