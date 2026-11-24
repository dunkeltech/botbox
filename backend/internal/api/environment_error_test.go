// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

func TestWriteComputeErrorNetworkCapacity(t *testing.T) {
	t.Parallel()
	dockerErr := errors.New("docker API HTTP 400: all predefined address pools have been fully subnetted")
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "full address pool",
			err:        fmt.Errorf("%w: create network botbox-egress-net-bot-1: %w", compute.ErrNetworkCapacity, dockerErr),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "environment.network_capacity",
		},
		{
			name:       "raw docker error without the typed cause stays generic",
			err:        dockerErr,
			wantStatus: http.StatusBadGateway,
			wantCode:   "sandbox_unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &server{logger: slog.New(slog.DiscardHandler)}
			rec := httptest.NewRecorder()
			s.writeComputeError(rec, tc.err, "failed to start sandbox environment")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", body.Error.Code, tc.wantCode)
			}
			// Host network names and raw Engine errors describe the operator's
			// Docker host and must not reach workspace members.
			for _, leak := range []string{"botbox-egress-net", "docker API", "subnetted"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Fatalf("response leaks %q: %s", leak, rec.Body.String())
				}
			}
		})
	}
}
