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

	"github.com/dunkeltech/botbox/backend/internal/guardrails"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

func TestNativeGoogleGuardedStream(t *testing.T) {
	const token = "synthetic-ACP-bearer"
	const req = `{"model":"gemini-3.8-flash","project":"fixture","request":{"contents":[{"role":"user","parts":[{"text":"prompt"}]}],"tools":[{"functionDeclarations":[{"name":"run_command"}]}]}}`
	const first = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"private "}]}}]}}` + "\r\n\r\n"
	const second = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"message"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3,"totalTokenCount":11}}}` + "\r\n\r\n"

	for _, tc := range []struct {
		name, action, payload, authorization, body, path, host string
		budgetErr                                              error
		wantStatus, wantCalls                                  int
	}{
		{name: "pass", wantStatus: 200, wantCalls: 1},
		{name: "redact across frames", action: guardrails.ActionRedact, wantStatus: 200, wantCalls: 1},
		{name: "block across frames", action: guardrails.ActionBlock, wantStatus: 403, wantCalls: 1},
		{name: "blocked input", action: guardrails.ActionBlock, body: strings.Replace(req, "prompt", "private message", 1), wantStatus: 403},
		{name: "alternate host", host: "not-google.test", wantStatus: 403},
		{name: "unknown path", path: "/v1internal:execute?alt=sse", wantStatus: 403},
		{name: "no bearer", authorization: "Basic fixture", wantStatus: 400},
		{name: "bearer in prompt", body: strings.Replace(req, "prompt", token, 1), wantStatus: 400},
		{name: "bad candidate count", body: strings.Replace(req, `"contents"`, `"generationConfig":{"candidateCount":2},"contents"`, 1), wantStatus: 400},
		{name: "budget exceeded", budgetErr: llmproxy.ErrBudgetExceeded, wantStatus: 429},
		{name: "database unavailable after admission", budgetErr: errors.New("internal detail"), wantStatus: 503},
		{name: "nonterminal stream", payload: first, wantStatus: 502, wantCalls: 1},
		{name: "unexpected executable part", payload: strings.Replace(first+second, `"text":"private "`, `"functionCall":{"name":"run_command","args":{"command":"anything"}}`, 1), wantStatus: 502, wantCalls: 1},
		{name: "upstream bearer echo", payload: strings.Replace(first+second, "private ", "\\u0073ynthetic-ACP-bearer", 1), wantStatus: 502, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var policyRules []guardrails.CustomRule
			if tc.action != "" {
				policyRules = []guardrails.CustomRule{{
					Slug: "fixture", Enabled: true, Direction: guardrails.DirectionBoth,
					MatchMode: guardrails.MatchModeKeywords, Pattern: "private message",
					Action: tc.action, ReplacementToken: "[REDACTED]",
				}}
			}
			policy := guardrails.CompilePolicy(guardrails.Policy{Enabled: true}, policyRules, nil)
			enforce := &fakeGuardrailEnforcer{
				inputFn: func(_ context.Context, _, _, _ string, data []byte) (llmproxy.GuardrailInputDecision, error) {
					result, next, err := policy.EvaluateJSON(guardrails.DirectionInput, data)
					return llmproxy.GuardrailInputDecision{Action: result.Action, Body: next}, err
				},
				outputFn: func(_ context.Context, _, _, _ string, data []byte) (llmproxy.GuardrailOutputDecision, error) {
					result, next, err := policy.EvaluateJSON(guardrails.DirectionOutput, data)
					return llmproxy.GuardrailOutputDecision{Action: result.Action, Body: next}, err
				},
			}
			var settled []llmproxy.Usage
			proxy := llmproxy.NewNativeGoogleProxy("fixture-bot", llmproxy.Options{
				Guardrails: enforce,
				Admit: func(ctx context.Context, provider, model, _ string) (func(llmproxy.Usage) error, error) {
					if !llmproxy.SubscriptionRequest(ctx) || provider != "google" || model != "gemini-3.8-flash" {
						t.Fatalf("wrong admission identity %s %s", provider, model)
					}
					if tc.budgetErr != nil {
						return nil, tc.budgetErr
					}
					return func(usage llmproxy.Usage) error {
						settled = append(settled, usage)
						return nil
					}, nil
				},
			})
			calls := 0
			upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				forwarded, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Goog-Api-Key") != "" ||
					r.Header.Get("Cookie") != "" || strings.Contains(string(forwarded), "run_command") {
					t.Fatal("native request rewrote authentication or forwarded untrusted headers or tool declarations")
				}
				fixture := tc.payload
				if fixture == "" {
					fixture = first + second
				}
				return &http.Response{
					StatusCode: 200, Body: io.NopCloser(strings.NewReader(fixture)),
					Header: http.Header{"Content-Type": {"text/event-stream"}, "Set-Cookie": {token}},
				}, nil
			})
			methodPath, host, auth, body := tc.path, tc.host, tc.authorization, tc.body
			if methodPath == "" {
				methodPath = "/v1internal:streamGenerateContent?alt=sse"
			}
			if host == "" {
				host = "daily-cloudcode-pa.googleapis.com"
			}
			if auth == "" {
				auth = "Bearer " + token
			}
			if body == "" {
				body = req
			}
			r := httptest.NewRequest(http.MethodPost, methodPath, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", auth)
			r.Header.Set("X-Goog-Api-Key", "must-not-forward")
			r.Header.Set("Cookie", "must-not-forward")
			rec := httptest.NewRecorder()
			proxy.ServeInspected(rec, r, host, upstream)
			if rec.Code != tc.wantStatus || calls != tc.wantCalls {
				t.Fatalf("status = %d, calls = %d, want %d and %d", rec.Code, calls, tc.wantStatus, tc.wantCalls)
			}
			if strings.Contains(rec.Body.String(), token) || rec.Header().Get("Set-Cookie") != "" {
				t.Fatal("Bearer credential leaked in proxy response")
			}
			if tc.name == "redact across frames" && (!strings.Contains(rec.Body.String(), "[REDACTED]") || strings.Contains(rec.Body.String(), "private ")) {
				t.Fatal("cross-frame content survived redaction")
			}
			if tc.wantCalls == 1 && len(settled) != 1 {
				t.Fatalf("settled %d records for one provider request", len(settled))
			}
			if tc.wantStatus == 200 && (!settled[0].Known || settled[0].InputTokens != 8 || settled[0].OutputTokens != 3) {
				t.Fatalf("bad usage: %+v", settled[0])
			}
		})
	}
}
