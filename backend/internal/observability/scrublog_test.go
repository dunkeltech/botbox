// SPDX-License-Identifier: AGPL-3.0-only

package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestScrubbingHandlerRedactsKeysAndPatterns(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	h := NewScrubbingHandler(slog.NewJSONHandler(&buf, nil))
	logger := slog.New(h).With("token", "raw-token-123", slog.Group("db", slog.String("password", "supersecret")))

	logger.Error("failed with Authorization Bearer my-secret-jwt-value and postgres://botbox:dbpass123@postgres:5432/botbox",
		"err", errors.New("upstream rejected sk-ant-api03-abcdef123456"),
		"api_key", "AIzaSySecretKey123456",
		"safe", "ok-value",
	)

	line := buf.String()
	for _, secret := range []string{
		"raw-token-123",
		"supersecret",
		"my-secret-jwt-value",
		"dbpass123",
		"sk-ant-api03-abcdef123456",
		"AIzaSySecretKey123456",
	} {
		if strings.Contains(line, secret) {
			t.Fatalf("scrubbed log leaked %q: %s", secret, line)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON log: %v (%s)", err, line)
	}
	if decoded["safe"] != "ok-value" {
		t.Fatalf("expected safe attribute preserved, got %v", decoded["safe"])
	}
	if decoded["token"] != redactedValue {
		t.Fatalf("expected token redacted, got %v", decoded["token"])
	}
}
