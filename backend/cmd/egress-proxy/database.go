package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// egressDeps is the per-bot proxy surface. AuthMode must not return credential material.
type egressDeps struct {
	Connect  http.Handler
	LLM      http.Handler
	AuthMode func(context.Context) (string, error)
	Health   func(context.Context) error
	CACert   []byte
}

func databaseHandler(pool *pgxpool.Pool, ring *secrets.KeyRing, workspace, bot uuid.UUID, ca *mitmproxy.BotCA, logger *slog.Logger, signal notify.Signal) (http.Handler, error) {
	if pool == nil || ring == nil || workspace == uuid.Nil || bot == uuid.Nil || ca == nil {
		return nil, errors.New("proxy configuration incomplete")
	}
	connect, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{Pool: pool, KeyRing: ring, WorkspaceID: workspace, BotID: bot, CA: ca, Logger: logger, Notify: signal})
	if err != nil {
		return nil, err
	}
	resolve := secrets.CLIProviderResolver(db.New(pool), ring, workspace, bot)
	llm, err := llmproxy.NewScopedHandler(workspace.String(), bot.String(), resolve, &llmproxy.Options{Logger: logger, Admit: budgets.ProxyAdmission(pool, workspace, bot, time.Now), Client: &http.Client{Timeout: 10 * time.Minute}})
	if err != nil {
		return nil, err
	}
	return newEgressHandler(egressDeps{
		Connect: connect,
		LLM:     llm,
		AuthMode: func(ctx context.Context) (string, error) {
			return llmproxy.AuthMode(ctx, resolve, bot.String(), llmproxy.ProviderAnthropic)
		},
		Health: pool.Ping,
		CACert: ca.CertPEM(),
	}), nil
}

func newEgressHandler(deps egressDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			// Native provider endpoints must use the metered LLM routes. Tunneling
			// them would bypass request settlement, even under an open egress policy.
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil || isLLMHost(host) {
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
		// The desktop CLI asks which placeholder to present. The answer is a
		// mode name only; the encrypted token stays in this process.
		if r.Method == http.MethodGet && r.URL.Path == "/llm/anthropic/auth-mode" {
			mode := ""
			var err error
			if deps.AuthMode != nil {
				mode, err = deps.AuthMode(r.Context())
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
		http.NotFound(w, r)
	})
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

func openDatabaseProxy(ctx context.Context, dsn, keySpec, workspace, bot, natsURL string, ca *mitmproxy.BotCA, logger *slog.Logger) (http.Handler, func(), error) {
	ws, err := uuid.Parse(workspace)
	if err != nil {
		return nil, nil, errors.New("invalid proxy workspace identity")
	}
	botID, err := uuid.Parse(bot)
	if err != nil {
		return nil, nil, errors.New("invalid proxy bot identity")
	}
	ring, err := secrets.ParseKeyRing(keySpec)
	if err != nil {
		return nil, nil, errors.New("invalid proxy encryption configuration")
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
		}
	}
	//nolint:contextcheck // handlers receive independent HTTP request contexts after startup
	handler, err := databaseHandler(pool, ring, ws, botID, ca, logger, signal)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	return handler, closeAll, nil
}
