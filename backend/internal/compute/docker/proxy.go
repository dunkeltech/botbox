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

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

// ProxyOptions configures trusted per-bot proxy containers. Credentials belong
// only to these control-plane containers and never to the bot sandbox spec.
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
	proxyIngressVersion = "bot-network-v1"
)

func (d *Driver) ensureProxy(ctx context.Context, workspace, bot, network string) error {
	if d.proxy == nil {
		return nil
	}
	if workspace == "" || bot == "" {
		return errors.New("proxy identity unavailable")
	}
	name := "botbox-egress-" + sanitizeIdentifier(bot)
	// Existing proxies must have the same trusted identity. Container names alone
	// do not prove that a previous deployment enabled metering.
	existing, err := d.inspectContainer(ctx, name)
	if err == nil {
		if existing.Config.Labels["botbox.workspace_id"] != workspace || existing.Config.Labels["botbox.bot_id"] != bot || existing.Config.Labels["botbox.egress_proxy"] != proxyMarker {
			return errors.New("proxy identity mismatch")
		}
		if existing.Config.Labels[proxyIngressLabel] == proxyIngressVersion {
			if existing.State.Running {
				return nil
			}
			return d.startProxy(ctx, existing.ID)
		}
		// Legacy proxies listen on the shared egress bridge. Replace the
		// verified sidecar before restoring access under the new boundary.
		status, removeErr := d.doJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(existing.ID)+"?force=true", nil, nil)
		if removeErr != nil && status != http.StatusNotFound {
			return errors.New("legacy proxy removal failed")
		}
	}
	if err != nil && !errors.Is(err, compute.ErrEnvironmentNotFound) {
		return errors.New("proxy inspection failed")
	}
	subnet, err := d.botProxySubnet(ctx, network, workspace, bot)
	if err != nil {
		return err
	}
	imageID, err := d.proxyIngressImage(ctx)
	if err != nil {
		return err
	}
	env := []string{"EGRESS_PROXY_BOT_SUBNET=" + subnet, "DATABASE_URL=" + d.proxy.DatabaseURL, "BOTBOX_SECRETS_KEK=" + d.proxy.KeySpec, "BOTBOX_WORKSPACE_ID=" + workspace, "BOTBOX_BOT_ID=" + bot}
	if strings.TrimSpace(d.proxy.NATSURL) != "" {
		env = append(env, "NATS_URL="+strings.TrimSpace(d.proxy.NATSURL))
	}
	if strings.TrimSpace(d.proxy.AuthBrokerURL) != "" && strings.TrimSpace(d.proxy.AuthBrokerToken) != "" {
		env = append(env,
			"EGRESS_PROXY_AUTH_BROKER_URL="+strings.TrimSpace(d.proxy.AuthBrokerURL),
			"EGRESS_PROXY_AUTH_BROKER_TOKEN="+strings.TrimSpace(d.proxy.AuthBrokerToken),
		)
	}
	payload := map[string]any{
		"Image":            imageID,
		"User":             "65532:65532",
		"Env":              env,
		"Labels":           map[string]string{"botbox.proxy": "true", "botbox.workspace_id": workspace, "botbox.bot_id": bot, "botbox.egress_proxy": proxyMarker, proxyIngressLabel: proxyIngressVersion},
		"HostConfig":       map[string]any{"Privileged": false, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "ReadonlyRootfs": true, "NetworkMode": "bridge", "Memory": int64(256 << 20), "NanoCPUs": int64(1000000000), "PidsLimit": 64, "Sysctls": map[string]string{"net.ipv4.ip_forward": "0"}},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{"bridge": map[string]any{}}},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if _, err = d.doJSON(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), payload, &created); err != nil || created.ID == "" {
		return errors.New("proxy creation failed")
	}
	// The proxy alone joins both networks. The sandbox retains one internal
	// network and receives no route through the proxy's network namespace.
	if _, err = d.doJSON(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", map[string]any{"Container": created.ID, "EndpointConfig": map[string]any{"Aliases": []string{"egress"}}}, nil); err != nil {
		_, _ = d.doJSON(context.WithoutCancel(ctx), http.MethodDelete, "/containers/"+url.PathEscape(created.ID)+"?force=true", nil, nil)
		return errors.New("proxy network attachment failed")
	}
	return d.startProxy(ctx, created.ID)
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
		Internal bool
		Labels   map[string]string
		IPAM     struct {
			Config []struct{ Subnet string }
		}
	}
	if _, err := d.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, &network); err != nil {
		return "", errors.New("inspect bot network failed")
	}
	if !network.Internal || network.Labels["botbox.workspace_id"] != workspace || network.Labels["botbox.bot_id"] != bot {
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
		if !slices.Contains(sandbox.Names, "/botbox-bot-"+sanitizeIdentifier(bot)) {
			return errors.New("metered sandbox identity mismatch")
		}
		if err := d.ensureProxy(ctx, workspace, bot, "botbox-net-"+sanitizeIdentifier(bot)); err != nil {
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
	if labels["botbox.egress_proxy"] != proxyMarker {
		return errors.New("sandbox requires metered proxy recreation")
	}
	proxy, err := d.inspectContainer(ctx, "botbox-egress-"+sanitizeIdentifier(labels["botbox.bot_id"]))
	if err != nil || !proxy.State.Running || proxy.Config.Labels["botbox.workspace_id"] != labels["botbox.workspace_id"] || proxy.Config.Labels["botbox.bot_id"] != labels["botbox.bot_id"] || proxy.Config.Labels["botbox.egress_proxy"] != proxyMarker || proxy.Config.Labels[proxyIngressLabel] != proxyIngressVersion {
		return errors.New("metered proxy unavailable")
	}
	return nil
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
