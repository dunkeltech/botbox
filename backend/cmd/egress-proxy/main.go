// SPDX-License-Identifier: AGPL-3.0-only

// Package main runs the bot.Box standalone egress proxy serving health checks,
// LLM provider credential brokering, and selective MITM CONNECT tunneling.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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
	database := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	var botNetwork netip.Prefix
	if database != "" {
		interfaces, err := net.InterfaceAddrs()
		if err != nil {
			logger.Error("inspect egress proxy interfaces failed")
			os.Exit(1)
		}
		addr, botNetwork, err = botNetworkIngress(os.Getenv("EGRESS_PROXY_BOT_SUBNET"), interfaces)
		if err != nil {
			logger.Error("initialize egress proxy ingress", "error", err)
			os.Exit(1)
		}
	}

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
		PublicOnly:   true,
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

	var handler http.Handler = rootHandler
	if database != "" {
		bound, closeDB, err := openDatabaseProxy(context.Background(), database, os.Getenv("BOTBOX_SECRETS_KEK"), os.Getenv("BOTBOX_WORKSPACE_ID"), os.Getenv("BOTBOX_BOT_ID"), os.Getenv("NATS_URL"), ca, logger)
		if err != nil {
			logger.Error("database proxy initialization failed", "error", err)
			os.Exit(1)
		}
		defer closeDB()
		handler = restrictBotNetwork(botNetwork, bound)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv.BaseContext = func(net.Listener) context.Context { return ctx }

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("egress proxy listener unavailable")
		os.Exit(1)
	}
	go func() {
		logger.Info("egress proxy listening", slog.String("addr", addr), slog.String("policy", string(policy)))
		if serveErr := srv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
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
