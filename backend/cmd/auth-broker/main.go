// Package main runs the standalone bot.Box browser authentication broker outside
// all bot sandboxes so credential-bearing Chromium profiles, cookies, and CDP
// sockets never enter sandbox containers.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := envOrDefault("AUTH_BROKER_ADDR", "127.0.0.1:8085")
	token := strings.TrimSpace(os.Getenv("AUTH_BROKER_TOKEN"))
	cdpURL := strings.TrimSpace(os.Getenv("AUTH_BROKER_CDP_URL"))

	var policyPool *pgxpool.Pool
	var err error
	// Database connectivity is service configuration; individual domains are managed live in the UI.
	if strings.TrimSpace(os.Getenv("DATABASE_URL")) != "" && cdpURL == "" {
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		policyPool, err = pgxpool.New(connectCtx, os.Getenv("DATABASE_URL"))
		if err == nil {
			err = policyPool.Ping(connectCtx)
		}
		cancel()
		if err != nil {
			logger.Error("GitLab policy database unavailable")
			os.Exit(1)
		}
		defer policyPool.Close()
	}

	var browserCmd *exec.Cmd
	if cdpURL == "" {
		cdpURL = "http://127.0.0.1:9223"
		chromeBin := findChromiumBinary()
		if chromeBin == "" {
			logger.Error("no Chromium binary found and AUTH_BROKER_CDP_URL is empty")
			os.Exit(1)
		}
		profileRoot := envOrDefault("AUTH_BROKER_PROFILE_DIR", filepath.Join(os.TempDir(), "botbox-auth-broker-profile"))
		if err := os.MkdirAll(profileRoot, 0o700); err != nil {
			logger.Error("create broker profile directory failed", slog.Any("error", err))
			os.Exit(1)
		}
		args := []string{
			"--no-sandbox",
			"--disable-gpu",
			"--disable-dev-shm-usage",
			"--remote-debugging-address=127.0.0.1",
			"--remote-debugging-port=9223",
			"--user-data-dir=" + profileRoot,
		}
		args = append(args, "--disable-quic", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp")
		if filepath.Base(chromeBin) != "chrome-headless-shell" {
			args = append([]string{"--headless=new"}, args...)
		}
		args = append(args, "about:blank")
		// Keep DevTools bound strictly to loopback inside the isolated broker host/container.
		browserCmd = exec.Command(chromeBin, args...) //nolint:gosec // controlled Chromium binary lookup for isolated broker
		if err := browserCmd.Start(); err != nil {
			logger.Error("failed to start isolated broker chromium", slog.Any("error", err))
			os.Exit(1)
		}
		defer func() {
			if browserCmd.Process != nil {
				_ = browserCmd.Process.Kill()
				_, _ = browserCmd.Process.Wait()
			}
		}()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		waitErr := waitForCDP(waitCtx, cdpURL)
		waitCancel()
		if waitErr != nil {
			logger.Warn("chromium devtools endpoint not ready before server listen", slog.Any("error", waitErr))
		} else {
			logger.Info("isolated broker chromium ready", slog.String("bin", filepath.Base(chromeBin)))
		}
	}

	engine := authbroker.NewCDPBrowsingEngine(cdpURL)
	svc := authbroker.NewService(engine)
	if policyPool != nil {
		svc.SetGitLabPolicy(func(ctx context.Context, ws, bot, session uuid.UUID, origin string) error {
			return authbroker.ValidateGitLabAccess(ctx, db.New(policyPool), ws, bot, session, origin)
		})
	}
	handler, err := authbroker.NewHTTPHandler(svc, token)
	if err != nil {
		logger.Error("init auth broker handler failed", slog.Any("error", err))
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("auth-broker listening", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("auth-broker listen failed", slog.Any("error", err))
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func waitForCDP(ctx context.Context, cdpURL string) error {
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	probe := strings.TrimRight(cdpURL, "/") + "/json/version"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe, nil)
		if err == nil {
			resp, doErr := client.Do(req) //nolint:gosec // local loopback Chromium DevTools endpoint
			if doErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func findChromiumBinary() string {
	if v := strings.TrimSpace(os.Getenv("AUTH_BROKER_CHROMIUM_BIN")); v != "" {
		return v
	}
	for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome", "chrome-headless-shell"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	if pwRoot := strings.TrimSpace(os.Getenv("PLAYWRIGHT_BROWSERS_PATH")); pwRoot != "" {
		if bin := findPlaywrightChromium(pwRoot); bin != "" {
			return bin
		}
	}
	return ""
}

func findPlaywrightChromium(root string) string {
	names := []string{"chrome-headless-shell", "Google Chrome for Testing", "Chromium", "chrome", "chromium"}
	roots := []string{filepath.Clean(root)}
	if entries, err := os.ReadDir(filepath.Clean(root)); err == nil { //nolint:gosec // scans configured PLAYWRIGHT_BROWSERS_PATH
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "chromium") {
				continue
			}
			full := filepath.Join(filepath.Clean(root), entry.Name())
			if resolved, rErr := filepath.EvalSymlinks(full); rErr == nil {
				roots = append(roots, resolved)
			} else {
				roots = append(roots, full)
			}
		}
	}
	var best string
	for _, base := range roots {
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error { //nolint:gosec // scans configured PLAYWRIGHT_BROWSERS_PATH for Chromium executable
			if err != nil || d.IsDir() {
				return nil
			}
			if !slices.Contains(names, d.Name()) {
				return nil
			}
			info, iErr := d.Info()
			if iErr != nil || info.Mode()&0o111 == 0 {
				return nil
			}
			if d.Name() == "chrome-headless-shell" {
				best = path
				return filepath.SkipAll
			}
			if best == "" {
				best = path
			}
			return nil
		})
		if best != "" && filepath.Base(best) == "chrome-headless-shell" {
			return best
		}
	}
	return best
}
