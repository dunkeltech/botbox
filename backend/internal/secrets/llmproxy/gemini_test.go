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
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

const (
	geminiStreamPath = "/llm/google/v1beta/models/gemini-3.8-flash:streamGenerateContent"
	geminiKey        = "fixture-private-gemini-key"
	// Two chunks with running usage totals, as the Gemini API streams them.
	geminiStream = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hel\"}]},\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":1500}}\r\n\r\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":1500,\"candidatesTokenCount\":9,\"thoughtsTokenCount\":40,\"cachedContentTokenCount\":1000}}\r\n\r\n"
)

type geminiProxy struct {
	handler   http.Handler
	upstream  []*http.Request
	admitted  []string
	settled   []llmproxy.Usage
	status    int
	body      string
	resolveFn func() (*llmproxy.SecretConfig, error)
}

func newGeminiProxy(t *testing.T, opts llmproxy.Options) *geminiProxy {
	t.Helper()
	p := &geminiProxy{status: http.StatusOK, body: geminiStream}
	p.resolveFn = func() (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: llmproxy.GeminiUpstreamURL, AuthKind: llmproxy.AuthKindGoogleAPIKey, SecretValue: geminiKey}, nil
	}
	opts.Admit = func(_ context.Context, provider, model, path string) (func(llmproxy.Usage) error, error) {
		p.admitted = append(p.admitted, provider+"|"+model+"|"+path)
		return func(u llmproxy.Usage) error { p.settled = append(p.settled, u); return nil }, nil
	}
	opts.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		p.upstream = append(p.upstream, r)
		return &http.Response{StatusCode: p.status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(p.body))}, nil
	})}
	handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(_ context.Context, bot, provider string) (*llmproxy.SecretConfig, error) {
		if bot != "bot" || provider != llmproxy.ProviderGoogle {
			return nil, llmproxy.ErrSecretNotFound
		}
		return p.resolveFn()
	}, &opts)
	if err != nil {
		t.Fatal(err)
	}
	p.handler = handler
	return p
}

func (p *geminiProxy) do(method, target, body string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for key, values := range header {
		req.Header[key] = values
	}
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	return rec
}

func TestGeminiRouteInjectsKeyAndMetersUsage(t *testing.T) {
	p := newGeminiProxy(t, llmproxy.Options{})
	rec := p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{"model":"cheap-model","contents":[],"generationConfig":{"candidateCount":1}}`, http.Header{
		"X-Goog-Api-Key": {"botbox-placeholder"},
		"Authorization":  {"Bearer botbox-placeholder"},
		"User-Agent":     {"google-genai-sdk/1.77.0"},
	})
	if rec.Code != http.StatusOK || rec.Body.String() != geminiStream {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(p.upstream) != 1 {
		t.Fatalf("upstream calls=%d", len(p.upstream))
	}
	got := p.upstream[0]
	if got.URL.String() != "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse" {
		t.Fatalf("upstream URL=%s", got.URL)
	}
	if got.Header.Get("X-Goog-Api-Key") != geminiKey || got.Header.Get("Authorization") != "" {
		t.Fatalf("upstream auth headers=%v", got.Header)
	}
	for key, values := range got.Header {
		if strings.Contains(strings.Join(values, ","), "botbox-placeholder") {
			t.Fatalf("placeholder forwarded in %s", key)
		}
	}
	// The path names the model. The body field cannot pick a cheaper price.
	if len(p.admitted) != 1 || p.admitted[0] != "google|gemini-3.8-flash|/v1beta/models/gemini-3.8-flash:streamGenerateContent" {
		t.Fatalf("admitted=%v", p.admitted)
	}
	want := llmproxy.Usage{InputTokens: 1500, OutputTokens: 49, CacheTokens: 1000, Known: true}
	if len(p.settled) != 1 || p.settled[0] != want {
		t.Fatalf("settled=%+v want %+v", p.settled, want)
	}
	if strings.Contains(rec.Body.String(), geminiKey) {
		t.Fatal("key in response")
	}
}

func TestGeminiRouteRejectsUnmeteredRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, body string
	}{
		{"count tokens", http.MethodPost, "/llm/google/v1beta/models/gemini-3.8-flash:countTokens", `{}`},
		{"other API version", http.MethodPost, "/llm/google/v1/models/gemini-3.8-flash:streamGenerateContent?alt=sse", `{}`},
		{"model list", http.MethodGet, "/llm/google/v1beta/models", ``},
		{"files API", http.MethodPost, "/llm/google/upload/v1beta/files", `{}`},
		{"path traversal", http.MethodPost, "/llm/google/v1beta/models/../files:streamGenerateContent?alt=sse", `{}`},
		{"tuned model path", http.MethodPost, "/llm/google/v1beta/tunedModels/x:streamGenerateContent?alt=sse", `{}`},
		{"upper case model", http.MethodPost, "/llm/google/v1beta/models/Gemini:streamGenerateContent?alt=sse", `{}`},
		{"long model", http.MethodPost, "/llm/google/v1beta/models/" + strings.Repeat("a", 101) + ":streamGenerateContent?alt=sse", `{}`},
		{"stream without SSE", http.MethodPost, geminiStreamPath, `{}`},
		{"stream with other query", http.MethodPost, geminiStreamPath + "?alt=sse&prettyPrint=false", `{}`},
		{"generate with SSE", http.MethodPost, "/llm/google/v1beta/models/gemini-3.8-flash:generateContent?alt=sse", `{}`},
		{"wrong method", http.MethodGet, geminiStreamPath + "?alt=sse", ``},
		{"body not an object", http.MethodPost, geminiStreamPath + "?alt=sse", `["fixture"]`},
		{"invalid JSON", http.MethodPost, geminiStreamPath + "?alt=sse", `{"contents":`},
		{"several candidates", http.MethodPost, geminiStreamPath + "?alt=sse", `{"generationConfig":{"candidateCount":2}}`},
		{"zero candidates", http.MethodPost, geminiStreamPath + "?alt=sse", `{"generationConfig":{"candidateCount":0}}`},
		{"candidate count type", http.MethodPost, geminiStreamPath + "?alt=sse", `{"generationConfig":{"candidateCount":"2"}}`},
		{"empty body", http.MethodPost, geminiStreamPath + "?alt=sse", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newGeminiProxy(t, llmproxy.Options{})
			rec := p.do(tc.method, tc.target, tc.body, nil)
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(p.upstream) != 0 || len(p.admitted) != 0 {
				t.Fatalf("unmetered request reached upstream=%d admitted=%v", len(p.upstream), p.admitted)
			}
		})
	}
}

func TestGeminiRouteDropsSandboxKeyQuery(t *testing.T) {
	p := newGeminiProxy(t, llmproxy.Options{})
	rec := p.do(http.MethodPost, geminiStreamPath+"?alt=sse&key=fixture-private-sandbox-key", `{}`, nil)
	if rec.Code != http.StatusOK || len(p.upstream) != 1 {
		t.Fatalf("status=%d calls=%d", rec.Code, len(p.upstream))
	}
	if p.upstream[0].URL.RawQuery != "alt=sse" || p.upstream[0].Header.Get("X-Goog-Api-Key") != geminiKey {
		t.Fatalf("upstream=%s", p.upstream[0].URL)
	}

	// Without metering, other queries pass, but never a key.
	var forwarded *http.Request
	handler := llmproxy.NewHandler(func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: llmproxy.GeminiUpstreamURL, AuthKind: llmproxy.AuthKindGoogleAPIKey, SecretValue: geminiKey}, nil
	}, &llmproxy.Options{Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		forwarded = r
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llm/google/v1beta/models?pageSize=5&key=fixture-private-sandbox-key", nil))
	if forwarded == nil || forwarded.URL.Query().Has("key") || forwarded.URL.Query().Get("pageSize") != "5" {
		t.Fatalf("forwarded=%v", forwarded)
	}
}

func TestGeminiRouteFailuresHideKey(t *testing.T) {
	t.Run("missing secret", func(t *testing.T) {
		p := newGeminiProxy(t, llmproxy.Options{})
		p.resolveFn = func() (*llmproxy.SecretConfig, error) { return nil, llmproxy.ErrSecretNotFound }
		rec := p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{}`, nil)
		if rec.Code != http.StatusForbidden || len(p.upstream) != 0 {
			t.Fatalf("status=%d calls=%d", rec.Code, len(p.upstream))
		}
	})
	t.Run("resolver failure", func(t *testing.T) {
		p := newGeminiProxy(t, llmproxy.Options{})
		p.resolveFn = func() (*llmproxy.SecretConfig, error) { return nil, errors.New(geminiKey) }
		rec := p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{}`, nil)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), geminiKey) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("provider rejects key", func(t *testing.T) {
		p := newGeminiProxy(t, llmproxy.Options{})
		p.status, p.body = http.StatusBadRequest, `{"error":{"message":"API key not valid: `+geminiKey+`"}}`
		rec := p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{}`, nil)
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), geminiKey) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if len(p.settled) != 1 || !p.settled[0].Known || p.settled[0].InputTokens != 0 {
			t.Fatalf("rejected request settled=%+v", p.settled)
		}
	})
	t.Run("truncated stream", func(t *testing.T) {
		p := newGeminiProxy(t, llmproxy.Options{})
		p.body = strings.SplitAfter(geminiStream, "\r\n\r\n")[0]
		p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{}`, nil)
		if len(p.settled) != 1 || p.settled[0].Known {
			t.Fatalf("stream without finish reason priced as complete: %+v", p.settled)
		}
	})
}

// blockingGuard stops every stream on the first write, like a guardrail block.
func blockingGuard(err error) *fakeGuardrailEnforcer {
	return &fakeGuardrailEnforcer{sseFn: func(context.Context, string, string, string, io.Writer) io.WriteCloser {
		return failingWriter{err: err}
	}}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }
func (failingWriter) Close() error                { return nil }

func TestGeminiGuardrailBlockRefusesTheRetry(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	p := newGeminiProxy(t, llmproxy.Options{Guardrails: blockingGuard(llmproxy.ErrStreamBlocked), Now: func() time.Time { return now }})
	send := func() *httptest.ResponseRecorder {
		return p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{}`, nil)
	}

	send()
	// agy retries a stopped stream. The retry must not reach Google again.
	rec := send()
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "guardrail_blocked") {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(p.upstream) != 1 || len(p.admitted) != 1 {
		t.Fatalf("retry reached upstream=%d admitted=%d", len(p.upstream), len(p.admitted))
	}
	// Only one request is refused, so the next turn works.
	send()
	if len(p.upstream) != 2 {
		t.Fatalf("refusal is not one-shot: upstream=%d", len(p.upstream))
	}
	// A refusal expires once agy has stopped retrying.
	now = now.Add(3 * time.Minute)
	send()
	if len(p.upstream) != 3 {
		t.Fatalf("expired refusal applied: upstream=%d", len(p.upstream))
	}
}

func TestGeminiRefusalOnlyAfterGuardrailBlock(t *testing.T) {
	p := newGeminiProxy(t, llmproxy.Options{Guardrails: blockingGuard(errors.New("client went away"))})
	for range 2 {
		p.do(http.MethodPost, geminiStreamPath+"?alt=sse", `{}`, nil)
	}
	if len(p.upstream) != 2 {
		t.Fatalf("a broken connection refused the next request: upstream=%d", len(p.upstream))
	}
}

func TestGuardrailBlockDoesNotRefuseOtherProviders(t *testing.T) {
	calls := 0
	handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: "https://provider.invalid", AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture"}, nil
	}, &llmproxy.Options{
		Guardrails: blockingGuard(llmproxy.ErrStreamBlocked),
		Admit: func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
			return func(llmproxy.Usage) error { return nil }, nil
		},
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/llm/openai/v1/chat/completions", strings.NewReader(`{"model":"fixture"}`)))
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestGoogleKeyReportsAPIKeyMode(t *testing.T) {
	mode, err := llmproxy.AuthMode(context.Background(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{AuthKind: llmproxy.AuthKindGoogleAPIKey, SecretValue: geminiKey}, nil
	}, "bot", llmproxy.ProviderGoogle)
	if err != nil || mode != "api_key" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
}
