// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"net/http"
	"testing"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
)

func TestHostCapacityGuardCountAndMemoryLimits(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{
		maxRunningSandboxes: 1,
		maxSandboxMemoryMiB: 4096,
	})

	owner, _ := h.completeSetup("cap-owner@example.com", "CorrectHorse123!", "Cap Owner", "Cap Corp", "cap-corp")
	wsID := owner.ActiveWorkspace.Id
	seedTestModelPrice(t, h, owner.AccessToken, wsID, "openrouter", "openrouter/fixture")

	bot1, _ := createAndStartBotForTest(t, h, owner.AccessToken, wsID, "CapBot1")

	// Create second bot; starting it must fail 503 environment.capacity_exceeded because MaxRunningSandboxes=1.
	create2 := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/bots", map[string]any{
		"name": "CapBot2", "avatarSeed": "cap2-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, owner.AccessToken, nil, true)
	if create2.status != http.StatusCreated {
		t.Fatalf("create bot2: %d %s", create2.status, string(create2.body))
	}
	var bot2 apigen.Bot
	create2.decode(t, &bot2)

	start2 := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/bots/"+bot2.Id.String()+"/environment/start", nil, owner.AccessToken, nil, true)
	if start2.status != http.StatusServiceUnavailable {
		t.Fatalf("second sandbox start status = %d, want 503 (%s)", start2.status, string(start2.body))
	}
	var errEnv struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	start2.decode(t, &errEnv)
	if errEnv.Error.Code != "environment.capacity_exceeded" {
		t.Fatalf("error code = %q, want environment.capacity_exceeded", errEnv.Error.Code)
	}

	// Restarting the already-running bot1 must be allowed (exclude_bot_id skips self).
	restart1 := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/bots/"+bot1.Id.String()+"/environment/restart", nil, owner.AccessToken, nil, true)
	if restart1.status != http.StatusOK {
		t.Fatalf("restart bot1 under capacity = %d, want 200 (%s)", restart1.status, string(restart1.body))
	}

	// Stop bot1; starting bot2 now succeeds.
	stop1 := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/bots/"+bot1.Id.String()+"/environment/stop", nil, owner.AccessToken, nil, true)
	if stop1.status != http.StatusOK {
		t.Fatalf("stop bot1: %d %s", stop1.status, string(stop1.body))
	}
	start2After := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/bots/"+bot2.Id.String()+"/environment/start", nil, owner.AccessToken, nil, true)
	if start2After.status != http.StatusOK {
		t.Fatalf("bot2 start after stop status = %d, want 200 (%s)", start2After.status, string(start2After.body))
	}

	// Also verify memory cap directly on a tight memory limit harness (1 bot * 2048 MiB fits; 2nd would be 4096 > 2048).
	memHarness := newTestHarness(t, nil, harnessOpts{
		maxRunningSandboxes: 10,
		maxSandboxMemoryMiB: 2048,
	})
	memOwner, _ := memHarness.completeSetup("mem-owner@example.com", "CorrectHorse123!", "Mem Owner", "Mem Corp", "mem-corp")
	memWS := memOwner.ActiveWorkspace.Id
	seedTestModelPrice(t, memHarness, memOwner.AccessToken, memWS, "openrouter", "openrouter/fixture")
	_, _ = createAndStartBotForTest(t, memHarness, memOwner.AccessToken, memWS, "MemBot1")
	c2 := memHarness.request(http.MethodPost, "/api/workspaces/"+memWS.String()+"/bots", map[string]any{
		"name": "MemBot2", "avatarSeed": "mem2-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, memOwner.AccessToken, nil, true)
	var memBot2 apigen.Bot
	c2.decode(t, &memBot2)
	s2 := memHarness.request(http.MethodPost, "/api/workspaces/"+memWS.String()+"/bots/"+memBot2.Id.String()+"/environment/start", nil, memOwner.AccessToken, nil, true)
	if s2.status != http.StatusServiceUnavailable {
		t.Fatalf("memory cap exceeded status = %d, want 503 (%s)", s2.status, string(s2.body))
	}
}
