// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlanRequiresScopedMeteredResponses(t *testing.T) {
	for _, tt := range []struct {
		method, path, body, auth string
		status                   int
	}{
		{"GET", "/llm/openai-plan/v1/responses", "", "", 403},
		{"POST", "/llm/openai-plan/v1/chat/completions", "{}", "Bearer botbox-placeholder", 403},
		{"POST", "/llm/openai-plan/v1/responses?query=other", "{}", "Bearer botbox-placeholder", 403},
		{"POST", "/llm/openai-plan/v1/responses", "{}", "Bearer forged", 403},
		{"POST", "/llm/openai-plan/v1/responses", `{"model":12,"input":[]}`, "Bearer botbox-placeholder", 400},
		{"POST", "/llm/openai-plan/v1/responses", `{"model":"fixture","input":[],"max_output_tokens":5}`, "Bearer botbox-placeholder", 400},
		{"POST", "/llm/openai-plan/v1/responses", `{"model":"fixture","input":"wrong type"}`, "Bearer botbox-placeholder", 400},
		{"POST", "/llm/openai-plan/v1/responses", strings.Repeat("a", (8<<20)+1), "Bearer botbox-placeholder", 400},
	} {
		h, err := NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*SecretConfig, error) {
			return &SecretConfig{UpstreamURL: "https://api.openai.com", AuthKind: AuthKindOpenAIPlan, SecretValue: "fixture"}, nil
		}, &Options{Admit: func(context.Context, string, string, string) (func(Usage) error, error) {
			t.Fatal("invalid request admitted")
			return nil, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		req.Header.Set("Authorization", tt.auth)
		h.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Fatalf("%s %s: %d want %d", tt.method, tt.path, rec.Code, tt.status)
		}
	}
	h := NewHandler(func(context.Context, string, string) (*SecretConfig, error) {
		t.Fatal("unscoped plan resolved")
		return nil, nil
	}, nil)
	req := httptest.NewRequest("POST", "/llm/openai-plan/v1/responses", strings.NewReader(`{"model":"fixture","input":[]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("unscoped plan accepted: %d", rec.Code)
	}
}

type planRT func(*http.Request) (*http.Response, error)

func (r planRT) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

func TestPlanSafeErrorsAndStreaming(t *testing.T) {
	const secret = "outside-credential-fixture"
	for _, tc := range []struct {
		name, contentType, response, contains string
		status, want                          int
		known                                 bool
	}{
		{"redirect", "text/html", secret, "plan_unavailable", 307, 502, true},
		{"allowance", "application/json", `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"` + secret + `"}}`, "plan_limit", 429, 429, true},
		{"outage", "application/json", secret, "plan_unavailable", 503, 502, false},
		{"missing type with SSE", "", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}}\n\n", "response.completed", 200, 200, true},
		{"missing type with text", "", "provider unavailable", "plan_unavailable", 200, 502, false},
		{"explicit non-stream type", "text/plain", "data: {\"type\":\"response.completed\"}\n\n", "plan_unavailable", 200, 502, false},
		{"truncated", "text/event-stream", "data: {\"type\":\"response.created\"}\n\n", "response.created", 200, 200, false},
		{"stream allowance", "text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"" + secret + "\"}}}\n\n", "plan_limit", 200, 200, false},
		{"stream", "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":4},\"text\":\"" + secret + "\"}}\n\n", "[redacted]", 200, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage Usage
			calls := 0
			client := &http.Client{Transport: planRT(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 || req.URL.Host != "api.openai.com" || req.Header.Get("Authorization") != "Bearer "+secret ||
					req.Header.Get("Cookie") != "" || req.Header.Get("X-Custom") != "" {
					t.Fatal("bad upstream route or credential")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}, "Location": {"https://elsewhere.test/"}}, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}
			h, err := NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*SecretConfig, error) {
				return &SecretConfig{UpstreamURL: "https://api.openai.com", AuthKind: AuthKindOpenAIPlan, SecretValue: secret, PlanRevision: "fixture-revision"}, nil
			}, &Options{Client: client, Admit: func(ctx context.Context, provider, model, path string) (func(Usage) error, error) {
				if provider != ProviderOpenAIPlan || path != "/v1/responses" || !SubscriptionRequest(ctx) || PlanRevision(ctx) != "fixture-revision" {
					t.Fatal("plan admission lost trusted context")
				}
				return func(u Usage) error { usage = u; return nil }, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/llm/openai-plan/v1/responses", strings.NewReader(`{"model":"fixture","input":[]}`))
			req.Header.Set("Authorization", "Bearer botbox-placeholder")
			req.Header.Set("X-Custom", "private")
			req.Header.Set("Cookie", "opaque")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if calls != 1 || rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.contains) || strings.Contains(rec.Body.String(), secret) || usage.Known != tc.known {
				t.Fatalf("status=%d calls=%d usage=%+v body=%s", rec.Code, calls, usage, rec.Body.String())
			}
			if tc.name == "missing type with SSE" &&
				(rec.Header().Get("Content-Type") != "text/event-stream" || usage.InputTokens != 3 || usage.OutputTokens != 4 ||
					!strings.HasPrefix(rec.Body.String(), "event: response.completed\n")) {
				t.Fatalf("promoted stream lost headers, events, or usage: %v %+v %q", rec.Header(), usage, rec.Body.String())
			}
		})
	}
}

func TestPlanErrorLogsProviderStatusWithoutPrivateText(t *testing.T) {
	const secret = "outside-credential-fixture"
	for _, tc := range []struct {
		body, category string
	}{
		{`{"error":{"code":"subscription_sharing_usage_unavailable","message":"` + secret + `"}}`, "subscription_sharing_usage_unavailable"},
		{`{"error":{"code":"` + secret + `","message":"private response"}}`, "responses_error"},
		{`{"detail":"Direct routing is unavailable: ` + secret + `"}`, "direct_routing"},
		{`{"detail":"private response"}`, "admission_detail"},
		{`<html>` + secret + `</html>`, "unstructured_response"},
	} {
		var logs bytes.Buffer
		resp := &http.Response{StatusCode: 503, Header: http.Header{"X-Request-Id": {"req_fixture"}},
			Body: io.NopCloser(strings.NewReader(tc.body))}
		rec := httptest.NewRecorder()
		writePlanError(rec, slog.New(slog.NewJSONHandler(&logs, nil)), resp, secret)
		got := logs.String()
		for _, fragment := range []string{`"upstream_status":503`, `"upstream_request_id":"req_fixture"`, `"category":"` + tc.category + `"`} {
			if !strings.Contains(got, fragment) {
				t.Fatalf("missing %s in %s", fragment, got)
			}
		}
		if strings.Contains(got, secret) || strings.Contains(got, "private response") || strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("private upstream text escaped: log=%s response=%s", got, rec.Body.String())
		}
	}
}

type interruptedPlanStream struct{}

func (interruptedPlanStream) Read([]byte) (int, error) {
	return 0, errors.New("stream interrupted")
}

func TestPlanKeepsReportedUsageAfterDisconnect(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		known       bool
	}{
		{"completed with usage", `{"type":"response.completed","response":{"usage":{"input_tokens":6,"output_tokens":4}}}`, true},
		{"interrupted before completion", `{"type":"response.in_progress","response":{"usage":{"input_tokens":6,"output_tokens":4}}}`, false},
		{"completed without usage", `{"type":"response.completed","response":{"usage":null}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage Usage
			client := &http.Client{Transport: planRT(func(*http.Request) (*http.Response, error) {
				stream := io.MultiReader(strings.NewReader("data: "+tc.event+"\n\n"), interruptedPlanStream{})
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(stream)}, nil
			})}
			h, err := NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*SecretConfig, error) {
				return &SecretConfig{UpstreamURL: "https://api.openai.com", AuthKind: AuthKindOpenAIPlan, SecretValue: "outside-fixture"}, nil
			}, &Options{Client: client, Admit: func(context.Context, string, string, string) (func(Usage) error, error) {
				return func(u Usage) error { usage = u; return nil }, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/llm/openai-plan/v1/responses", strings.NewReader(`{"model":"fixture","input":[]}`))
			req.Header.Set("Authorization", "Bearer botbox-placeholder")
			h.ServeHTTP(httptest.NewRecorder(), req)
			if usage.Known != tc.known || (tc.known && (usage.InputTokens != 6 || usage.OutputTokens != 4)) {
				t.Fatalf("unexpected usage: %+v", usage)
			}
		})
	}
}

func TestUnifiedOpenAIRouteSupportsPlanOAuthAndReconnectErrors(t *testing.T) {
	//nolint:gosec // synthetic credential used in unit test
	const secret = "unified-plan-access-fixture"
	var sawAuth string
	client := &http.Client{Transport: planRT(func(req *http.Request) (*http.Response, error) {
		sawAuth = req.Header.Get("Authorization")
		var body map[string]any
		if jsonErr := json.NewDecoder(req.Body).Decode(&body); jsonErr != nil || body["store"] != false || body["stream"] != true {
			t.Fatalf("unified route did not normalize ChatGPT plan payload: %v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")),
		}, nil
	})}

	var usage Usage
	var sawSubscription bool
	h, err := NewScopedHandler("workspace", "bot", func(_ context.Context, _, provider string) (*SecretConfig, error) {
		if provider != ProviderOpenAI {
			return nil, ErrSecretNotFound
		}
		return &SecretConfig{UpstreamURL: "https://api.openai.com", PathPrefix: "/v1/responses", AuthKind: AuthKindOpenAIPlan, SecretValue: secret}, nil
	}, &Options{
		Client: client,
		Admit: func(ctx context.Context, provider, model, path string) (func(Usage) error, error) {
			sawSubscription = SubscriptionRequest(ctx)
			if provider != ProviderOpenAI || path != "/v1/responses" || model != "gpt-5" {
				t.Fatalf("unexpected admit args: %s %s %s", provider, model, path)
			}
			return func(u Usage) error { usage = u; return nil }, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-responses path and wrong placeholder are rejected for AuthKindOpenAIPlan.
	for _, tc := range []struct {
		path, auth string
	}{
		{"/llm/openai/v1/chat/completions", "Bearer botbox-placeholder"},
		{"/llm/openai/v1/responses", "Bearer forged"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"gpt-5","input":[]}`))
		req.Header.Set("Authorization", tc.auth)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s with %s: got %d want 403", tc.path, tc.auth, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[],"metadata":{"trace":"x"},"stream":false}`))
	req.Header.Set("Authorization", "Bearer botbox-placeholder")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !sawSubscription || sawAuth != "Bearer "+secret || usage.InputTokens != 5 || usage.OutputTokens != 2 {
		t.Fatalf("status=%d sub=%v auth=%q usage=%+v body=%s", rec.Code, sawSubscription, sawAuth, usage, rec.Body.String())
	}

	// ErrPlanReconnect maps to 401 plan_reconnect on /llm/openai/*.
	reconnectHandler, err := NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*SecretConfig, error) {
		return nil, ErrPlanReconnect
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[]}`))
	req.Header.Set("Authorization", "Bearer botbox-placeholder")
	reconnectHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "plan_reconnect") {
		t.Fatalf("reconnect status=%d body=%s", rec.Code, rec.Body.String())
	}
}
