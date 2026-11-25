// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/bots"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/computermcp"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/google/uuid"
)

const (
	agyACPLabel      = "botbox.agy_acp"
	agyACPWorkspace  = "/tmp/agy-workspace" //nolint:gosec // fixed working directory, not a credential
	agyACPEntrypoint = `
umask 077
mkdir -p /home/bot/.gemini/antigravity-cli /tmp/agy-workspace
cat /etc/ssl/certs/ca-certificates.crt /run/botbox/egress-ca/ca.crt > /tmp/botbox-acp-ca.pem
exec sleep infinity
`
)

// RunAgyACP runs a native ACP turn in a separate container. Only Docker's
// trusted exec channel reaches its stdio, and the work sandbox never mounts
// the volume where agy keeps its own login.
func (d *Driver) RunAgyACP(ctx context.Context, sandbox compute.EnvID, prompt, session, model string, tools *computermcp.Session, emit func(engine.Event) error) error {
	id, err := d.ensureAgyACPContainer(ctx, sandbox)
	if err != nil {
		return engine.WithFailureCode(engine.FailureCLIUnavailable, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := d.dialAgyACP(ctx, id)
	if err != nil {
		return engine.WithFailureCode(engine.FailureCLIUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	turn := engine.ACPTurn{Directory: agyACPWorkspace, Prompt: prompt, SessionID: session, Model: model}
	var emitMu sync.Mutex
	report := func(event engine.Event) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(event)
	}
	if tools != nil {
		server, finish, err := d.startComputerMCP(ctx, id, computerMCPHandler(tools, report))
		if err != nil {
			return engine.WithFailureCode(engine.FailureCLIUnavailable, err)
		}
		defer tools.Close()
		defer finish()
		turn.MCPServers = []engine.ACPMCPServer{server}
		turn.ToolValidators = computermcp.ModelTools()
	}
	err = engine.RunACP(conn, conn, turn, report)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func computerMCPHandler(tools *computermcp.Session, report func(engine.Event) error) *agent.MCPHandler {
	return agent.NewScopedMCPHandler(computermcp.Tools(), func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		if err := computermcp.ValidateCall(computermcp.ServerName, name, args); err != nil {
			return nil, err
		}
		id := uuid.NewString()
		if err := report(engine.Event{Kind: engine.EventKindToolCall, ToolID: id, ToolName: name, Status: "action"}); err != nil {
			return nil, err
		}
		result, err := tools.Call(ctx, name, args)
		if err := report(engine.Event{Kind: engine.EventKindToolResult, ToolID: id, ToolName: name, IsError: err != nil}); err != nil {
			return nil, err
		}
		return result, err
	})
}

func (d *Driver) ensureAgyACPContainer(ctx context.Context, sandbox compute.EnvID) (string, error) {
	if d.proxy == nil {
		return "", errors.New("ACP requires a metered network")
	}
	work, err := d.inspectContainer(ctx, string(sandbox))
	if err != nil {
		return "", err
	}
	workspace, bot := work.Config.Labels["botbox.workspace_id"], work.Config.Labels["botbox.bot_id"]
	if !work.State.Running || work.Config.Labels["botbox.egress_proxy"] != proxyMarker ||
		work.Config.Labels[proxyIngressLabel] != proxyIngressVersion ||
		uuid.Validate(workspace) != nil || uuid.Validate(bot) != nil {
		return "", errors.New("ACP requires a managed bot")
	}
	if _, attached := work.NetworkSettings.Networks[proxyNetworkName(bot)]; !attached {
		return "", errors.New("ACP requires an isolated bot network")
	}
	spec := agyACPSpec(workspace, bot)
	if work.Image != "" {
		spec.Image = work.Image
	}
	namespace := agyACPProxyNamespace(bot)
	if err := d.ensureInternalNetwork(ctx, spec.HostConfig.NetworkMode, spec.Labels); err != nil {
		return "", err
	}
	if err := d.ensureProxyPair(ctx, workspace, bot, namespace, spec.HostConfig.NetworkMode); err != nil {
		return "", err
	}
	if err := waitForProxy(ctx, func(ctx context.Context) error {
		return d.proxyPairReady(ctx, workspace, bot, namespace)
	}); err != nil {
		return "", err
	}
	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	current, err := d.inspectContainer(ctx, string(sandbox))
	if err != nil || !current.State.Running {
		return "", compute.ErrEnvironmentNotRunning
	}
	container, err := d.inspectAgyACPContainer(ctx, workspace, bot)
	if err != nil {
		return "", err
	}
	if container != nil {
		if !container.State.Running {
			if err := d.startProxy(ctx, container.ID); err != nil {
				return "", err
			}
		}
		return container.ID, nil
	}
	if err := ValidateContainerSpec(spec); err != nil {
		return "", err
	}
	if err := d.ensureVolume(ctx, agyACPName(bot), spec.Labels); err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if _, err := d.doJSON(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(agyACPName(bot)), spec, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("ACP container unavailable")
	}
	if err := d.startProxy(ctx, created.ID); err != nil {
		return "", err
	}
	return created.ID, nil
}

func agyACPName(bot string) string {
	return "botbox-agy-acp-" + sanitizeIdentifier(bot)
}

func agyACPProxyNamespace(bot string) string {
	return bot + "-agy-acp"
}

func agyACPSpec(workspace, bot string) ContainerCreateSpec {
	namespace := agyACPProxyNamespace(bot)
	network := proxyNetworkName(namespace)
	pids := int64(compute.DefaultPidsLimit)
	return ContainerCreateSpec{
		Image:      compute.DefaultSandboxImage,
		User:       "bot",
		Entrypoint: []string{"/bin/sh", "-ec", agyACPEntrypoint},
		Cmd:        []string{"botbox-agy-acp"},
		Env: []string{
			"HOME=/home/bot", "HTTP_PROXY=http://egress:8081", "HTTPS_PROXY=http://egress:8081",
			"http_proxy=http://egress:8081", "https_proxy=http://egress:8081", "SSL_CERT_FILE=/tmp/botbox-acp-ca.pem",
		},
		Labels: map[string]string{"botbox.workspace_id": workspace, "botbox.bot_id": bot, agyACPLabel: "true"},
		HostConfig: ContainerHostConfig{
			CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
			NanoCPUs: int64(compute.DefaultCPUMillis) * 1000000, Memory: int64(compute.DefaultMemoryMiB) * 1024 * 1024, PidsLimit: &pids,
			NetworkMode: network, ReadonlyRootfs: true, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=128m"},
			Binds: []string{agyACPName(bot) + ":/home/bot", proxyCAVolume(namespace, "public") + ":/run/botbox/egress-ca:ro"},
		},
		NetworkingConfig: ContainerNetworkingConfig{EndpointsConfig: map[string]map[string]any{network: {}}},
	}
}

func (d *Driver) inspectAgyACPContainer(ctx context.Context, workspace, bot string) (*containerInspectResponse, error) {
	container, err := d.inspectContainer(ctx, agyACPName(bot))
	if errors.Is(err, compute.ErrEnvironmentNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	labels := container.Config.Labels
	if labels[agyACPLabel] != "true" || labels["botbox.workspace_id"] != workspace || labels["botbox.bot_id"] != bot {
		return nil, compute.ErrSecurityViolation
	}
	if _, attached := container.NetworkSettings.Networks[proxyNetworkName(agyACPProxyNamespace(bot))]; !attached || len(container.NetworkSettings.Networks) != 1 {
		return nil, compute.ErrSecurityViolation
	}
	return container, nil
}

func (d *Driver) removeAgyACPContainer(ctx context.Context, workspace, bot string) error {
	if uuid.Validate(workspace) != nil || uuid.Validate(bot) != nil {
		return nil
	}
	container, err := d.inspectAgyACPContainer(ctx, workspace, bot)
	if err != nil {
		return err
	}
	if container != nil {
		if err := d.removeProxy(ctx, container.ID); err != nil {
			return err
		}
	}
	namespace := sanitizeIdentifier(agyACPProxyNamespace(bot))
	for role, name := range map[string]string{"relay": "botbox-egress-" + namespace, "worker": "botbox-egress-worker-" + namespace} {
		proxy, err := d.inspectProxy(ctx, name, workspace, bot, role)
		if err != nil {
			return err
		}
		if proxy != nil {
			if err := d.removeProxy(ctx, proxy.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Driver) stopAgyACPCompanion(ctx context.Context, sandbox compute.EnvID) error {
	work, err := d.inspectContainer(ctx, string(sandbox))
	if err != nil {
		return err
	}
	workspace, bot := work.Config.Labels["botbox.workspace_id"], work.Config.Labels["botbox.bot_id"]
	if work.Config.Labels[proxyIngressLabel] != proxyIngressVersion || uuid.Validate(workspace) != nil || uuid.Validate(bot) != nil {
		return nil
	}
	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	runtime, err := d.inspectAgyACPContainer(ctx, workspace, bot)
	if err != nil || runtime == nil || !runtime.State.Running {
		return err
	}
	code, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(runtime.ID)+"/stop?t=2", nil, nil)
	if err != nil && code != http.StatusNotModified && code != http.StatusNotFound {
		return errors.New("ACP runtime stop failed")
	}
	return nil
}

func (d *Driver) dialAgyACP(ctx context.Context, id string) (net.Conn, error) {
	const launcher = `
umask 077
dir=/home/bot/.gemini/antigravity-cli
tmp=$(mktemp "$dir/.botbox-settings.XXXXXX")
printf '%s\n' '{"toolPermission":"request-review","permissions":{"allow":["mcp(botbox-computer/*)"],"deny":["command(*)","unsandboxed(*)","read_file(*)","write_file(*)","read_url(*)","execute_url(*)"]}}' > "$tmp"
mv -f "$tmp" "$dir/settings.json"
exec /usr/local/bin/agy acp --sandbox --disable-telemetry --include-default-customizations=false --log-file /dev/null
`
	return d.dialACPExec(ctx, id, []string{"/bin/sh", "-ec", launcher})
}

func (d *Driver) dialACPExec(ctx context.Context, id string, command []string) (net.Conn, error) {
	// Docker does not signal an exec process when a client disconnects. A
	// process deadline also bounds orphaned workers after an API restart.
	deadline, exists := ctx.Deadline()
	timeout := bots.DefaultTurnTimeout
	if exists {
		timeout = max(time.Second, min(bots.MaxTurnTimeout, time.Until(deadline)+time.Second-1))
	}
	payload := map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": false,
		"WorkingDir": agyACPWorkspace,
		"Cmd":        append([]string{"/usr/bin/timeout", "--kill-after=2s", fmt.Sprintf("%ds", timeout/time.Second)}, command...),
	}
	var created struct {
		ID string `json:"Id"`
	}
	if _, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", payload, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("docker did not create an ACP process")
	}
	return d.hijack(ctx, "/exec/"+url.PathEscape(created.ID)+"/start", []byte(`{"Detach":false,"Tty":false}`))
}
