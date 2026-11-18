// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

func TestMeterAdmissionAndSettlementFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, path, method string
		admitErr                 error
		upstream, status         int
		known                    bool
	}{
		{name: "success", body: `{"model":"fixture"}`, path: "/v1/responses", method: "POST", upstream: 200, status: 200, known: true},
		{name: "missing model", body: `{}`, path: "/v1/responses", method: "POST", status: 400},
		{name: "wrong model type", body: `{"model":12}`, path: "/v1/responses", method: "POST", status: 400},
		{name: "wrong method", body: `{"model":"fixture"}`, path: "/v1/responses", method: "GET", status: 403},
		{name: "unpriced endpoint", body: `{"model":"fixture"}`, path: "/v1/files", method: "POST", status: 403},
		{name: "budget", body: `{"model":"fixture"}`, path: "/v1/responses", method: "POST", admitErr: llmproxy.ErrBudgetExceeded, status: 429},
		{name: "dependency", body: `{"model":"fixture"}`, path: "/v1/responses", method: "POST", admitErr: errors.New("fixture-outside-key"), status: 503},
		{name: "provider error", body: `{"model":"fixture"}`, path: "/v1/responses", method: "POST", upstream: 401, status: 401, known: true},
		{name: "provider failure", body: `{"model":"fixture"}`, path: "/v1/responses", method: "POST", upstream: 503, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, settled := 0, 0
			var got llmproxy.Usage
			handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
				return &llmproxy.SecretConfig{UpstreamURL: "https://provider.invalid", AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture-outside-key"}, nil
			}, &llmproxy.Options{
				Admit: func(_ context.Context, provider, model, path string) (func(llmproxy.Usage) error, error) {
					if provider != "openai" || model != "fixture" || path != tc.path {
						t.Error("wrong accounting identity")
					}
					if tc.admitErr != nil {
						return nil, tc.admitErr
					}
					return func(u llmproxy.Usage) error { settled++; got = u; return errors.New("fixture-outside-key") }, nil
				},
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					body := `{"usage":{"input_tokens":10,"output_tokens":3}}`
					if tc.upstream >= 400 {
						body = "fixture-outside-key"
					}
					return &http.Response{StatusCode: tc.upstream, Header: http.Header{"Content-Type": []string{"application/json"}, "Set-Cookie": []string{"fixture-outside-key"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tc.method, "/llm/openai"+tc.path, strings.NewReader(tc.body)))
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "fixture-outside-key") || rec.Header().Get("Set-Cookie") != "" {
				t.Fatal("credential details leaked")
			}
			expected := 0
			if tc.upstream > 0 {
				expected = 1
			}
			if calls != expected || settled != expected || got.Known != tc.known {
				t.Fatalf("calls=%d settlements=%d usage=%+v", calls, settled, got)
			}
		})
	}
}

func TestClaudeSubscriptionCannotFallBackToAPIKey(t *testing.T) {
	for _, oauthSecret := range []bool{false, true} {
		auth := llmproxy.AuthKindAnthropicKey
		if oauthSecret {
			auth = llmproxy.AuthKindClaudeOAuth
		}
		called := false
		handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
			return &llmproxy.SecretConfig{UpstreamURL: "https://provider.invalid", AuthKind: auth, SecretValue: "fixture-private-key"}, nil
		}, &llmproxy.Options{Admit: func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
			called = true
			return func(llmproxy.Usage) error { return nil }, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/llm/anthropic/v1/messages", strings.NewReader(`{"model":"sonnet"}`))
		if oauthSecret {
			req.Header.Set("x-api-key", "botbox-placeholder")
		} else {
			req.Header.Set("Authorization", "Bearer botbox-placeholder")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 403 || called || strings.Contains(rec.Body.String(), "fixture-private-key") {
			t.Fatal("mismatched billing authentication admitted")
		}
	}
}
