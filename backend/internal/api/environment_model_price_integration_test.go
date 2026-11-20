// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
)

func TestEnvironmentRequiresConfiguredModelPrice(t *testing.T) {
	t.Parallel()

	var driver *stopFailureDriver
	h := newTestHarness(t, nil, harnessOpts{decorateCompute: func(memory *compute.MemoryDriver) compute.Driver {
		driver = &stopFailureDriver{Driver: memory}
		return driver
	}})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	created := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli",
		"llmProvider": "codex", "llmModel": "model-1",
	}, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create bot: %d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	botPath := base + "/bots/" + bot.Id.String()
	envPath := botPath + "/environment"

	expectMissing := func(model string) {
		t.Helper()
		res := h.request(http.MethodGet, envPath, nil, owner.AccessToken, nil, false)
		var state struct {
			Status            string
			MissingModelPrice *struct{ Provider, Model string }
			ControlHolder     *apigen.ControlHolder
		}
		res.decode(t, &state)
		if res.status != http.StatusOK || state.Status != "stopped" || state.ControlHolder != nil ||
			state.MissingModelPrice == nil || state.MissingModelPrice.Provider != "openai" || state.MissingModelPrice.Model != model {
			t.Fatalf("missing price state: %d %s", res.status, res.body)
		}
		for _, op := range []string{"/start", "/restart"} {
			res := h.request(http.MethodPost, envPath+op, nil, owner.AccessToken, nil, true)
			if res.status != http.StatusConflict || res.errorCode(t) != "environment.model_price_required" {
				t.Fatalf("%s without price: %d %s", op, res.status, res.body)
			}
		}
	}

	setEnvironmentModelPrice(t, h, base, owner.AccessToken, "OpenAI", "model-1")
	expectMissing("model-1")
	setEnvironmentModelPrice(t, h, base, owner.AccessToken, "openai", "model-1")

	start := func() compute.EnvID {
		t.Helper()
		res := h.request(http.MethodPost, envPath+"/start", nil, owner.AccessToken, nil, true)
		if res.status != http.StatusOK {
			t.Fatalf("start with price: %d %s", res.status, res.body)
		}
		var state apigen.EnvironmentStateResponse
		res.decode(t, &state)
		return compute.EnvID(state.EnvId)
	}
	envID := start()
	if res := h.request(http.MethodPost, botPath+"/control/take", nil, owner.AccessToken, nil, true); res.status != http.StatusOK {
		t.Fatalf("take control: %d %s", res.status, res.body)
	}
	driver.stopErr = errors.New("compute unavailable")
	if res := h.request(http.MethodPatch, botPath, map[string]any{"llmModel": "model-2"}, owner.AccessToken, nil, true); res.status != http.StatusBadGateway {
		t.Fatalf("failed stop must reject model update: %d %s", res.status, res.body)
	}
	current := h.request(http.MethodGet, botPath, nil, owner.AccessToken, nil, false)
	current.decode(t, &bot)
	if bot.LlmModel != "model-1" || bot.Status != apigen.BotStatusIdle {
		t.Fatalf("failed stop changed model or status: model=%q status=%q", bot.LlmModel, bot.Status)
	}
	driver.stopErr = nil
	if res := h.request(http.MethodPatch, botPath, map[string]any{"llmModel": "model-2"}, owner.AccessToken, nil, true); res.status != http.StatusOK {
		t.Fatalf("save bot with unpriced model: %d %s", res.status, res.body)
	}
	assertEnvironmentStopped(t, h, envID)
	expectMissing("model-2")

	setEnvironmentModelPrice(t, h, base, owner.AccessToken, "openai", "model-2")
	if restartedID := start(); restartedID != envID {
		t.Fatalf("start used environment %s, want to keep %s", restartedID, envID)
	}
	// An environment started before this rule was deployed must also be stopped.
	if _, err := h.pool.Exec(t.Context(), `DELETE FROM model_prices WHERE workspace_id=$1 AND provider='openai' AND model='model-2'`, owner.ActiveWorkspace.Id); err != nil {
		t.Fatal(err)
	}
	if err := api.ReconcileEnvironments(t.Context(), h.pool, h.computeDrv, h.logger); err != nil {
		t.Fatal(err)
	}
	assertEnvironmentStopped(t, h, envID)
	expectMissing("model-2")
}

type stopFailureDriver struct {
	compute.Driver
	stopErr error
}

func (d *stopFailureDriver) StopEnvironment(ctx context.Context, id compute.EnvID) error {
	if d.stopErr != nil {
		return d.stopErr
	}
	return d.Driver.StopEnvironment(ctx, id)
}

func assertEnvironmentStopped(t *testing.T, h *testHarness, id compute.EnvID) {
	t.Helper()
	missing, err := h.computeDrv.Reconcile(context.Background(), []compute.EnvID{id})
	if err != nil || len(missing) != 1 || missing[0] != id {
		t.Fatalf("environment %s still running: reconcile=%v err=%v", id, missing, err)
	}
}

func setEnvironmentModelPrice(t *testing.T, h *testHarness, base, token, provider, model string) {
	t.Helper()
	res := h.request(http.MethodPut, base+"/model-prices", apigen.ModelPriceRequest{
		Provider: provider,
		Model:    model,
	}, token, nil, true)
	if res.status != http.StatusOK {
		t.Fatalf("set model price for %s/%s: %d %s", provider, model, res.status, res.body)
	}
}
