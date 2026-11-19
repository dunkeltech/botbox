// SPDX-License-Identifier: AGPL-3.0-only

package authbroker

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// VerifyRendererSandbox qualifies a Linux Chromium instance before it can
// receive login requests. Subsequent connections stay bound to that browser.
func (e *CDPBrowsingEngine) VerifyRendererSandbox(ctx context.Context) error {
	c, err := e.dialBrowser(ctx)
	if err != nil {
		return errors.New("browser diagnostics connection unavailable")
	}
	defer c.ws.CloseNow() //nolint:errcheck // probe connection cleanup

	raw, err := c.call(ctx, "", "Target.createTarget", map[string]string{"url": "chrome://sandbox/"})
	if err != nil {
		return errors.New("browser sandbox diagnostics unavailable")
	}
	var target struct{ TargetID string }
	if json.Unmarshal(raw, &target) != nil || target.TargetID == "" {
		return errors.New("browser sandbox diagnostics target unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = c.call(cleanup, "", "Target.closeTarget", map[string]string{"targetId": target.TargetID})
	}()

	raw, err = c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true})
	if err != nil {
		return errors.New("browser sandbox diagnostics attachment failed")
	}
	var attached struct{ SessionID string }
	if json.Unmarshal(raw, &attached) != nil || attached.SessionID == "" {
		return errors.New("browser sandbox diagnostics session unavailable")
	}
	// The isolated diagnostics target contains browser-generated status. Wait
	// for its WebUI to populate the table before evaluating renderer controls.
	expression := `new Promise(resolve => {
	  const inspect = () => {
	    const rows = Object.fromEntries(Array.from(document.querySelectorAll("tr"))
	      .filter(row => row.cells.length === 2)
	      .map(row => Array.from(row.cells).map(cell => cell.textContent.trim())));
	    if (location.href === "chrome://sandbox/" && "Seccomp-BPF sandbox" in rows) resolve(rows);
	    else setTimeout(inspect, 25);
	  };
	  inspect();
	})`
	raw, err = c.call(ctx, attached.SessionID, "Runtime.evaluate", map[string]any{
		"expression": expression, "awaitPromise": true, "returnByValue": true,
	})
	if err != nil {
		return errors.New("browser sandbox diagnostics did not complete")
	}
	var status struct {
		Result struct{ Value map[string]string }
	}
	if json.Unmarshal(raw, &status) != nil || !rendererSandboxActive(status.Result.Value) {
		return errors.New("browser namespace and seccomp sandbox required")
	}
	e.mu.Lock()
	e.verifiedBrowser = c.endpoint
	e.mu.Unlock()
	return nil
}

func rendererSandboxActive(rows map[string]string) bool {
	return (rows["Layer 1 Sandbox"] == "Namespace" || rows["Namespace Sandbox"] == "Yes") &&
		rows["PID namespaces"] == "Yes" &&
		rows["Network namespaces"] == "Yes" &&
		rows["Seccomp-BPF sandbox"] == "Yes"
}
