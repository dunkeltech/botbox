// Command api runs the bot.Box REST and WebSocket control plane server.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("api server exited with error", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := envOrDefault("API_LISTEN_ADDR", "127.0.0.1:8080")
	publicBaseURL := envOrDefault("PUBLIC_BASE_URL", "http://127.0.0.1:3000")
	cookieSecure := strings.HasPrefix(strings.ToLower(publicBaseURL), "https://")
	if strings.EqualFold(os.Getenv("BOTBOX_COOKIE_SECURE"), "true") {
		cookieSecure = true
	} else if strings.EqualFold(os.Getenv("BOTBOX_COOKIE_SECURE"), "false") {
		cookieSecure = false
	}

	cfg := api.Config{
		Logger:        logger,
		CookieSecure:  cookieSecure,
		PublicBaseURL: publicBaseURL,
	}

	var pool *pgxpool.Pool
	if dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL")); dbURL != "" {
		var err error
		pool, err = pgxpool.New(ctx, dbURL)
		if err != nil {
			return err
		}
		defer pool.Close()

		if err := waitForPostgres(ctx, pool); err != nil {
			return err
		}

		if err := migrations.MigrateUp(ctx, pool); err != nil {
			return err
		}

		queries := db.New(pool)
		userCount, err := queries.CountUsers(ctx)
		if err != nil {
			return err
		}

		setupMgr := auth.NewSetupManager(os.Stdout)
		if _, _, err := setupMgr.EnsureSetupSecret(ctx, userCount, os.Getenv("BOTBOX_SETUP_SECRET"), logger); err != nil {
			return err
		}

		jwtKey, err := loadSigningKey(logger, os.Getenv("BOTBOX_JWT_PRIVATE_KEY"))
		if err != nil {
			return err
		}
		jwtSvc, err := auth.NewJWTService(jwtKey, auth.DefaultAccessTokenTTL, time.Now)
		if err != nil {
			return err
		}

		cfg.Pool = pool
		cfg.Setup = setupMgr
		cfg.JWT = jwtSvc
		cfg.ReadinessCheck = func(checkCtx context.Context) error {
			return pool.Ping(checkCtx)
		}

		kekEnv := os.Getenv("BOTBOX_SECRETS_KEK")
		kekFile := envOrDefault("BOTBOX_SECRETS_KEK_FILE", filepath.Join(os.TempDir(), "botbox-secrets.kek"))
		keyRing, generatedKEK, kekErr := secrets.LoadOrGenerateKeyRing(kekEnv, kekFile)
		if kekErr != nil {
			return kekErr
		}
		if generatedKEK {
			logger.Warn("BOTBOX_SECRETS_KEK is unset; generated local dev KEK file", "path", kekFile)
		}
		cfg.KeyRing = keyRing

		computeDrv, closeCompute, drvErr := initComputeDriver(logger)
		if drvErr != nil {
			return drvErr
		}
		if closeCompute != nil {
			defer closeCompute()
		}
		cfg.Compute = computeDrv
		if natsURL := strings.TrimSpace(os.Getenv("NATS_URL")); natsURL != "" {
			worker := &engine.Worker{Pool: pool, Execute: modelExecutor(pool, queries, keyRing, computeDrv)}
			ready := make(chan struct{})
			queue, queueErr := events.OpenQueue(natsURL, func(ctx context.Context, job events.TurnNotification) error {
				select {
				case <-ready:
					return worker.Process(ctx, job)
				case <-ctx.Done():
					return ctx.Err()
				}
			}, worker.Interrupt)
			if queueErr != nil {
				return fmt.Errorf("initialize turn queue: %w", queueErr)
			}
			defer queue.Close()
			worker.Enqueue = queue.Enqueue
			worker.Publish = func(eventCtx context.Context, session engine.Session, event engine.Event) error {
				data, err := json.Marshal(event)
				if err != nil {
					return err
				}
				return queue.PublishConversation(eventCtx, session.WorkspaceID, session.ConversationID, data)
			}
			close(ready)
			cfg.ReadinessCheck = func(checkCtx context.Context) error {
				if err := pool.Ping(checkCtx); err != nil {
					return err
				}
				return queue.Healthy()
			}
			cfg.ChatEvents = queue
			cfg.TurnQueue = queue
		}

		if vncSecret := strings.TrimSpace(os.Getenv("BOTBOX_VNC_SECRET")); len(vncSecret) >= 16 {
			signer, signErr := vncrelay.NewSigner([]byte(vncSecret))
			if signErr != nil {
				return signErr
			}
			cfg.TicketSigner = signer
		} else if len(jwtKey) >= 32 {
			signer, signErr := vncrelay.NewSigner(jwtKey.Seed())
			if signErr == nil {
				cfg.TicketSigner = signer
			}
		}

		if recErr := api.ReconcileEnvironments(ctx, pool, computeDrv, logger); recErr != nil {
			logger.Warn("environment startup reconcile reported error", "err", recErr)
		}
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewHandlerWithConfig(cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func waitForPostgres(ctx context.Context, pool *pgxpool.Pool) error {
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	lastErr := pool.Ping(pingCtx)
	if lastErr == nil {
		return nil
	}

	// devenv up starts postgres and the API together, so retry until the
	// configured DATABASE_URL accepts connections.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-pingCtx.Done():
			return lastErr
		case <-ticker.C:
			lastErr = pool.Ping(pingCtx)
			if lastErr == nil {
				return nil
			}
		}
	}
}

func loadSigningKey(logger *slog.Logger, encodedSeed string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(encodedSeed) != "" {
		return auth.ParseEd25519Seed(encodedSeed)
	}
	logger.Warn("BOTBOX_JWT_PRIVATE_KEY is unset; using ephemeral signing key")
	return auth.GenerateEd25519Key()
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func initComputeDriver(logger *slog.Logger) (compute.Driver, func(), error) {
	driverName := strings.ToLower(strings.TrimSpace(envOrDefault("BOTBOX_COMPUTE_DRIVER", compute.DefaultDriverDocker)))
	if driverName != compute.DefaultDriverDocker && driverName != "memory" {
		return nil, nil, errors.New("unsupported compute driver")
	}
	if driverName == compute.DefaultDriverDocker {
		proxyDB, err := proxyDatabaseURL(os.Getenv("DATABASE_URL"), os.Getenv("BOTBOX_EGRESS_DATABASE_URL"))
		if err != nil {
			return nil, nil, err
		}
		var proxy *docker.ProxyOptions
		if proxyDB != "" {
			keySpec := os.Getenv("BOTBOX_SECRETS_KEK")
			if keySpec == "" {
				data, err := os.ReadFile(envOrDefault("BOTBOX_SECRETS_KEK_FILE", filepath.Join(os.TempDir(), "botbox-secrets.kek")))
				if err != nil {
					return nil, nil, errors.New("proxy encryption configuration unavailable")
				}
				keySpec = strings.TrimSpace(string(data))
			}
			proxy = &docker.ProxyOptions{Image: envOrDefault("BOTBOX_EGRESS_IMAGE", "botbox/egress-proxy:dev"), DatabaseURL: proxyDB, KeySpec: keySpec}
		}
		drv, err := docker.NewDriver(docker.Options{SocketPath: os.Getenv("DOCKER_SOCKET"), Proxy: proxy})
		if err != nil {
			return nil, nil, err
		}
		logger.Info("initialized docker compute driver")
		return drv, nil, nil
	}
	memDrv, err := compute.NewMemoryDriver()
	if err != nil {
		return nil, nil, err
	}
	logger.Info("initialized in-process memory compute driver")
	return memDrv, memDrv.Close, nil
}
