// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// poolExhaustedMessages are the Docker Engine errors for "no free subnet left
// in the default address pools". The Engine sends them as a plain HTTP 400 or
// 500, so the text is the only way to tell them apart from other failures.
// The second form comes from Engine releases before 23.0.
var poolExhaustedMessages = []string{
	"all predefined address pools have been fully subnetted",
	"could not find an available, non-overlapping IPv4 address pool",
}

// orphanNetworkGrace protects a network that a concurrent CreateEnvironment
// has just created but not yet attached its first container to.
const orphanNetworkGrace = 2 * time.Minute

type dockerNetwork struct {
	ID      string            `json:"Id"`
	Name    string            `json:"Name"`
	Created time.Time         `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

func isPoolExhausted(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return slices.ContainsFunc(poolExhaustedMessages, func(m string) bool { return strings.Contains(msg, m) })
}

func legacyNetworkName(bot string) string {
	return "botbox-net-" + sanitizeIdentifier(bot)
}

// botNetworkNames lists every network one bot can own: the metered desktop
// network, the separate network of the native agy ACP companion, and the
// network from before the egress proxy, which the switch to the proxy never
// removed.
func botNetworkNames(bot string) []string {
	return []string{proxyNetworkName(bot), proxyNetworkName(agyACPProxyNamespace(bot)), legacyNetworkName(bot)}
}

// ownsBotNetwork checks name and labels together, so a network of another
// workspace, another bot or another project is never removed, even when its
// name looks like ours.
func ownsBotNetwork(nw dockerNetwork, workspace, bot string) bool {
	return workspace != "" && bot != "" &&
		nw.Labels["botbox.workspace_id"] == workspace &&
		nw.Labels["botbox.bot_id"] == bot &&
		slices.Contains(botNetworkNames(bot), nw.Name)
}

// removeBotNetworks deletes the networks of one bot once no container uses
// them any more. Each network holds a whole subnet of the Docker address
// pool, so leaving them behind blocks new computers after a few dozen bots.
func (d *Driver) removeBotNetworks(ctx context.Context, workspace, bot string) error {
	var errs []error
	for _, name := range botNetworkNames(bot) {
		var nw dockerNetwork
		code, err := d.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, &nw)
		if code == http.StatusNotFound {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect network %s: %w", name, err))
			continue
		}
		if !ownsBotNetwork(nw, workspace, bot) {
			continue
		}
		if _, err := d.removeUnusedNetwork(ctx, nw); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reclaimOrphanNetworks removes bot networks that no container uses. They
// leak when a container is removed outside botbox, or when an older release
// destroyed a computer without its network. It runs only after Docker
// reported a full address pool, so normal starts do not pay for the scan.
func (d *Driver) reclaimOrphanNetworks(ctx context.Context, keep string) (int, error) {
	filters, err := json.Marshal(map[string][]string{"label": {"botbox.bot_id"}})
	if err != nil {
		return 0, err
	}
	var networks []dockerNetwork
	if _, err := d.doJSON(ctx, http.MethodGet, "/networks?filters="+url.QueryEscape(string(filters)), nil, &networks); err != nil {
		return 0, fmt.Errorf("list bot networks: %w", err)
	}
	removed := 0
	var errs []error
	for _, nw := range networks {
		if nw.Name == keep || !ownsBotNetwork(nw, nw.Labels["botbox.workspace_id"], nw.Labels["botbox.bot_id"]) {
			continue
		}
		if nw.Created.IsZero() || d.now().Sub(nw.Created) < orphanNetworkGrace {
			continue
		}
		ok, err := d.removeUnusedNetwork(ctx, nw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// removeUnusedNetwork deletes nw unless a container still references it.
// Stopped containers count as users: Docker would delete the network under
// them, but they could then never start again.
func (d *Driver) removeUnusedNetwork(ctx context.Context, nw dockerNetwork) (bool, error) {
	filters, err := json.Marshal(map[string][]string{"network": {nw.Name}})
	if err != nil {
		return false, err
	}
	var users []struct {
		ID string `json:"Id"`
	}
	if _, err := d.doJSON(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &users); err != nil {
		return false, fmt.Errorf("list containers on network %s: %w", nw.Name, err)
	}
	if len(users) > 0 {
		return false, nil
	}
	// Delete by ID, so a network recreated under the same name since the
	// inspect is not removed by mistake.
	ref := nw.ID
	if ref == "" {
		ref = nw.Name
	}
	code, err := d.doJSON(ctx, http.MethodDelete, "/networks/"+url.PathEscape(ref), nil, nil)
	switch {
	case err == nil:
		return true, nil
	case code == http.StatusNotFound:
		return false, nil
	case code == http.StatusForbidden || code == http.StatusConflict:
		// A container attached between the check and the delete; Docker
		// refuses with "active endpoints". Keep the network.
		return false, nil
	default:
		return false, fmt.Errorf("remove network %s: %w", nw.Name, err)
	}
}
