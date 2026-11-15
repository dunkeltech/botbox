package llmproxy_test

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

	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

type fakeGuardrailEnforcer struct {
	inputFn  func(ctx context.Context, provider, model, routePath string, body []byte) (llmproxy.GuardrailInputDecision, error)
	outputFn func(ctx context.Context, provider, model, routePath string, body []byte) (llmproxy.GuardrailOutputDecision, error)
	sseFn    func(ctx context.Context, provider, model, routePath string, dst io.Writer) io.WriteCloser
}

func (f *fakeGuardrailEnforcer) EvaluateInput(ctx context.Context, provider, model, routePath string, body []byte) (llmproxy.GuardrailInputDecision, error) {
	if f.inputFn != nil {
		return f.inputFn(ctx, provider, model, routePath, body)
	}
	return llmproxy.GuardrailInputDecision{Action: "allowed"}, nil
}

func (f *fakeGuardrailEnforcer) EvaluateOutputJSON(ctx context.Context, provider, model, routePath string, body []byte) (llmproxy.GuardrailOutputDecision, error) {
	if f.outputFn != nil {
		return f.outputFn(ctx, provider, model, routePath, body)
	}
	return llmproxy.GuardrailOutputDecision{Action: "allowed"}, nil
}

func (f *fakeGuardrailEnforcer) WrapSSEOutput(ctx context.Context, provider, model, routePath string, dst io.Writer) io.WriteCloser {
	if f.sseFn != nil {
		return f.sseFn(ctx, provider, model, routePath, dst)
	}
	return nopWriteCloser{Writer: dst}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestProxyGuardrailsEnforcementAndIsolation(t *testing.T) {
	t.Parallel()

	const rawSecret = "sk-ant-fixture0123456789abcdef" //nolint:gosec // synthetic test secret
	const realKey = "real-upstream-bearer-999"         //nolint:gosec // synthetic test secret

	newTestProxy := func(t *testing.T, enforcer llmproxy.GuardrailEnforcer, upstreamHandler http.HandlerFunc) (*httptest.Server, *int, *int, *bytes.Buffer) {
		t.Helper()
		upstreamCalls := 0
		admitCalls := 0
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

		handler, err := llmproxy.NewScopedHandler("ws-1", "bot-1", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
			return &llmproxy.SecretConfig{
				UpstreamURL: "https://provider.invalid",
				AuthKind:    llmproxy.AuthKindBearer,
				SecretValue: realKey,
			}, nil
		}, &llmproxy.Options{
			Guardrails: enforcer,
			Admit: func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
				admitCalls++
				return func(llmproxy.Usage) error { return nil }, nil
			},
			Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				upstreamCalls++
				rec := httptest.NewRecorder()
				upstreamHandler(rec, r)
				return rec.Result(), nil
			})},
			Logger: logger,
		})
		if err != nil {
			t.Fatalf("NewScopedHandler: %v", err)
		}
		return httptest.NewServer(handler), &upstreamCalls, &admitCalls, &logBuf
	}

	t.Run("input block rejects with 403 before Admit and upstream without leaking input", func(t *testing.T) {
		enforcer := &fakeGuardrailEnforcer{
			inputFn: func(context.Context, string, string, string, []byte) (llmproxy.GuardrailInputDecision, error) {
				return llmproxy.GuardrailInputDecision{
					Action:  "blocked",
					Code:    "guardrail_blocked",
					Message: "LLM request blocked by workspace guardrail",
				}, nil
			},
		}
		srv, upstreamCalls, admitCalls, logs := newTestProxy(t, enforcer, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		defer srv.Close()

		reqBody := `{"model":"gpt-5","messages":[{"role":"user","content":"Secret ` + rawSecret + `"}]}`
		resp, err := http.Post(srv.URL+"/llm/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (%s)", resp.StatusCode, body)
		}
		if *admitCalls != 0 {
			t.Fatalf("Admit called %d times, want 0", *admitCalls)
		}
		if *upstreamCalls != 0 {
			t.Fatalf("upstream called %d times, want 0", *upstreamCalls)
		}
		if bytes.Contains(body, []byte(rawSecret)) || strings.Contains(logs.String(), rawSecret) {
			t.Fatalf("raw secret leaked in response or logs")
		}
		if !bytes.Contains(body, []byte("guardrail_blocked")) {
			t.Fatalf("expected guardrail_blocked error code, got %s", body)
		}
	})

	t.Run("input redact rewrites request body before upstream forwarding", func(t *testing.T) {
		var seenBody string
		enforcer := &fakeGuardrailEnforcer{
			inputFn: func(_ context.Context, _, _, _ string, body []byte) (llmproxy.GuardrailInputDecision, error) {
				redacted := bytes.ReplaceAll(body, []byte(rawSecret), []byte("[REDACTED:SECRET]"))
				return llmproxy.GuardrailInputDecision{
					Action: "redacted",
					Body:   redacted,
				}, nil
			},
		}
		srv, upstreamCalls, admitCalls, _ := newTestProxy(t, enforcer, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			seenBody = string(b)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":5,"output_tokens":2}}`))
		})
		defer srv.Close()

		reqBody := `{"model":"gpt-5","messages":[{"role":"user","content":"Keep ` + rawSecret + `"}]}`
		resp, err := http.Post(srv.URL+"/llm/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if *upstreamCalls != 1 || *admitCalls != 1 {
			t.Fatalf("calls upstream=%d admit=%d, want 1/1", *upstreamCalls, *admitCalls)
		}
		if strings.Contains(seenBody, rawSecret) || !strings.Contains(seenBody, "[REDACTED:SECRET]") {
			t.Fatalf("upstream received non-redacted body: %s", seenBody)
		}
	})

	t.Run("output JSON redact and output JSON block", func(t *testing.T) {
		enforcerRedact := &fakeGuardrailEnforcer{
			outputFn: func(_ context.Context, _, _, _ string, body []byte) (llmproxy.GuardrailOutputDecision, error) {
				return llmproxy.GuardrailOutputDecision{
					Action: "redacted",
					Body:   bytes.ReplaceAll(body, []byte("alice@example.com"), []byte("[REDACTED:EMAIL]")),
				}, nil
			},
		}
		srv1, _, _, _ := newTestProxy(t, enforcerRedact, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Email alice@example.com"}}],"usage":{"prompt_tokens":4,"completion_tokens":3}}`))
		})
		defer srv1.Close()

		resp1, err := http.Post(srv1.URL+"/llm/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5"}`))
		if err != nil {
			t.Fatalf("Post redact: %v", err)
		}
		b1, _ := io.ReadAll(resp1.Body)
		_ = resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK || bytes.Contains(b1, []byte("alice@example.com")) || !bytes.Contains(b1, []byte("[REDACTED:EMAIL]")) {
			t.Fatalf("unexpected JSON output redact response (%d): %s", resp1.StatusCode, b1)
		}

		enforcerBlock := &fakeGuardrailEnforcer{
			outputFn: func(context.Context, string, string, string, []byte) (llmproxy.GuardrailOutputDecision, error) {
				return llmproxy.GuardrailOutputDecision{
					Action:  "blocked",
					Code:    "guardrail_blocked",
					Message: "LLM response blocked by workspace guardrail",
				}, nil
			},
		}
		srv2, _, _, _ := newTestProxy(t, enforcerBlock, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Leak ` + rawSecret + `"}}]}`))
		})
		defer srv2.Close()

		resp2, err := http.Post(srv2.URL+"/llm/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5"}`))
		if err != nil {
			t.Fatalf("Post block: %v", err)
		}
		b2, _ := io.ReadAll(resp2.Body)
		_ = resp2.Body.Close()
		if resp2.StatusCode != http.StatusForbidden || bytes.Contains(b2, []byte(rawSecret)) || !bytes.Contains(b2, []byte("guardrail_blocked")) {
			t.Fatalf("unexpected JSON output block response (%d): %s", resp2.StatusCode, b2)
		}
	})

	t.Run("enforcer error fails closed without calling upstream and SSE filter wraps stream", func(t *testing.T) {
		enforcerErr := &fakeGuardrailEnforcer{
			inputFn: func(context.Context, string, string, string, []byte) (llmproxy.GuardrailInputDecision, error) {
				return llmproxy.GuardrailInputDecision{}, errors.New("db unavailable")
			},
		}
		srvErr, upstreamCalls, admitCalls, _ := newTestProxy(t, enforcerErr, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		defer srvErr.Close()

		respErr, err := http.Post(srvErr.URL+"/llm/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5"}`))
		if err != nil {
			t.Fatalf("Post err: %v", err)
		}
		_ = respErr.Body.Close()
		if respErr.StatusCode < 400 || *upstreamCalls != 0 || *admitCalls != 0 {
			t.Fatalf("expected fail-closed status >= 400 with 0 upstream/admit calls, got status=%d up=%d admit=%d", respErr.StatusCode, *upstreamCalls, *admitCalls)
		}

		// SSE wrapper intercepts streamed response.
		sseWrapped := false
		enforcerSSE := &fakeGuardrailEnforcer{
			sseFn: func(_ context.Context, _, _, _ string, dst io.Writer) io.WriteCloser {
				sseWrapped = true
				return &replacingWriteCloser{dst: dst, old: rawSecret, replacement: "[REDACTED]"}
			},
		}
		srvSSE, _, _, _ := newTestProxy(t, enforcerSSE, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"text\":\"" + rawSecret + "\"}\n\ndata: [DONE]\n\n"))
		})
		defer srvSSE.Close()

		respSSE, err := http.Post(srvSSE.URL+"/llm/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5","stream":true}`))
		if err != nil {
			t.Fatalf("Post sse: %v", err)
		}
		sseBody, _ := io.ReadAll(respSSE.Body)
		_ = respSSE.Body.Close()
		var parsedErr map[string]any
		_ = json.Unmarshal(sseBody, &parsedErr)
		if !sseWrapped || bytes.Contains(sseBody, []byte(rawSecret)) || !bytes.Contains(sseBody, []byte("[REDACTED]")) {
			t.Fatalf("SSE wrapper did not mask stream (wrapped=%v): %s", sseWrapped, sseBody)
		}
	})
}

type replacingWriteCloser struct {
	dst         io.Writer
	old         string
	replacement string
}

func (r *replacingWriteCloser) Write(p []byte) (int, error) {
	replaced := bytes.ReplaceAll(p, []byte(r.old), []byte(r.replacement))
	if _, err := r.dst.Write(replaced); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *replacingWriteCloser) Close() error { return nil }

func TestGuardrailInputIsConsistentWithAndWithoutMetering(t *testing.T) {
	t.Parallel()
	const input = `{"model":"model-fixture","input":"fixture-private-value"}`
	for _, metered := range []bool{false, true} {
		name := "unmetered"
		if metered {
			name = "metered"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				decision   llmproxy.GuardrailInputDecision
				err        error
				wantStatus int
				wantCode   string
			}{
				{"blocked", llmproxy.GuardrailInputDecision{Action: "blocked"}, nil, http.StatusForbidden, "guardrail_blocked"},
				{"unavailable", llmproxy.GuardrailInputDecision{}, errors.New("fixture-private-value"), http.StatusForbidden, "guardrail_unavailable"},
				{"redacted", llmproxy.GuardrailInputDecision{Action: "redacted", Body: []byte(`{"model":"model-fixture","input":"[REDACTED]"}`)}, nil, http.StatusOK, ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var evaluated, admitted, forwarded, settled int
					var order []string
					opts := &llmproxy.Options{
						Guardrails: &fakeGuardrailEnforcer{inputFn: func(_ context.Context, provider, model, path string, body []byte) (llmproxy.GuardrailInputDecision, error) {
							evaluated++
							order = append(order, "guardrails")
							if provider != "openai" || model != "model-fixture" || path != "/v1/responses" || string(body) != input {
								t.Fatal("guardrails received unexpected request metadata")
							}
							return tc.decision, tc.err
						}},
						Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
							forwarded++
							order = append(order, "upstream")
							body, err := io.ReadAll(r.Body)
							closeErr := r.Body.Close()
							if err != nil || closeErr != nil || !bytes.Equal(body, tc.decision.Body) || r.ContentLength != int64(len(body)) {
								t.Fatal("upstream did not receive only the redacted request")
							}
							resp := httptest.NewRecorder()
							resp.Header().Set("Content-Type", "application/json")
							_, _ = resp.WriteString(`{"usage":{"input_tokens":1,"output_tokens":2}}`)
							return resp.Result(), nil
						})},
					}
					if metered {
						opts.Admit = func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
							admitted++
							order = append(order, "admit")
							return func(usage llmproxy.Usage) error {
								settled++
								order = append(order, "settle")
								if !usage.Known || usage.InputTokens != 1 || usage.OutputTokens != 2 {
									t.Fatalf("incorrect settlement: %+v", usage)
								}
								return nil
							}, nil
						}
					}
					handler, err := llmproxy.NewScopedHandler("ws-1", "bot-1", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
						return &llmproxy.SecretConfig{UpstreamURL: "https://provider.invalid", AuthKind: llmproxy.AuthKindBearer, SecretValue: "synthetic-value"}, nil
					}, opts)
					if err != nil {
						t.Fatal(err)
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/llm/openai/v1/responses", strings.NewReader(input)))
					if response.Code != tc.wantStatus || evaluated != 1 || strings.Contains(response.Body.String(), "fixture-private-value") {
						t.Fatalf("unexpected guardrail result: status=%d evaluations=%d body=%s", response.Code, evaluated, response.Body.String())
					}
					if tc.wantCode != "" {
						if !strings.Contains(response.Body.String(), tc.wantCode) || admitted != 0 || forwarded != 0 || settled != 0 {
							t.Fatal("blocked request reached admission or upstream")
						}
						return
					}
					wantOrder := "guardrails,upstream"
					if metered {
						wantOrder = "guardrails,admit,upstream,settle"
					}
					if strings.Join(order, ",") != wantOrder || forwarded != 1 {
						t.Fatalf("wrong request lifecycle: %v", order)
					}
				})
			}
		})
	}
}
