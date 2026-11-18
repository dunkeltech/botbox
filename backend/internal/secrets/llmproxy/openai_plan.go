// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
)

var (
	// ErrPlanAccess denies requests whose desktop authorization has changed.
	ErrPlanAccess = errors.New("ChatGPT plan access denied")
	// ErrPlanReconnect asks the connection owner to authorize again.
	ErrPlanReconnect = errors.New("ChatGPT plan authorization expired")
	// ErrModelPriceRequired prevents an unpriced request under a monetary cap.
	ErrModelPriceRequired = errors.New("budgeted model requires a configured price")
)

type planRevisionContextKey struct{}

// PlanRevision identifies the trusted connection selected for this request.
func PlanRevision(ctx context.Context) string {
	revision, _ := ctx.Value(planRevisionContextKey{}).(string)
	return revision
}

var (
	planModelID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
	planRequestID = regexp.MustCompile(`^req_[a-zA-Z0-9_-]{1,100}$`)
)

func planRequest(body []byte, model string) ([]byte, error) {
	var request map[string]json.RawMessage
	if !planModelID.MatchString(model) || json.Unmarshal(body, &request) != nil || request == nil {
		return nil, errors.New("invalid ChatGPT plan request")
	}
	var input []json.RawMessage
	if json.Unmarshal(request["input"], &input) != nil || input == nil {
		return nil, errors.New("ChatGPT plan input must be an array")
	}
	for _, key := range []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "moderation",
		"multi_agent", "prompt", "previous_response_id", "temperature", "top_logprobs", "top_p", "truncation", "user"} {
		if _, exists := request[key]; exists {
			return nil, errors.New("unsupported ChatGPT plan field")
		}
	}
	// These fields affect storage, attribution, and routing, not the user task.
	// The plan endpoint rejects them even when the CLI supplies defaults.
	delete(request, "metadata")
	delete(request, "prompt_cache_retention")
	delete(request, "safety_identifier")
	request["store"] = json.RawMessage("false")
	request["stream"] = json.RawMessage("true")
	return json.Marshal(request)
}

func isEventStream(value string) bool {
	media, _, err := mime.ParseMediaType(value)
	return err == nil && media == "text/event-stream"
}

func writePlanError(w http.ResponseWriter, logger *slog.Logger, resp *http.Response, secret string) {
	if id := resp.Header.Get("X-Request-ID"); planRequestID.MatchString(id) && !bytes.Contains([]byte(id), []byte(secret)) {
		w.Header().Set("X-Request-ID", id)
	}
	var provider struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&provider)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		writeError(w, logger, 401, "plan_reconnect", "reconnect the ChatGPT plan in bot.Box")
	case provider.Error.Code == "subscription_sharing_usage_limit_exceeded":
		writeError(w, logger, 429, "plan_limit", "ChatGPT plan limit reached")
	case resp.StatusCode == http.StatusForbidden:
		writeError(w, logger, 403, "plan_ineligible", "ChatGPT plan cannot use the selected model or route")
	case resp.StatusCode == http.StatusTooManyRequests:
		writeError(w, logger, 429, "plan_rate_limit", "ChatGPT plan is temporarily rate limited")
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		writeError(w, logger, resp.StatusCode, "plan_request_unsupported", "ChatGPT plan rejected the request")
	default:
		writeError(w, logger, 502, "plan_unavailable", "ChatGPT plan provider is temporarily unavailable")
	}
}

func copyPlanStream(dst io.Writer, src io.Reader, secret string) error {
	r := bufio.NewReaderSize(src, 1<<20)
	token := []byte(secret)
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) == 0 {
				return nil
			}
			return err
		}
		// Keep provider messages usable without exposing a reflected bearer value.
		if _, err = dst.Write(safePlanEvent(bytes.ReplaceAll(line, token, []byte("[redacted]")))); err != nil {
			return err
		}
	}
}

func safePlanEvent(line []byte) []byte {
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok || (!bytes.Contains(data, []byte("response.failed")) && !bytes.Contains(data, []byte(`"error"`))) {
		return line
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(data, &event) != nil {
		return line
	}
	var kind string
	_ = json.Unmarshal(event["type"], &kind)
	switch kind {
	case "response.failed":
		var response map[string]json.RawMessage
		if json.Unmarshal(event["response"], &response) != nil || response == nil {
			return line
		}
		var prior struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(response["error"], &prior)
		code, message := planStreamFailure(prior.Code)
		safe, _ := json.Marshal(map[string]string{"code": code, "message": message})
		response["error"] = safe
		event["response"], _ = json.Marshal(response)
	case "error":
		var prior string
		_ = json.Unmarshal(event["code"], &prior)
		code, message := planStreamFailure(prior)
		event["code"], _ = json.Marshal(code)
		event["message"], _ = json.Marshal(message)
	default:
		return line
	}
	encoded, _ := json.Marshal(event)
	return append(append([]byte("data: "), encoded...), '\n')
}

func planStreamFailure(code string) (string, string) {
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		return "plan_limit", "ChatGPT plan limit reached"
	case "subscription_sharing_invalid_user":
		return "plan_reconnect", "reconnect the ChatGPT plan in bot.Box"
	default:
		return "plan_unavailable", "ChatGPT plan stream failed"
	}
}
