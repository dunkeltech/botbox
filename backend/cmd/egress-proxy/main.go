// Package main runs the bot.Box standalone egress proxy serving health checks,
// LLM provider credential brokering, and selective MITM CONNECT tunneling.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := envOrDefault("EGRESS_PROXY_ADDR", "127.0.0.1:8081")

	ca, err := mitmproxy.NewBotCA("egress-proxy-standalone")
	if err != nil {
		logger.Error("create proxy CA failed", slog.Any("error", err))
		os.Exit(1)
	}

	policy := mitmproxy.PolicyAllowlist
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EGRESS_PROXY_POLICY")), string(mitmproxy.PolicyOpen)) {
		policy = mitmproxy.PolicyOpen
	}

	allowed := make(map[string]bool)
	for _, entry := range strings.Split(os.Getenv("EGRESS_PROXY_ALLOWED_HOSTS"), ",") {
		host := strings.ToLower(strings.TrimSpace(entry))
		if host != "" {
			allowed[host] = true
		}
	}

	mitmHandler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:           ca,
		Policy:       policy,
		AllowedHosts: allowed,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("init mitmproxy handler failed", slog.Any("error", err))
		os.Exit(1)
	}

	llmHandler := llmproxy.NewHandler(
		func(_ context.Context, _, provider string) (*llmproxy.SecretConfig, error) {
			envKey := "EGRESS_PROXY_LLM_" + strings.ToUpper(strings.TrimSpace(provider)) + "_KEY"
			val := strings.TrimSpace(os.Getenv(envKey))
			if val == "" {
				return nil, llmproxy.ErrSecretNotFound
			}
			return &llmproxy.SecretConfig{SecretValue: val}, nil
		},
		&llmproxy.Options{Logger: logger},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, writeErr := w.Write([]byte("ok\n")); writeErr != nil {
			logger.Error("write healthz failed", slog.Any("error", writeErr))
		}
	})
	mux.Handle("/llm/", llmHandler)

	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			mitmHandler.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("egress proxy listening", slog.String("addr", addr), slog.String("policy", string(policy)))
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("egress proxy server failed", slog.Any("error", serveErr))
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("egress proxy shutdown failed", slog.Any("error", err))
	}
}

func envOrDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}
