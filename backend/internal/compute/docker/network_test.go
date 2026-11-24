// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

const poolFull = "all predefined address pools have been fully subnetted"

var fakeNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type fakeNetwork struct {
	dockerNetwork
	users        int
	deleteStatus int
}

// fakeEngine models the parts of the Docker Engine API that network cleanup
// uses, including a fixed number of free subnets.
type fakeEngine struct {
	mu             sync.Mutex
	networks       map[string]*fakeNetwork
	capacity       int
	alwaysFull     bool
	poolMessage    string
	createStatus   int
	listStatus     int
	inspectStatus  int
	containerLabel map[string]string
	onPoolFull     func()

	createAttempts int
	listCalls      int
	deleted        []string
}

func newFakeEngine(networks ...*fakeNetwork) *fakeEngine {
	f := &fakeEngine{networks: map[string]*fakeNetwork{}, capacity: 1000, poolMessage: poolFull}
	for _, n := range networks {
		if n.ID == "" {
			n.ID = "id-" + n.Name
		}
		f.networks[n.Name] = n
	}
	return f
}

func botNet(name, workspace, bot string, age time.Duration) *fakeNetwork {
	return &fakeNetwork{dockerNetwork: dockerNetwork{
		Name:    name,
		Created: fakeNow.Add(-age),
		Labels:  map[string]string{"botbox.managed": "true", "botbox.workspace_id": workspace, "botbox.bot_id": bot},
	}}
}

func (f *fakeEngine) lookup(ref string) *fakeNetwork {
	if n, ok := f.networks[ref]; ok {
		return n
	}
	for _, n := range f.networks {
		if n.ID == ref {
			return n
		}
	}
	return nil
}

func writeDockerError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.45")
	var filters map[string][]string
	_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)

	switch {
	case r.Method == http.MethodGet && path == "/networks":
		f.listCalls++
		if f.listStatus != 0 {
			writeDockerError(w, f.listStatus, "list failed")
			return
		}
		out := []dockerNetwork{}
		for _, n := range f.networks {
			if _, labeled := n.Labels["botbox.bot_id"]; labeled || !slices.Contains(filters["label"], "botbox.bot_id") {
				out = append(out, n.dockerNetwork)
			}
		}
		_ = json.NewEncoder(w).Encode(out)

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/networks/"):
		if f.inspectStatus != 0 {
			writeDockerError(w, f.inspectStatus, "inspect failed")
			return
		}
		n := f.lookup(strings.TrimPrefix(path, "/networks/"))
		if n == nil {
			writeDockerError(w, http.StatusNotFound, "network not found")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id": n.ID, "Name": n.Name, "Created": n.Created, "Labels": n.Labels, "Internal": true,
		})

	case r.Method == http.MethodPost && path == "/networks/create":
		f.createAttempts++
		if f.createStatus != 0 {
			writeDockerError(w, f.createStatus, "docker daemon unavailable")
			return
		}
		if f.alwaysFull || len(f.networks) >= f.capacity {
			if f.onPoolFull != nil {
				f.onPoolFull()
			}
			writeDockerError(w, http.StatusBadRequest, f.poolMessage)
			return
		}
		var req struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.networks[req.Name] = &fakeNetwork{dockerNetwork: dockerNetwork{ID: "id-" + req.Name, Name: req.Name, Created: fakeNow, Labels: req.Labels}}
		w.WriteHeader(http.StatusCreated)

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/networks/"):
		n := f.lookup(strings.TrimPrefix(path, "/networks/"))
		if n == nil {
			writeDockerError(w, http.StatusNotFound, "network not found")
			return
		}
		if n.deleteStatus != 0 {
			writeDockerError(w, n.deleteStatus, "error while removing network: network has active endpoints")
			return
		}
		delete(f.networks, n.Name)
		f.deleted = append(f.deleted, n.Name)
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && path == "/containers/json":
		users := []map[string]string{}
		for _, name := range filters["network"] {
			if n := f.networks[name]; n != nil {
				for range n.users {
					users = append(users, map[string]string{"Id": "user-of-" + name})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(users)

	case r.Method == http.MethodPost && path == "/volumes/create":
		w.WriteHeader(http.StatusCreated)

	case r.Method == http.MethodPost && path == "/containers/create":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"cid"}`))

	case r.Method == http.MethodGet && path == "/containers/cid/json":
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid", "Config": map[string]any{"Labels": f.containerLabel}})

	case r.Method == http.MethodDelete && path == "/containers/cid":
		w.WriteHeader(http.StatusNoContent)

	default:
		writeDockerError(w, http.StatusNotFound, "unexpected "+r.Method+" "+path)
	}
}

func newFakeDriver(t *testing.T, f *fakeEngine) (*Driver, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	d, err := NewDriver(Options{BaseURL: srv.URL, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	d.now = func() time.Time { return fakeNow }
	return d, &logs
}

func TestDestroyEnvironmentRemovesOnlyUnusedOwnedNetworks(t *testing.T) {
	t.Parallel()
	owner := map[string]string{"botbox.managed": "true", "botbox.workspace_id": "ws-1", "botbox.bot_id": "bot-1"}
	desktop, acp, legacy := proxyNetworkName("bot-1"), proxyNetworkName(agyACPProxyNamespace("bot-1")), legacyNetworkName("bot-1")

	cases := []struct {
		name        string
		labels      map[string]string
		networks    []*fakeNetwork
		setup       func(*fakeEngine)
		wantDeleted []string
		wantLog     string
	}{
		{
			name:   "removes desktop, ACP and legacy networks",
			labels: owner,
			networks: []*fakeNetwork{
				botNet(desktop, "ws-1", "bot-1", time.Second),
				botNet(acp, "ws-1", "bot-1", time.Second),
				botNet(legacy, "ws-1", "bot-1", time.Hour),
			},
			wantDeleted: []string{desktop, acp, legacy},
		},
		{
			name:   "networks already gone",
			labels: owner,
		},
		{
			name:     "network of another workspace stays",
			labels:   owner,
			networks: []*fakeNetwork{botNet(desktop, "ws-2", "bot-1", time.Hour)},
		},
		{
			name:     "network labeled for another bot stays",
			labels:   owner,
			networks: []*fakeNetwork{botNet(desktop, "ws-1", "bot-2", time.Hour)},
		},
		{
			name:   "network without botbox labels stays",
			labels: owner,
			networks: []*fakeNetwork{{dockerNetwork: dockerNetwork{
				Name: legacy, Created: fakeNow.Add(-time.Hour), Labels: map[string]string{"com.docker.compose.project": "other"},
			}}},
		},
		{
			name:   "network still used by a stopped container stays",
			labels: owner,
			networks: []*fakeNetwork{
				func() *fakeNetwork { n := botNet(legacy, "ws-1", "bot-1", time.Hour); n.users = 1; return n }(),
				botNet(desktop, "ws-1", "bot-1", time.Hour),
			},
			wantDeleted: []string{desktop},
		},
		{
			name:   "docker refuses delete because a container attached meanwhile",
			labels: owner,
			networks: []*fakeNetwork{
				func() *fakeNetwork {
					n := botNet(desktop, "ws-1", "bot-1", time.Hour)
					n.deleteStatus = http.StatusForbidden
					return n
				}(),
			},
		},
		{
			name:     "engine failure is logged and does not fail the delete",
			labels:   owner,
			networks: []*fakeNetwork{botNet(desktop, "ws-1", "bot-1", time.Hour)},
			setup:    func(f *fakeEngine) { f.inspectStatus = http.StatusInternalServerError },
			wantLog:  "remove bot networks",
		},
		{
			name:     "container without bot labels touches no network",
			labels:   map[string]string{"botbox.managed": "true"},
			networks: []*fakeNetwork{botNet(desktop, "ws-1", "bot-1", time.Hour)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeEngine(tc.networks...)
			f.containerLabel = tc.labels
			if tc.setup != nil {
				tc.setup(f)
			}
			d, logs := newFakeDriver(t, f)
			if err := d.DestroyEnvironment(context.Background(), "cid"); err != nil {
				t.Fatalf("DestroyEnvironment: %v", err)
			}
			slices.Sort(f.deleted)
			slices.Sort(tc.wantDeleted)
			if !slices.Equal(f.deleted, tc.wantDeleted) {
				t.Fatalf("deleted %v, want %v", f.deleted, tc.wantDeleted)
			}
			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Fatalf("log %q lacks %q", logs.String(), tc.wantLog)
			}
			if tc.wantLog == "" && logs.Len() > 0 {
				t.Fatalf("unexpected log: %s", logs.String())
			}
		})
	}
}

func TestCreateEnvironmentReclaimsOrphanNetworksWhenPoolIsFull(t *testing.T) {
	t.Parallel()
	busy := botNet(proxyNetworkName("bot-busy"), "ws-1", "bot-busy", time.Hour)
	busy.users = 1
	f := newFakeEngine(
		botNet(legacyNetworkName("bot-old"), "ws-1", "bot-old", time.Hour),
		botNet(proxyNetworkName("bot-gone"), "ws-2", "bot-gone", 24*time.Hour),
		// Just created by a concurrent start that has not attached its proxy yet.
		botNet(proxyNetworkName("bot-young"), "ws-1", "bot-young", 30*time.Second),
		busy,
		// Labeled like a bot network, but the name does not belong to that bot.
		botNet("rakazo-computer-x", "ws-1", "bot-x", time.Hour),
		botNet(proxyNetworkName("bot-y"), "ws-1", "bot-other", time.Hour),
		&fakeNetwork{dockerNetwork: dockerNetwork{Name: "studio-core", Created: fakeNow.Add(-time.Hour)}},
	)
	f.capacity = len(f.networks)
	d, logs := newFakeDriver(t, f)

	id, err := d.CreateEnvironment(context.Background(), compute.BotConfig{WorkspaceID: "ws-1", BotID: "bot-new"})
	if err != nil || id != "cid" {
		t.Fatalf("CreateEnvironment = %q, %v", id, err)
	}
	slices.Sort(f.deleted)
	want := []string{proxyNetworkName("bot-gone"), legacyNetworkName("bot-old")}
	if !slices.Equal(f.deleted, want) {
		t.Fatalf("deleted %v, want %v", f.deleted, want)
	}
	if f.createAttempts != 2 || f.networks[legacyNetworkName("bot-new")] == nil {
		t.Fatalf("network not created after reclaim: attempts=%d", f.createAttempts)
	}
	if !strings.Contains(logs.String(), "reclaimed orphan bot networks") {
		t.Fatalf("reclaim not logged: %s", logs.String())
	}
}

func TestCreateEnvironmentReportsNetworkCapacity(t *testing.T) {
	t.Parallel()
	orphan := func() *fakeNetwork { return botNet(legacyNetworkName("bot-old"), "ws-1", "bot-old", time.Hour) }
	busy := func() *fakeNetwork {
		n := botNet(legacyNetworkName("bot-busy"), "ws-1", "bot-busy", time.Hour)
		n.users = 1
		return n
	}
	cases := []struct {
		name         string
		networks     []*fakeNetwork
		setup        func(*fakeEngine)
		wantCapacity bool
		wantAttempts int
		wantDeleted  int
		wantList     int
		wantLog      string
	}{
		{name: "nothing to reclaim", networks: []*fakeNetwork{busy()}, wantCapacity: true, wantAttempts: 1, wantList: 1},
		{
			name: "message of engines before 23.0", networks: []*fakeNetwork{busy()},
			setup: func(f *fakeEngine) {
				f.poolMessage = "could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network"
			},
			wantCapacity: true, wantAttempts: 1, wantList: 1,
		},
		{
			name: "pool still full after reclaim", networks: []*fakeNetwork{orphan()},
			setup:        func(f *fakeEngine) { f.alwaysFull = true },
			wantCapacity: true, wantAttempts: 2, wantDeleted: 1, wantList: 1,
		},
		{
			name: "listing networks fails", networks: []*fakeNetwork{orphan()},
			setup:        func(f *fakeEngine) { f.listStatus = http.StatusInternalServerError },
			wantCapacity: true, wantAttempts: 1, wantList: 1, wantLog: "reclaim orphan bot networks",
		},
		{
			name: "other create errors are not capacity errors", networks: []*fakeNetwork{orphan()},
			setup:        func(f *fakeEngine) { f.createStatus = http.StatusInternalServerError },
			wantCapacity: false, wantAttempts: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeEngine(tc.networks...)
			f.capacity = len(f.networks)
			if tc.setup != nil {
				tc.setup(f)
			}
			d, logs := newFakeDriver(t, f)
			_, err := d.CreateEnvironment(context.Background(), compute.BotConfig{WorkspaceID: "ws-1", BotID: "bot-new"})
			if err == nil {
				t.Fatal("CreateEnvironment succeeded without a free subnet")
			}
			if got := errors.Is(err, compute.ErrNetworkCapacity); got != tc.wantCapacity {
				t.Fatalf("ErrNetworkCapacity = %v, want %v (err %v)", got, tc.wantCapacity, err)
			}
			if f.createAttempts != tc.wantAttempts || len(f.deleted) != tc.wantDeleted || f.listCalls != tc.wantList {
				t.Fatalf("attempts=%d deleted=%v lists=%d", f.createAttempts, f.deleted, f.listCalls)
			}
			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Fatalf("log %q lacks %q", logs.String(), tc.wantLog)
			}
		})
	}
}

func TestCreateEnvironmentStopsReclaimWhenCanceled(t *testing.T) {
	t.Parallel()
	f := newFakeEngine(botNet(legacyNetworkName("bot-old"), "ws-1", "bot-old", time.Hour))
	f.capacity = len(f.networks)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onPoolFull = cancel
	d, _ := newFakeDriver(t, f)

	_, err := d.CreateEnvironment(ctx, compute.BotConfig{WorkspaceID: "ws-1", BotID: "bot-new"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("canceled create still removed networks: %v", f.deleted)
	}
}
