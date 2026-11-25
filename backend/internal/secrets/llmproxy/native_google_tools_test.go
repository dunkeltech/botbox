// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/computermcp"
	"github.com/dunkeltech/botbox/backend/internal/guardrails"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

func TestNativeGoogleAllowsOnlyItsWorkSandboxTools(t *testing.T) {
	const declarations = `{"model":"gemini","request":{"contents":[{}],"tools":[
		{"functionDeclarations":[{"name":"run_command"},{"name":"mcp_botbox-computer_write_file"},{"name":"mcp_other_write_file"}]},
		{"googleSearch":{}}
	]}}`
	const approved = `{"functionCall":{"id":"fixture","name":"mcp_botbox-computer_write_file","args":{"path":"fixture.txt","content":"fixture","toolSummary":"Update the fixture"}}}`
	for _, tc := range []struct {
		name   string
		part   string
		status int
	}{
		{"work tool", approved, 200},
		{"local shell", strings.Replace(approved, "mcp_botbox-computer_write_file", "run_command", 1), 502},
		{"other server", strings.Replace(approved, "mcp_botbox-computer_write_file", "mcp_other_write_file", 1), 502},
		{"extra bot identity", strings.Replace(approved, `"path":"fixture.txt"`, `"path":"fixture.txt","bot_id":"other"`, 1), 502},
		{"blocked argument", strings.Replace(approved, `"content":"fixture"`, `"content":"private fixture"`, 1), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := guardrails.CompilePolicy(guardrails.Policy{Enabled: true}, []guardrails.CustomRule{{
				Slug: "fixture", Enabled: true, Direction: guardrails.DirectionOutput, MatchMode: guardrails.MatchModeKeywords,
				Pattern: "private fixture", Action: guardrails.ActionBlock,
			}}, nil)
			proxy := llmproxy.NewNativeGoogleProxy("fixture-bot", llmproxy.Options{
				NativeComputerTools: computermcp.ModelTools(),
				Guardrails: &fakeGuardrailEnforcer{outputFn: func(_ context.Context, _, _, _ string, body []byte) (llmproxy.GuardrailOutputDecision, error) {
					result, next, err := policy.EvaluateJSON(guardrails.DirectionOutput, body)
					return llmproxy.GuardrailOutputDecision{Action: result.Action, Body: next}, err
				}},
				Admit: func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
					return func(llmproxy.Usage) error { return nil }, nil
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent?alt=sse", strings.NewReader(declarations))
			req.Header.Set("Authorization", "Bearer fixture-native-login")
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				forwarded, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				data := string(forwarded)
				if !strings.Contains(data, "mcp_botbox-computer_write_file") || strings.Contains(data, "run_command") ||
					strings.Contains(data, "mcp_other_write_file") || strings.Contains(data, "googleSearch") {
					t.Fatalf("unexpected tool roster: %s", data)
				}
				response := `data: {"response":{"candidates":[{"content":{"parts":[` + tc.part + `]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}}` + "\r\n\r\n"
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)),
					Header: http.Header{"Content-Type": {"text/event-stream"}}}, nil
			})
			proxy.ServeInspected(res, req, "cloudcode-pa.googleapis.com", upstream)
			if res.Code != tc.status {
				t.Fatalf("tool response status = %d, want %d (%s)", res.Code, tc.status, res.Body.String())
			}
			if strings.Contains(res.Body.String(), "private fixture") || (tc.status >= 400 && strings.Contains(res.Body.String(), "functionCall")) {
				t.Fatal("denied tool response reached the CLI")
			}
		})
	}
}
