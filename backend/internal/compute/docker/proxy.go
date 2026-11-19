// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/egresslink"
	"github.com/google/uuid"
)

// ProxyOptions configures per-bot relays and their trusted egress workers.
type ProxyOptions struct {
	Image           string
	DatabaseURL     string
	KeySpec         string
	NATSURL         string
	AuthBrokerURL   string
	AuthBrokerToken string
}

const (
	proxyMarker         = "metered-v1"
	proxyIngressLabel   = "botbox.proxy_ingress"
	proxyIngressVersion = "bot-network-v2"
	proxyPairLabel      = "botbox.proxy_pair"
)

func proxyNetworkName(bot string) string {
	return "botbox-egress-net-" + sanitizeIdentifier(bot)
}

func proxyCAVolume(bot, kind string) string {
	return "botbox-egress-ca-" + kind + "-" + sanitizeIdentifier(bot)
}

func (d *Driver) ensureProxy(ctx context.Context, workspace, bot, network string) error {
	if d.proxy == nil {
		return nil
	}
	if workspace == "" || bot == "" || network != proxyNetworkName(bot) {
		return errors.New("proxy identity unavailable")
	}
	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()

	relayName := "botbox-egress-" + sanitizeIdentifier(bot)
	workerName := "botbox-egress-worker-" + sanitizeIdentifier(bot)
	subnet, err := d.botProxySubnet(ctx, network, workspace, bot)
	if err != nil {
		return err
	}
	relay, err := d.inspectProxy(ctx, relayName, workspace, bot, "relay")
	if err != nil {
		return err
	}
	worker, err := d.inspectProxy(ctx, workerName, workspace, bot, "worker")
	if err != nil {
		return err
	}
	serviceEnv := d.workerServiceEnv(workspace, bot)
	if matchingProxyPair(relay, worker) && workerSettingsMatch(worker.Config.Env, serviceEnv) {
		return d.startExistingProxyPair(ctx, relay, worker)
	}

	image, err := d.proxyIngressImage(ctx)
	if err != nil {
		return err
	}
	// Removing the old worker ends its authenticated connections and makes any
	// copy of the old relay key useless for the replacement pair.
	for _, old := range []*containerInspectResponse{worker, relay} {
		if old != nil {
			if err := d.removeProxy(ctx, old.ID); err != nil {
				return err
			}
		}
	}
	workerIdentity, relayIdentity, err := egresslink.NewPair()
	if err != nil {
		return errors.New("create egress transport identity")
	}
	pairID := uuid.NewString()
	workerEnv := append(workerIdentity.Environment(), "EGRESS_LINK_MODE=worker", "EGRESS_PROXY_BOT_SUBNET="+subnet)
	workerEnv = append(workerEnv, serviceEnv...)
	workerID, err := d.createProxyContainer(ctx, workerName, "worker", image, workspace, bot, pairID, "bridge", workerEnv)
	if err != nil {
		return err
	}
	var relayID string
	ready := false
	defer func() {
		if !ready {
			_ = d.removeProxy(context.WithoutCancel(ctx), workerID)
			if relayID != "" {
				_ = d.removeProxy(context.WithoutCancel(ctx), relayID)
			}
		}
	}()
	// The worker has an Internet-facing interface but listens for mTLS only on
	// this bot's isolated bridge. IP forwarding is disabled in both containers.
	if _, err := d.doJSON(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect",
		map[string]any{"Container": workerID, "EndpointConfig": map[string]any{"Aliases": []string{"egress-upstream"}}}, nil); err != nil {
		return errors.New("egress worker network attachment failed")
	}
	if err := d.startProxy(ctx, workerID); err != nil {
		return err
	}
	relayEnv := append(relayIdentity.Environment(), "EGRESS_LINK_MODE=relay", "EGRESS_PROXY_BOT_SUBNET="+subnet)
	relayID, err = d.createProxyContainer(ctx, relayName, "relay", image, workspace, bot, pairID, network, relayEnv)
	if err != nil {
		return err
	}
	if err := d.startProxy(ctx, relayID); err != nil {
		return err
	}
	ready = true
	return nil
}

func (d *Driver) workerServiceEnv(workspace, bot string) []string {
	env := []string{
		"DATABASE_URL=" + d.proxy.DatabaseURL, "BOTBOX_SECRETS_KEK=" + d.proxy.KeySpec,
		"BOTBOX_WORKSPACE_ID=" + workspace, "BOTBOX_BOT_ID=" + bot,
	}
	if d.proxy.NATSURL != "" {
		env = append(env, "NATS_URL="+d.proxy.NATSURL)
	}
	if d.proxy.AuthBrokerURL != "" && d.proxy.AuthBrokerToken != "" {
		env = append(env, "EGRESS_PROXY_AUTH_BROKER_URL="+d.proxy.AuthBrokerURL, "EGRESS_PROXY_AUTH_BROKER_TOKEN="+d.proxy.AuthBrokerToken)
	}
	return env
}

func workerSettingsMatch(actual, expected []string) bool {
	for _, entry := range expected {
		if !slices.Contains(actual, entry) {
			return false
		}
	}
	// Removing an optional service must also revoke the old worker's access.
	for _, entry := range actual {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "NATS_URL", "EGRESS_PROXY_AUTH_BROKER_URL", "EGRESS_PROXY_AUTH_BROKER_TOKEN":
			if !slices.Contains(expected, entry) {
				return false
			}
		}
	}
	return true
}

func (d *Driver) startExistingProxyPair(ctx context.Context, relay, worker *containerInspectResponse) error {
	for _, container := range []*containerInspectResponse{worker, relay} {
		if !container.State.Running {
			if err := d.startProxy(ctx, container.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func matchingProxyPair(relay, worker *containerInspectResponse) bool {
	if relay == nil || worker == nil {
		return false
	}
	pair := relay.Config.Labels[proxyPairLabel]
	return pair != "" && pair == worker.Config.Labels[proxyPairLabel] &&
		relay.Config.Labels[proxyIngressLabel] == proxyIngressVersion &&
		worker.Config.Labels[proxyIngressLabel] == proxyIngressVersion
}

func (d *Driver) inspectProxy(ctx context.Context, name, workspace, bot, role string) (*containerInspectResponse, error) {
	c, err := d.inspectContainer(ctx, name)
	if errors.Is(err, compute.ErrEnvironmentNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("inspect egress container")
	}
	labels := c.Config.Labels
	if labels["botbox.workspace_id"] != workspace || labels["botbox.bot_id"] != bot ||
		labels["botbox.proxy"] != "true" || labels["botbox.egress_proxy"] != proxyMarker ||
		(labels[proxyIngressLabel] == proxyIngressVersion && labels["botbox.proxy_role"] != role) {
		return nil, errors.New("egress container identity mismatch")
	}
	return c, nil
}

func (d *Driver) createProxyContainer(ctx context.Context, name, role, image, workspace, bot, pair, network string, env []string) (string, error) {
	aliases := map[string]any{}
	if role == "relay" {
		aliases["Aliases"] = []string{"egress"}
	}
	host := map[string]any{"Privileged": false, "CapDrop": []string{"ALL"},
		"SecurityOpt": []string{"no-new-privileges:true"}, "ReadonlyRootfs": true, "NetworkMode": network,
		"Memory": int64(256 << 20), "NanoCPUs": int64(1000000000), "PidsLimit": 64,
		"Sysctls": map[string]string{"net.ipv4.ip_forward": "0"}}
	if role == "worker" {
		host["Binds"] = []string{
			proxyCAVolume(bot, "private") + ":/var/lib/botbox/egress-private",
			proxyCAVolume(bot, "public") + ":/var/lib/botbox/egress-public",
		}
	}
	payload := map[string]any{
		"Image": image, "User": "65532:65532", "Env": env,
		"Labels": map[string]string{"botbox.proxy": "true", "botbox.proxy_role": role,
			"botbox.workspace_id": workspace, "botbox.bot_id": bot, "botbox.egress_proxy": proxyMarker,
			proxyIngressLabel: proxyIngressVersion, proxyPairLabel: pair},
		"HostConfig":       host,
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: aliases}},
	}
	if role == "relay" {
		payload["Healthcheck"] = map[string]any{
			"Test":     []string{"CMD", "/usr/local/bin/egress-proxy", "--check-relay"},
			"Interval": int64(2 * time.Second), "Timeout": int64(3 * time.Second), "Retries": 3,
		}
	}
	var created struct {
		ID string `json:"Id"`
	}
	if _, err := d.doJSON(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), payload, &created); err != nil || created.ID == "" {
		return "", errors.New("egress container creation failed")
	}
	return created.ID, nil
}

func (d *Driver) removeProxy(ctx context.Context, id string) error {
	status, err := d.doJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=true", nil, nil)
	if err != nil && status != http.StatusNotFound {
		return errors.New("egress container cleanup failed")
	}
	return nil
}

func (d *Driver) proxyIngressImage(ctx context.Context) (string, error) {
	var image struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string
		}
	}
	if _, err := d.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(d.proxy.Image)+"/json", nil, &image); err != nil {
		return "", errors.New("egress proxy image unavailable")
	}
	if image.ID == "" || image.Config.Labels[proxyIngressLabel] != proxyIngressVersion {
		return "", errors.New("egress proxy image requires a secure-ingress rebuild")
	}
	// Pin the inspected image so deployment cannot switch tags between the
	// ingress-version check and container creation.
	return image.ID, nil
}

func (d *Driver) botProxySubnet(ctx context.Context, name, workspace, bot string) (string, error) {
	var network struct {
		Internal   bool
		EnableIPv6 bool
		Options    map[string]string
		Labels     map[string]string
		IPAM       struct {
			Config []struct{ Subnet string }
		}
	}
	if _, err := d.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, &network); err != nil {
		return "", errors.New("inspect bot network failed")
	}
	if !network.Internal || network.EnableIPv6 || network.Options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || network.Labels["botbox.workspace_id"] != workspace || network.Labels["botbox.bot_id"] != bot {
		return "", errors.New("bot network identity mismatch")
	}
	for _, config := range network.IPAM.Config {
		prefix, err := netip.ParsePrefix(config.Subnet)
		if err == nil && prefix.Addr().Is4() && prefix.Bits() > 0 {
			return prefix.Masked().String(), nil
		}
	}
	return "", errors.New("bot network IPv4 subnet unavailable")
}

func (d *Driver) startProxy(ctx context.Context, id string) error {
	code, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
	if err != nil && code != http.StatusNotModified {
		return errors.New("proxy start failed")
	}
	return nil
}

// UpgradeProxyIngress removes legacy sidecars and restores secure ingress for
// running sandboxes before the API starts accepting work.
func (d *Driver) UpgradeProxyIngress(ctx context.Context) error {
	if d.proxy == nil {
		return nil
	}
	filters, err := json.Marshal(map[string][]string{"label": {"botbox.egress_proxy=" + proxyMarker}})
	if err != nil {
		return err
	}
	var containers []struct {
		ID     string
		Labels map[string]string
		Names  []string
		State  string
	}
	if _, err := d.doJSON(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &containers); err != nil {
		return errors.New("list metered bot containers failed")
	}
	for _, proxy := range containers {
		if proxy.Labels["botbox.proxy"] != "true" || proxy.Labels[proxyIngressLabel] == proxyIngressVersion {
			continue
		}
		workspace, bot := proxy.Labels["botbox.workspace_id"], proxy.Labels["botbox.bot_id"]
		if proxy.ID == "" || workspace == "" || bot == "" || !slices.Contains(proxy.Names, "/botbox-egress-"+sanitizeIdentifier(bot)) {
			return errors.New("legacy proxy identity mismatch")
		}
		status, err := d.doJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(proxy.ID)+"?force=true", nil, nil)
		if err != nil && status != http.StatusNotFound {
			return errors.New("legacy proxy removal failed")
		}
	}
	for _, sandbox := range containers {
		if sandbox.Labels["botbox.managed"] != "true" || sandbox.State != "running" {
			continue
		}
		workspace, bot := sandbox.Labels["botbox.workspace_id"], sandbox.Labels["botbox.bot_id"]
		if workspace == "" || bot == "" || sandbox.ID == "" || !slices.Contains(sandbox.Names, "/botbox-bot-"+sanitizeIdentifier(bot)) {
			return errors.New("metered sandbox identity mismatch")
		}
		if sandbox.Labels[proxyIngressLabel] != proxyIngressVersion {
			if err := d.StopEnvironment(ctx, compute.EnvID(sandbox.ID)); err != nil {
				return err
			}
			continue
		}
		if err := d.ensureProxy(ctx, workspace, bot, proxyNetworkName(bot)); err != nil {
			return err
		}
	}
	return nil
}

// CLIProxyReady verifies the persisted sandbox/proxy deployment before allowing
// money-budgeted CLI turns. Older sandboxes need recreation to receive proxy env.
func (d *Driver) CLIProxyReady(ctx context.Context, id compute.EnvID) error {
	if d.proxy == nil {
		return errors.New("metered proxy is not configured")
	}
	sandbox, err := d.inspectContainer(ctx, string(id))
	if err != nil {
		return errors.New("sandbox inspection failed")
	}
	labels := sandbox.Config.Labels
	if labels["botbox.egress_proxy"] != proxyMarker || labels[proxyIngressLabel] != proxyIngressVersion {
		return errors.New("sandbox requires metered proxy recreation")
	}
	workspace, bot := labels["botbox.workspace_id"], labels["botbox.bot_id"]
	relay, relayErr := d.inspectProxy(ctx, "botbox-egress-"+sanitizeIdentifier(bot), workspace, bot, "relay")
	worker, workerErr := d.inspectProxy(ctx, "botbox-egress-worker-"+sanitizeIdentifier(bot), workspace, bot, "worker")
	if relayErr != nil || workerErr != nil || !matchingProxyPair(relay, worker) ||
		!relay.State.Running || !worker.State.Running || relay.State.Health.Status != "healthy" {
		return errors.New("metered proxy unavailable")
	}
	return nil
}

func (d *Driver) waitProxyReady(ctx context.Context, id compute.EnvID) error {
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := d.CLIProxyReady(waitCtx, id); err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return errors.New("metered proxy did not pass the startup health check")
		case <-ticker.C:
		}
	}
}

func validateProxyOptions(opts *ProxyOptions) error {
	if opts == nil {
		return nil
	}
	if strings.TrimSpace(opts.Image) == "" || strings.TrimSpace(opts.DatabaseURL) == "" || strings.TrimSpace(opts.KeySpec) == "" {
		return errors.New("proxy configuration incomplete")
	}
	return nil
}
