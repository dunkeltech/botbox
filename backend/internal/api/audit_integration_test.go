// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAuditLogEndpointRBACFilteringAndScrubbing(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("audit-owner@example.com", "CorrectHorse123!", "Audit Owner", "Audit Corp", "audit-corp")
	wsID := owner.ActiveWorkspace.Id

	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, wsID, "audit-admin@example.com", "admin", "Audit Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, wsID, "audit-member@example.com", "member", "Audit Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, wsID, "audit-viewer@example.com", "viewer", "Audit Viewer", "CorrectHorse123!")

	wsRes := h.request(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Foreign Audit", "slug": "foreign-audit",
	}, owner.AccessToken, nil, true)
	if wsRes.status != http.StatusCreated {
		t.Fatalf("create foreign ws: %d %s", wsRes.status, string(wsRes.body))
	}
	var foreignWS apigen.WorkspaceSummary
	wsRes.decode(t, &foreignWS)
	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, foreignWS.Id, "foreign-audit@example.com", "owner", "Foreign Owner", "CorrectHorse123!")

	basePath := "/api/workspaces/" + wsID.String() + "/audit-logs"

	// Unauthenticated -> 401
	unauth := h.request(http.MethodGet, basePath, nil, "", nil, true)
	if unauth.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated audit-logs status = %d, want 401 (%s)", unauth.status, string(unauth.body))
	}

	// Cross-workspace -> 404
	crossWS := h.request(http.MethodGet, basePath, nil, outsider.AccessToken, nil, true)
	if crossWS.status != http.StatusNotFound {
		t.Fatalf("cross-workspace audit-logs status = %d, want 404 (%s)", crossWS.status, string(crossWS.body))
	}

	// Member & Viewer -> 403
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "member", token: member.AccessToken},
		{name: "viewer", token: viewer.AccessToken},
	} {
		resp := h.request(http.MethodGet, basePath, nil, tc.token, nil, true)
		if resp.status != http.StatusForbidden {
			t.Fatalf("%s audit-logs status = %d, want 403 (%s)", tc.name, resp.status, string(resp.body))
		}
	}

	// Admin -> 200
	adminResp := h.request(http.MethodGet, basePath, nil, admin.AccessToken, nil, true)
	if adminResp.status != http.StatusOK {
		t.Fatalf("admin audit-logs status = %d, want 200 (%s)", adminResp.status, string(adminResp.body))
	}

	// Invalid query parameters -> 400
	for _, query := range []string{
		"?limit=0",
		"?limit=201",
		"?limit=abc",
		"?offset=-1",
		"?offset=2147483648",
		"?offset=4294967296",
		"?since=not-a-timestamp",
		"?until=not-a-timestamp",
		"?since=2026-11-26T12:00:00Z&until=2026-11-26T11:00:00Z",
	} {
		bad := h.request(http.MethodGet, basePath+query, nil, owner.AccessToken, nil, true)
		if bad.status != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400 (%s)", query, bad.status, string(bad.body))
		}
	}

	// Seed targeted audit entries including potentially sensitive metadata.
	wsPG := pgtype.UUID{Bytes: wsID, Valid: true}
	actorPG := pgtype.UUID{Bytes: owner.User.Id, Valid: true}
	targetSecretID := uuid.New()
	_, err := h.queries.CreateAuditLog(t.Context(), db.CreateAuditLogParams{
		WorkspaceID: wsPG,
		ActorUserID: actorPG,
		Action:      "secret.test_created",
		TargetType:  pgtype.Text{String: "workspace_secret", Valid: true},
		TargetID:    pgtype.UUID{Bytes: targetSecretID, Valid: true},
		Metadata:    []byte(`{"secret_value":"super-secret-value","note":"Bearer super-bearer-123456","safe":"ok"}`),
	})
	if err != nil {
		t.Fatalf("CreateAuditLog secret entry: %v", err)
	}
	_, err = h.queries.CreateAuditLog(t.Context(), db.CreateAuditLogParams{
		WorkspaceID: wsPG,
		ActorUserID: actorPG,
		Action:      "environment.test_started",
		TargetType:  pgtype.Text{String: "bot", Valid: true},
		TargetID:    pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Metadata:    []byte(`{"runtime":"runc"}`),
	})
	if err != nil {
		t.Fatalf("CreateAuditLog env entry: %v", err)
	}

	// Owner lists filtered by action, targetType, and time window + checks scrubbing.
	q := url.Values{}
	q.Set("action", "secret.test_created")
	q.Set("targetType", "workspace_secret")
	q.Set("since", time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339))
	q.Set("until", time.Now().UTC().Add(1*time.Hour).Format(time.RFC3339))
	filtered := h.request(http.MethodGet, basePath+"?"+q.Encode(), nil, owner.AccessToken, nil, true)
	if filtered.status != http.StatusOK {
		t.Fatalf("filtered audit-logs status = %d, want 200 (%s)", filtered.status, string(filtered.body))
	}
	bodyText := string(filtered.body)
	if strings.Contains(bodyText, "super-secret-value") || strings.Contains(bodyText, "super-bearer-123456") {
		t.Fatalf("audit response leaked sensitive metadata: %s", bodyText)
	}
	var out apigen.AuditLogListResponse
	filtered.decode(t, &out)
	if out.Total != 1 || len(out.Entries) != 1 {
		t.Fatalf("filtered total=%d len=%d, want 1", out.Total, len(out.Entries))
	}
	if got := out.Entries[0].Metadata["secret_value"]; got != "[REDACTED]" {
		t.Fatalf("secret_value = %v, want [REDACTED]", got)
	}
	if got, _ := out.Entries[0].Metadata["note"].(string); !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("note = %q, want [REDACTED] bearer token", got)
	}
	if out.Entries[0].ActorEmail == nil || *out.Entries[0].ActorEmail != "audit-owner@example.com" {
		t.Fatalf("actorEmail = %v, want audit-owner@example.com", out.Entries[0].ActorEmail)
	}

	// Pagination check across all workspace entries.
	page1 := h.request(http.MethodGet, basePath+"?limit=1&offset=0", nil, owner.AccessToken, nil, true)
	page2 := h.request(http.MethodGet, basePath+"?limit=1&offset=1", nil, owner.AccessToken, nil, true)
	var p1, p2 apigen.AuditLogListResponse
	page1.decode(t, &p1)
	page2.decode(t, &p2)
	if p1.Total < 2 || len(p1.Entries) != 1 || len(p2.Entries) != 1 || p1.Entries[0].Id == p2.Entries[0].Id {
		t.Fatalf("pagination mismatch p1=%+v p2=%+v", p1, p2)
	}
}
