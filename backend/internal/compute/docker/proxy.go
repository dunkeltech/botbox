package docker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

// ProxyOptions configures trusted per-bot proxy containers. Credentials belong
// only to these control-plane containers and never to the bot sandbox spec.
type ProxyOptions struct {
	Image       string
	DatabaseURL string
	KeySpec     string
}

const proxyMarker = "metered-v1"

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
		if existing.State.Running {
			return nil
		}
		return d.startProxy(ctx, existing.ID)
	}
	if !errors.Is(err, compute.ErrEnvironmentNotFound) {
		return errors.New("proxy inspection failed")
	}
	payload := map[string]any{
		"Image":            d.proxy.Image,
		"User":             "65532:65532",
		"Env":              []string{"EGRESS_PROXY_ADDR=0.0.0.0:8081", "DATABASE_URL=" + d.proxy.DatabaseURL, "BOTBOX_SECRETS_KEK=" + d.proxy.KeySpec, "BOTBOX_WORKSPACE_ID=" + workspace, "BOTBOX_BOT_ID=" + bot},
		"Labels":           map[string]string{"botbox.proxy": "true", "botbox.workspace_id": workspace, "botbox.bot_id": bot, "botbox.egress_proxy": proxyMarker},
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

func (d *Driver) startProxy(ctx context.Context, id string) error {
	code, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
	if err != nil && code != http.StatusNotModified {
		return errors.New("proxy start failed")
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
	if err != nil || !proxy.State.Running || proxy.Config.Labels["botbox.workspace_id"] != labels["botbox.workspace_id"] || proxy.Config.Labels["botbox.bot_id"] != labels["botbox.bot_id"] || proxy.Config.Labels["botbox.egress_proxy"] != proxyMarker {
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
