// SPDX-License-Identifier: AGPL-3.0-only

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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/observability"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(observability.NewScrubbingHandler(slog.NewJSONHandler(os.Stdout, nil)))
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

	trustedProxies, err := api.ParseTrustedProxyCIDRs(os.Getenv("BOTBOX_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return fmt.Errorf("parse BOTBOX_TRUSTED_PROXY_CIDRS: %w", err)
	}

	shutdownTracing, err := observability.InitTracing(ctx, "botbox-api")
	if err != nil {
		return fmt.Errorf("initialize tracing: %w", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(shutCtx)
	}()

	metrics := observability.NewMetrics()
	shutdownMetrics, err := observability.StartMetricsServer(ctx, os.Getenv("BOTBOX_METRICS_ADDR"), metrics, logger)
	if err != nil {
		return fmt.Errorf("start metrics server: %w", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownMetrics(shutCtx)
	}()

	defaultIdleTimeout, err := durationEnvOrDefault("BOTBOX_SANDBOX_IDLE_TIMEOUT", 60*time.Minute)
	if err != nil {
		return fmt.Errorf("parse BOTBOX_SANDBOX_IDLE_TIMEOUT: %w", err)
	}
	idleReapInterval, err := durationEnvOrDefault("BOTBOX_IDLE_REAP_INTERVAL", time.Minute)
	if err != nil {
		return fmt.Errorf("parse BOTBOX_IDLE_REAP_INTERVAL: %w", err)
	}
	reconcileInterval, err := durationEnvOrDefault("BOTBOX_RECONCILE_INTERVAL", 5*time.Minute)
	if err != nil {
		return fmt.Errorf("parse BOTBOX_RECONCILE_INTERVAL: %w", err)
	}
	maxRunningSandboxes, err := nonNegativeIntEnv("BOTBOX_MAX_RUNNING_SANDBOXES")
	if err != nil {
		return fmt.Errorf("parse BOTBOX_MAX_RUNNING_SANDBOXES: %w", err)
	}
	maxSandboxMemoryMiB, err := nonNegativeIntEnv("BOTBOX_MAX_SANDBOX_MEMORY_MIB")
	if err != nil {
		return fmt.Errorf("parse BOTBOX_MAX_SANDBOX_MEMORY_MIB: %w", err)
	}

	cfg := api.Config{
		Logger:                    logger,
		CookieSecure:              cookieSecure,
		PublicBaseURL:             publicBaseURL,
		TrustedProxyCIDRs:         trustedProxies,
		DefaultSandboxIdleTimeout: defaultIdleTimeout,
		MaxRunningSandboxes:       maxRunningSandboxes,
		MaxSandboxMemoryMiB:       maxSandboxMemoryMiB,
		Metrics:                   metrics,
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

		var authBroker authbroker.Client
		if brokerURL := strings.TrimSpace(os.Getenv("BOTBOX_AUTH_BROKER_URL")); brokerURL != "" {
			brokerToken := strings.TrimSpace(os.Getenv("BOTBOX_AUTH_BROKER_TOKEN"))
			client, bErr := authbroker.NewHTTPClient(brokerURL, brokerToken)
			if bErr != nil {
				return fmt.Errorf("initialize auth broker client: %w", bErr)
			}
			authBroker = client
		}
		computeDrv, closeCompute, drvErr := initComputeDriverWithBroker(logger, authBroker != nil)
		if drvErr != nil {
			return drvErr
		}
		if closeCompute != nil {
			defer closeCompute()
		}
		if dockerDriver, ok := computeDrv.(*docker.Driver); ok {
			rows, err := queries.ListEnvironmentsForReconcile(ctx)
			if err != nil {
				return fmt.Errorf("list environments for proxy upgrade: %w", err)
			}
			workspacesByBot := make(map[string]string, len(rows))
			for _, row := range rows {
				workspacesByBot[row.BotID.String()] = row.WorkspaceID.String()
			}
			if err := dockerDriver.UpgradeProxyIngress(ctx, workspacesByBot); err != nil {
				return fmt.Errorf("upgrade bot proxy ingress: %w", err)
			}
		}
		cfg.AuthBroker = authBroker
		cfg.Compute = computeDrv
		if natsURL := strings.TrimSpace(os.Getenv("NATS_URL")); natsURL != "" {
			worker := &engine.Worker{Pool: pool, KeyRing: keyRing, AuthBroker: authBroker, Metrics: metrics, Execute: cliExecutor(pool, queries, keyRing, computeDrv, authBroker != nil)}
			ready := make(chan struct{})
			concurrency, err := turnConcurrency(os.Getenv("BOTBOX_TURN_CONCURRENCY"))
			if err != nil {
				return err
			}
			queue, queueErr := events.OpenQueueWithConcurrency(natsURL, concurrency, func(ctx context.Context, job events.TurnNotification) error {
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
			worker.Signal = func(eventCtx context.Context, ws, conversation uuid.UUID, ids []uuid.UUID) {
				data, err := notify.MarshalSignal(conversation, ids)
				if err != nil {
					logger.Error("encode notification signal", "err", err)
					return
				}
				if err = queue.PublishConversation(eventCtx, ws, conversation, data); err != nil {
					// A missed signal is recovered by reloading the list (issue #17).
					logger.Error("publish notification signal", "err", err)
				}
			}
			worker.Publish = func(eventCtx context.Context, session engine.Session, event engine.Event) error {
				data, err := json.Marshal(event)
				if err != nil {
					return err
				}
				return queue.PublishConversation(eventCtx, session.WorkspaceID, session.ConversationID, data)
			}
			close(ready)
			inputCtx, stopInput := context.WithCancel(ctx)
			inputDone := make(chan struct{})
			defer func() { stopInput(); <-inputDone }()
			go func() {
				defer close(inputDone)
				collectGroupInputs(inputCtx, worker, logger)
			}()
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

	rt, handler := api.NewRuntimeHandler(cfg)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	var bgStop context.CancelFunc
	bgDone := make(chan struct{})
	if pool != nil {
		bgCtx, cancelBg := context.WithCancel(ctx)
		bgStop = cancelBg
		queries := db.New(pool)
		go func() {
			defer close(bgDone)
			runMaintenanceLoops(bgCtx, queries, cfg.Compute, rt, metrics, idleReapInterval, reconcileInterval, logger)
		}()
	} else {
		close(bgDone)
	}
	stopBackground := func() {
		if bgStop != nil {
			bgStop()
		}
		<-bgDone
	}
	defer stopBackground()

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
		// Stop periodic background loops first so in-flight DB queries drain before
		// HTTP shutdown, deferred queue close, metrics/tracing flush, and pool close.
		stopBackground()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runMaintenanceLoops(
	ctx context.Context,
	queries *db.Queries,
	computeDrv compute.Driver,
	rt *api.Runtime,
	metrics *observability.Metrics,
	idleReapInterval, reconcileInterval time.Duration,
	logger *slog.Logger,
) {
	var wg sync.WaitGroup
	if metrics != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			collectMetricsGauges(ctx, queries, metrics, logger)
		}()
	}
	if rt != nil && idleReapInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runPeriodicTask(ctx, idleReapInterval, "idle sandbox reap", func(passCtx context.Context) error {
				stopped, err := rt.ReapIdleEnvironments(passCtx)
				if err == nil && stopped > 0 && logger != nil {
					logger.Info("auto-stopped idle sandboxes", "count", stopped)
				}
				return err
			}, logger)
		}()
	}
	if computeDrv != nil && queries != nil && reconcileInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runPeriodicTask(ctx, reconcileInterval, "periodic environment reconcile", func(passCtx context.Context) error {
				return rt.ReconcileEnvironments(passCtx)
			}, logger)
		}()
	}
	<-ctx.Done()
	wg.Wait()
}

func runPeriodicTask(ctx context.Context, interval time.Duration, name string, fn func(context.Context) error, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := fn(passCtx)
			cancel()
			if err != nil && ctx.Err() == nil && logger != nil {
				logger.Warn("periodic task failed", "task", name, "err", err)
			}
		}
	}
}

func collectMetricsGauges(ctx context.Context, queries *db.Queries, metrics *observability.Metrics, logger *slog.Logger) {
	if queries == nil || metrics == nil {
		return
	}
	refresh := func() {
		passCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		for _, state := range []string{"stopped", "starting", "running", "stopping", "error"} {
			metrics.SandboxesByState.WithLabelValues(state).Set(0)
		}
		if rows, err := queries.CountEnvironmentsByState(passCtx); err != nil {
			if ctx.Err() == nil && logger != nil {
				logger.Warn("refresh sandbox state metrics failed", "err", err)
			}
		} else {
			for _, row := range rows {
				metrics.SandboxesByState.WithLabelValues(row.Status).Set(float64(row.Total))
			}
		}

		metrics.TurnQueueDepth.Reset()
		if queued, err := queries.CountQueuedTurnsByWorkspace(passCtx); err != nil {
			if ctx.Err() == nil && logger != nil {
				logger.Warn("refresh queue depth metrics failed", "err", err)
			}
		} else {
			for _, row := range queued {
				metrics.TurnQueueDepth.WithLabelValues(uuid.UUID(row.WorkspaceID.Bytes).String()).Set(float64(row.Total))
			}
		}
	}

	refresh()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func collectGroupInputs(ctx context.Context, worker *engine.Worker, logger *slog.Logger) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := worker.CollectGroupInputs(pass)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("group input collection failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
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

func durationEnvOrDefault(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid non-negative duration %q", raw)
	}
	return d, nil
}

func nonNegativeIntEnv(key string) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid non-negative integer %q", raw)
	}
	return n, nil
}

func initComputeDriver(logger *slog.Logger) (compute.Driver, func(), error) {
	return initComputeDriverWithBroker(logger, false)
}

func initComputeDriverWithBroker(logger *slog.Logger, authBrokerEnabled bool) (compute.Driver, func(), error) {
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
			proxy = &docker.ProxyOptions{
				Image:         envOrDefault("BOTBOX_EGRESS_IMAGE", "botbox/egress-proxy:dev"),
				WorkerNetwork: os.Getenv("BOTBOX_EGRESS_WORKER_NETWORK"),
				WorkerMetrics: strings.EqualFold(strings.TrimSpace(os.Getenv("BOTBOX_EGRESS_WORKER_METRICS")), "true"),
				DatabaseURL:   proxyDB,
				KeySpec:       keySpec,
				NATSURL:       proxyNATSURL(os.Getenv("NATS_URL"), os.Getenv("BOTBOX_EGRESS_NATS_URL")),
			}
			if authBrokerEnabled {
				proxy.AuthBrokerURL = proxyNATSURL(os.Getenv("BOTBOX_AUTH_BROKER_URL"), os.Getenv("BOTBOX_AUTH_BROKER_PROXY_URL"))
				proxy.AuthBrokerToken = strings.TrimSpace(os.Getenv("BOTBOX_AUTH_BROKER_TOKEN"))
			}
		}
		drv, err := docker.NewDriver(docker.Options{
			SocketPath:        os.Getenv("DOCKER_SOCKET"),
			Runtime:           os.Getenv("BOTBOX_DOCKER_RUNTIME"),
			Proxy:             proxy,
			AuthBrokerEnabled: authBrokerEnabled,
			Logger:            logger,
		})
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

func turnConcurrency(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 4, nil
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 1 || count > 64 {
		return 0, errors.New("BOTBOX_TURN_CONCURRENCY must be 1..64")
	}
	return count, nil
}
