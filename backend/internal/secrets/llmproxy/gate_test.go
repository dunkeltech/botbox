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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestScopedProxyGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gateErr error
		status  int
		calls   int
	}{
		{"allowed", nil, 200, 1},
		{"exhausted", llmproxy.ErrBudgetExceeded, 429, 0},
		{"budget unavailable", errors.New("database password must not leak"), 503, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, resolutions := 0, 0
			handler, err := llmproxy.NewScopedHandler("workspace-a", "bot-a", func(_ context.Context, bot, provider string) (*llmproxy.SecretConfig, error) {
				resolutions++
				if bot != "bot-a" || provider != "openrouter" {
					t.Fatalf("identity %s/%s", bot, provider)
				}
				//nolint:gosec // synthetic credential used only by an in-process fake transport
				return &llmproxy.SecretConfig{UpstreamURL: "https://example.invalid", AuthKind: llmproxy.AuthKindBearer, SecretValue: "test-credential"}, nil
			}, &llmproxy.Options{
				CheckBudget: func(_ context.Context, ws, bot string) error {
					if ws != "workspace-a" || bot != "bot-a" {
						t.Fatal("wrong budget scope")
					}
					return tc.gateErr
				},
				Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("X-Botbox-Bot-ID") != "" {
						t.Fatal("identity forwarded")
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				req := httptest.NewRequest("POST", "/llm/openrouter/v1/chat/completions", strings.NewReader("{}"))
				req.Header.Set("X-Botbox-Bot-ID", "victim")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "password") {
					t.Fatal("dependency details leaked")
				}
			}
			if calls != tc.calls*2 || resolutions != tc.calls*2 {
				t.Fatalf("calls=%d resolutions=%d", calls, resolutions)
			}
		})
	}
	for _, ids := range [][2]string{{"", "bot"}, {"ws", ""}, {" ", "bot"}} {
		if _, err := llmproxy.NewScopedHandler(ids[0], ids[1], func(context.Context, string, string) (*llmproxy.SecretConfig, error) { return nil, nil }, nil); err == nil {
			t.Fatal("missing scope accepted")
		}
	}
}

func TestScopedSecretPathRestriction(t *testing.T) {
	calls := 0
	handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: "https://example.invalid/api", PathPrefix: "/api/v1", AuthKind: llmproxy.AuthKindBearer, SecretValue: strings.Repeat("x", 8)}, nil
	}, &llmproxy.Options{Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/llm/openrouter/v1/chat/completions", 200}, {"/llm/openrouter/v10/chat/completions", 403}, {"/llm/openrouter/other", 403}} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("POST", tc.path, nil))
		if recorder.Code != tc.status {
			t.Fatalf("path=%s status=%d", tc.path, recorder.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("blocked path reached upstream: %d calls", calls)
	}
}
