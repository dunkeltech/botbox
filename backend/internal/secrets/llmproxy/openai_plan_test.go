package llmproxy

import (
	"context"
	"io"
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
		})
	}
}
