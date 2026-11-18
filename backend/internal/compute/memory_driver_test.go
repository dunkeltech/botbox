// SPDX-License-Identifier: AGPL-3.0-only

package compute_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

func TestMemoryDriverLifecyclePersistenceAndSecurity(t *testing.T) {
	t.Parallel()

	drv, err := compute.NewMemoryDriver(compute.MemoryDriverOptions{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewMemoryDriver failed: %v", err)
	}
	t.Cleanup(drv.Close)

	envID, err := drv.CreateEnvironment(context.Background(), compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-1",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment failed: %v", err)
	}

	if st, err := drv.Status(context.Background(), envID); err != nil || st != compute.StatusStopped {
		t.Fatalf("initial status = (%q, %v), want stopped", st, err)
	}

	// Operations while stopped fail with ErrEnvironmentNotRunning.
	if _, err := drv.ExecCommand(context.Background(), envID, "/home/bot", []string{"echo", "hi"}); !errors.Is(err, compute.ErrEnvironmentNotRunning) {
		t.Fatalf("ExecCommand while stopped err = %v, want ErrEnvironmentNotRunning", err)
	}

	if err := drv.StartEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("StartEnvironment failed: %v", err)
	}

	if err := drv.WriteFile(context.Background(), envID, "/home/bot/persist.txt", []byte("keep-across-restart")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	res, err := drv.ExecCommand(context.Background(), envID, "/home/bot", []string{"sh", "-c", "cat persist.txt"})
	if err != nil || res.ExitCode != 0 || res.Stdout != "keep-across-restart" {
		t.Fatalf("ExecCommand cat result = %+v err=%v", res, err)
	}

	// Negative path containment check inside MemoryDriver.
	if _, err := drv.ReadFile(context.Background(), envID, "../etc/passwd"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ReadFile traversal code = %v, want PermissionDenied", status.Code(err))
	}

	// Stop & restart; persistent home directory must retain persist.txt.
	if err := drv.StopEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("StopEnvironment failed: %v", err)
	}
	if err := drv.StartEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("restart StartEnvironment failed: %v", err)
	}

	content, err := drv.ReadFile(context.Background(), envID, "persist.txt")
	if err != nil || string(content) != "keep-across-restart" {
		t.Fatalf("ReadFile after restart = (%q, %v), want keep-across-restart", string(content), err)
	}

	entries, err := drv.ListFiles(context.Background(), envID, "/home/bot")
	if err != nil || len(entries) < 1 {
		t.Fatalf("ListFiles failed: entries=%+v err=%v", entries, err)
	}

	vncURL, err := drv.GetVNCStreamURL(context.Background(), envID)
	if err != nil || !strings.HasPrefix(vncURL, "ws://127.0.0.1:") {
		t.Fatalf("GetVNCStreamURL = (%q, %v)", vncURL, err)
	}

	missing, err := drv.Reconcile(context.Background(), []compute.EnvID{envID, "missing-env"})
	if err != nil || len(missing) != 1 || missing[0] != "missing-env" {
		t.Fatalf("Reconcile = (%v, %v), want [missing-env]", missing, err)
	}

	if err := drv.DestroyEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("DestroyEnvironment failed: %v", err)
	}
}
