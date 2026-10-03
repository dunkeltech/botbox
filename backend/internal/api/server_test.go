package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/api"
)

func TestHandlerRoutesAndHealth(t *testing.T) {
	t.Parallel()

	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name           string
		method         string
		target         string
		cancelContext  bool
		readinessCheck api.ReadinessCheck
		wantStatus     int
		wantStatusBody string
		wantErrorCode  string
	}{
		{
			name:           "healthz succeeds when dependencies are ready",
			method:         http.MethodGet,
			target:         "/api/healthz",
			wantStatus:     http.StatusOK,
			wantStatusBody: "ok",
		},
		{
			name:          "healthz rejects POST method",
			method:        http.MethodPost,
			target:        "/api/healthz",
			wantStatus:    http.StatusMethodNotAllowed,
			wantErrorCode: "method_not_allowed",
		},
		{
			name:          "unknown endpoint returns 404 JSON error",
			method:        http.MethodGet,
			target:        "/api/unknown-endpoint",
			wantStatus:    http.StatusNotFound,
			wantErrorCode: "not_found",
		},
		{
			name:   "healthz returns 503 when dependency check fails",
			method: http.MethodGet,
			target: "/api/healthz",
			readinessCheck: func(_ context.Context) error {
				return errors.New("postgres dial timeout")
			},
			wantStatus:    http.StatusServiceUnavailable,
			wantErrorCode: "dependency_unavailable",
		},
		{
			name:          "healthz returns 503 when context is canceled",
			method:        http.MethodGet,
			target:        "/api/healthz",
			cancelContext: true,
			wantStatus:    http.StatusServiceUnavailable,
			wantErrorCode: "dependency_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := api.NewHandler(discardLogger, tc.readinessCheck)
			ctx := context.Background()
			if tc.cancelContext {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			req := httptest.NewRequestWithContext(ctx, tc.method, tc.target, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			res := rec.Result()
			defer func() {
				if err := res.Body.Close(); err != nil {
					t.Fatalf("close body: %v", err)
				}
			}()

			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}

			if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want %q", got, "nosniff")
			}

			var decoded struct {
				Status string `json:"status"`
				Error  struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode body: %v", err)
			}

			if tc.wantStatusBody != "" && decoded.Status != tc.wantStatusBody {
				t.Fatalf("status body = %q, want %q", decoded.Status, tc.wantStatusBody)
			}
			if tc.wantErrorCode != "" && decoded.Error.Code != tc.wantErrorCode {
				t.Fatalf("error.code = %q, want %q", decoded.Error.Code, tc.wantErrorCode)
			}
		})
	}
}
