// SPDX-License-Identifier: AGPL-3.0-only

// Package main runs the standalone bot.Box browser authentication broker outside
// all bot sandboxes so credential-bearing Chromium profiles, cookies, and CDP
// sockets never enter sandbox containers.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
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
	if err := run(logger); err != nil {
		logger.Error("auth-broker stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := envOrDefault("AUTH_BROKER_ADDR", "127.0.0.1:8085")
	token := strings.TrimSpace(os.Getenv("AUTH_BROKER_TOKEN"))
	cdpURL := strings.TrimSpace(os.Getenv("AUTH_BROKER_CDP_URL"))

	checkOnly := len(os.Args) == 2 && os.Args[1] == "--check-sandbox"
	var policyPool *pgxpool.Pool
	var err error
	// Database connectivity is service configuration; individual domains are managed live in the UI.
	if !checkOnly && cdpURL == "" && strings.TrimSpace(os.Getenv("DATABASE_URL")) != "" {
		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		policyPool, err = pgxpool.New(connectCtx, os.Getenv("DATABASE_URL"))
		if err == nil {
			err = policyPool.Ping(connectCtx)
		}
		cancel()
		if err != nil {
			return errors.New("GitLab policy database unavailable")
		}
		defer policyPool.Close()
	}

	var browserDone <-chan struct{}
	if cdpURL == "" {
		chromeBin := findChromiumBinary()
		if chromeBin == "" {
			return errors.New("browser binary unavailable")
		}
		profileRoot := envOrDefault("AUTH_BROKER_PROFILE_DIR", filepath.Join(os.TempDir(), "botbox-auth-broker-profile"))
		if err := os.MkdirAll(profileRoot, 0o700); err != nil {
			return errors.New("create broker profile root")
		}
		profile, err := os.MkdirTemp(profileRoot, "browser-")
		if err != nil {
			return errors.New("create isolated browser profile")
		}
		defer os.RemoveAll(profile) //nolint:errcheck // best-effort cleanup after stopping Chromium

		browserCmd := exec.CommandContext(ctx, chromeBin, chromiumArgs(profile)...) //nolint:gosec // operator-configured Chromium
		// The renderer and browser must not inherit DB or broker credentials.
		browserCmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=en_US.UTF-8", "HOME=" + profile}
		if err := browserCmd.Start(); err != nil {
			return errors.New("start isolated Chromium")
		}
		done := make(chan struct{})
		go func() {
			_ = browserCmd.Wait()
			close(done)
		}()
		browserDone = done
		defer func() {
			_ = browserCmd.Process.Kill()
			<-done
		}()
		waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cdpURL, err = waitForBrowserPort(waitCtx, done, profile)
		cancel()
		if err != nil {
			return err
		}
	}
	if !validCDPEndpoint(cdpURL) {
		return errors.New("browser CDP must use a private loopback endpoint")
	}

	engine := authbroker.NewCDPBrowsingEngine(cdpURL)
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = engine.VerifyRendererSandbox(verifyCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("verify Chromium renderer sandbox: %w", err)
	}
	if checkOnly {
		logger.Info("Chromium renderer sandbox verified")
		return nil
	}
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
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverDone := make(chan error, 1)
	go func() {
		logger.Info("auth-broker listening", "addr", addr)
		serverDone <- srv.ListenAndServe()
	}()

	var failure error
	select {
	case <-ctx.Done():
	case <-browserDone:
		failure = errors.New("verified Chromium exited")
	case failure = <-serverDone:
	}
	stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	return failure
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func chromiumArgs(profile string) []string {
	args := []string{
		"--disable-setuid-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--lang=en-US",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--user-data-dir=" + profile, "--disable-quic", "--allow-chrome-scheme-url",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
	}
	return append(args, "--headless=new", "about:blank")
}

func validCDPEndpoint(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" ||
		endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return false
	}
	host, err := netip.ParseAddr(endpoint.Hostname())
	port, portErr := strconv.ParseUint(endpoint.Port(), 10, 16)
	return err == nil && host.IsLoopback() && portErr == nil && port != 0
}

func waitForBrowserPort(ctx context.Context, done <-chan struct{}, profile string) (string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if content, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil { //nolint:gosec // private Chromium profile
			port, _, _ := strings.Cut(string(content), "\n")
			if parsed, err := strconv.ParseUint(port, 10, 16); err == nil && parsed != 0 {
				return "http://" + net.JoinHostPort("127.0.0.1", port), nil
			}
		}
		select {
		case <-done:
			return "", errors.New("browser exited before opening its private CDP endpoint")
		case <-ctx.Done():
			return "", errors.New("browser CDP startup timed out")
		case <-ticker.C:
		}
	}
}

func findChromiumBinary() string {
	if v := strings.TrimSpace(os.Getenv("AUTH_BROKER_CHROMIUM_BIN")); v != "" {
		return v
	}
	for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome"} {
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
	names := []string{"Google Chrome for Testing", "Chromium", "chrome", "chromium"}
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
			best = path
			return filepath.SkipAll
		})
		if best != "" {
			return best
		}
	}
	return best
}
