// SPDX-License-Identifier: AGPL-3.0-only

package groups

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
)

func TestTeamToolArguments(t *testing.T) {
	turn := uuid.New()
	for _, tt := range []struct {
		name, tool, args string
		valid            bool
	}{
		{"status", TeamStatusTool, `{"bot":"Babu Frik"}`, true},
		{"CLI progress", TeamProgressTool, `{"text":"Tests passed","blocker":false,"turn_id":"` + turn.String() + `"}`, true},
		{"missing bot", TeamStatusTool, `{}`, false},
		{"wrong bot type", TeamStatusTool, `{"bot":5}`, false},
		{"missing text", TeamProgressTool, `{"blocker":true}`, false},
		{"blank text", TeamProgressTool, `{"text":"  "}`, false},
		{"wrong blocker type", TeamProgressTool, `{"text":"done","blocker":"true"}`, false},
		{"null blocker", TeamProgressTool, `{"text":"done","blocker":null}`, false},
		{"NUL in text", TeamProgressTool, `{"text":"bad\u0000text"}`, false},
		{"text too large", TeamProgressTool, `{"text":"` + strings.Repeat("ä", teamTextLimit+1) + `"}`, false},
		{"foreign workspace", TeamStatusTool, `{"bot":"Babu","workspace_id":"other"}`, false},
		{"foreign turn", TeamProgressTool, `{"text":"Fake progress","turn_id":"` + uuid.NewString() + `"}`, false},
		{"trailing object", TeamProgressTool, `{"text":"done"}{}`, false},
		{"unknown tool", "other_tool", `{"text":"done"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTeamToolArgs(tt.tool, json.RawMessage(tt.args), turn)
			if tt.valid && err != nil || !tt.valid && !errors.Is(err, ErrTeamToolArguments) {
				t.Fatalf("validation error = %v, valid = %t", err, tt.valid)
			}
		})
	}
	if got := TeamToolResult("", errors.New("connection error: credential value")); got != "team tool temporarily unavailable" {
		t.Fatalf("unsafe error returned: %q", got)
	}
}

func TestTeamStatusFromActivity(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, state, want string
		age               time.Duration
		stopping, waiting bool
	}{
		{"working without recent tool activity", "running", "working", 2 * time.Second, false, false},
		{"worker lost", "running", "unknown", 46 * time.Second, false, false},
		{"pre-migration row", "running", "unknown", 0, false, false},
		{"stopping", "running", "stopping", time.Second, true, false},
		{"still working with an open approval", "running", "working", time.Second, false, true},
		{"waiting on approval", "completed", "waiting_approval", 0, false, true},
		{"waiting on credential", "waiting_secret", "waiting_secret", 0, false, false},
		{"queued", "queued", "queued", 0, false, false},
		{"completed", "completed", "completed", 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := db.GetTeamPeerActivityRow{TurnStatus: tt.state, StopRequested: tt.stopping, AwaitingApproval: tt.waiting, UpdatedAt: timestamp(now.Add(-time.Minute))}
			if tt.age != 0 {
				row.HeartbeatAt = timestamp(now.Add(-tt.age))
			}
			got := statusFromActivity(pgID(uuid.New()), row, now)
			if got.State != tt.want || (got.WorkingSince != nil) != (tt.want == "working") {
				t.Fatalf("status = %+v, want %q", got, tt.want)
			}
		})
	}
}

func TestTeamProgressLimitsPerTurn(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, blocker := range []bool{false, true} {
		limit := teamProgressLimit
		if blocker {
			limit = teamBlockerLimit
		}
		metadata, _ := json.Marshal(teamUpdateMeta{Blocker: blocker})
		rows := make([]db.ListTeamTurnUpdatesRow, limit)
		for i := range rows {
			rows[i] = db.ListTeamTurnUpdatesRow{Kind: "progress", Content: "Previously reported", Metadata: metadata, CreatedAt: timestamp(now.Add(-time.Minute))}
		}
		if _, err := allowTeamProgress(rows, "A new update", blocker, now); !errors.Is(err, ErrTeamUpdateLimit) {
			t.Fatalf("blocker %t exceeded cap: %v", blocker, err)
		}
		if reason, err := allowTeamProgress(rows, "Previously reported", blocker, now); err != nil || !strings.Contains(reason, "already posted") {
			t.Fatalf("duplicate update: %q %v", reason, err)
		}
	}
}
