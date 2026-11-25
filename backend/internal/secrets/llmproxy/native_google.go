// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	cloudCodeStream   = "/v1internal:streamGenerateContent"
	cloudCodeGenerate = "/v1internal:generateContent"
)

// NativeGoogleProxy inspects Cloud Code requests from an already authenticated
// CLI. The CONNECT handler supplies the trusted origin and TLS transport.
type NativeGoogleProxy struct {
	*proxyHandler
}

// NewNativeGoogleProxy configures a bot-scoped proxy without a secret resolver.
func NewNativeGoogleProxy(botID string, opts Options) *NativeGoogleProxy {
	opts.botID = botID
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &NativeGoogleProxy{proxyHandler: &proxyHandler{options: opts}}
}

// ServeInspected forwards approved requests using the CLI's Bearer header.
// It is reachable only after the egress worker validates mode and CONNECT.
func (p *NativeGoogleProxy) ServeInspected(w http.ResponseWriter, r *http.Request, host string, transport http.RoundTripper) {
	stream, metered, allowed := nativeGoogleMethod(r)
	if !allowed || (host != "cloudcode-pa.googleapis.com" && host != "daily-cloudcode-pa.googleapis.com") ||
		transport == nil || p.options.Guardrails == nil || p.options.Admit == nil {
		http.Error(w, "unsupported Cloud Code request", http.StatusForbidden)
		return
	}
	token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !hasBearer || token == "" || len(token) > 8192 || strings.ContainsAny(token, " \t\r\n") || contentType != "application/json" ||
		r.Header.Get("Content-Encoding") != "" || r.Header.Get("Upgrade") != "" {
		http.Error(w, "invalid Cloud Code request", http.StatusBadRequest)
		return
	}
	body, ok := readNativeJSON(r.Body, token)
	if !ok {
		http.Error(w, "invalid Cloud Code request body", http.StatusBadRequest)
		return
	}
	route := proxyRoute{botID: p.options.botID, provider: ProviderGoogle, method: r.Method, path: r.URL.Path}
	model, nested, ok := nativeGoogleModelRequest(body, stream, metered)
	if !ok {
		http.Error(w, "invalid Cloud Code model request", http.StatusBadRequest)
		return
	}
	decision, err := p.options.Guardrails.EvaluateInput(r.Context(), route.provider, model, route.path, body)
	if !p.allowGuardrailDecision(w, route, decision.Action, decision.Code, decision.Message, "LLM request blocked by workspace guardrail", err) {
		return
	}
	if decision.Action == "redacted" {
		body = decision.Body
		after, changed, valid := nativeGoogleModelRequest(body, stream, metered)
		if !valid || after != model {
			http.Error(w, "invalid inspected model request", http.StatusForbidden)
			return
		}
		nested = changed
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	var usage Usage
	if metered {
		ctx = context.WithValue(ctx, subscriptionContextKey{}, true)
		body, ok = constrainNativeTools(body, nested, p.options.NativeComputerTools)
		if !ok {
			http.Error(w, "invalid inspected model request", http.StatusForbidden)
			return
		}
		settle, permitted := p.admitRequest(ctx, w, route, model)
		if !permitted {
			return
		}
		defer func() {
			if err := settle(usage); err != nil {
				p.options.Logger.Warn("Cloud Code request accounting incomplete", "bot_id", route.botID)
			}
		}()
	}
	target := url.URL{Scheme: "https", Host: host, Path: route.path, RawQuery: r.URL.RawQuery}
	out, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "Cloud Code request unavailable", http.StatusBadGateway)
		return
	}
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Authorization", r.Header.Get("Authorization"))
	for _, name := range []string{"Accept", "User-Agent", "X-Goog-Api-Client", "X-Client-Name", "X-Client-Version"} {
		if value := r.Header.Get(name); value != "" {
			out.Header.Set(name, value)
		}
	}
	response, err := transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "Cloud Code upstream unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	usage = p.forwardNativeGoogle(ctx, w, response, route, model, token, stream, metered)
}

func nativeGoogleMethod(r *http.Request) (stream, metered, allowed bool) {
	if r.Method != http.MethodPost || r.URL.RawPath != "" {
		return false, false, false
	}
	switch r.URL.Path {
	case cloudCodeStream:
		return true, true, r.URL.RawQuery == "alt=sse"
	case cloudCodeGenerate:
		return false, true, r.URL.RawQuery == ""
	case "/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels",
		"/v1internal:fetchUserInfo", "/v1internal:listExperiments",
		"/v1internal:fetchAdminControls", "/v1internal:retrieveUserQuotaSummary":
		return false, false, r.URL.RawQuery == ""
	default:
		return false, false, false
	}
}

func nativeGoogleModelRequest(body []byte, stream, metered bool) (string, []byte, bool) {
	var envelope struct {
		Model   string          `json:"model"`
		Request json.RawMessage `json:"request"`
	}
	if !decodeJSONObject(body, &envelope) {
		return "", nil, false
	}
	if !metered {
		return "", nil, true
	}
	query, method := "", "generateContent"
	if stream {
		query, method = "alt=sse", "streamGenerateContent"
	}
	model, ok := geminiRequestModel("/v1beta/models/"+envelope.Model+":"+method, query, envelope.Request)
	var contents struct {
		Contents []json.RawMessage `json:"contents"`
	}
	if !ok || json.Unmarshal(envelope.Request, &contents) != nil || len(contents.Contents) == 0 {
		return "", nil, false
	}
	return model, envelope.Request, true
}

func decodeJSONObject(body []byte, dst any) bool {
	body = bytes.TrimSpace(body)
	return len(body) != 0 && body[0] == '{' && json.Unmarshal(body, dst) == nil
}

func constrainNativeTools(body, request []byte, tools map[string]func(json.RawMessage) error) ([]byte, bool) {
	var envelope, modelRequest map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || json.Unmarshal(request, &modelRequest) != nil {
		return nil, false
	}
	declared := modelRequest["tools"]
	delete(modelRequest, "tools")
	delete(modelRequest, "toolConfig")
	if len(tools) > 0 {
		safe, ok := nativeMCPDeclarations(declared, tools)
		if !ok {
			return nil, false
		}
		if len(safe) > 0 {
			modelRequest["tools"] = safe
		}
	}
	nested, err := json.Marshal(modelRequest)
	if err != nil {
		return nil, false
	}
	envelope["request"] = nested
	data, err := json.Marshal(envelope)
	return data, err == nil
}

func readNativeJSON(body io.Reader, token string) ([]byte, bool) {
	if body == nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(body, maxModelBodyBytes+1))
	if err != nil || len(data) > maxModelBodyBytes {
		return nil, false
	}
	data = bytes.TrimSpace(data)
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if len(data) == 0 || data[0] != '{' || !json.Valid(data) || decoder.Decode(&decoded) != nil {
		return nil, false
	}
	// Also test decoded strings so JSON escaping cannot echo the Bearer token.
	canonical, err := json.Marshal(decoded)
	return canonical, err == nil && !bytes.Contains(canonical, []byte(token))
}
