// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
)

func TestNormalRuntimeDefaultsToDocker(t *testing.T) {
	t.Setenv("BOTBOX_COMPUTE_DRIVER", "")
	t.Setenv("BOTBOX_EGRESS_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	driver, closeDriver, err := initComputeDriver(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if closeDriver != nil {
		defer closeDriver()
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*docker.Driver); !ok {
		t.Fatal("normal runtime silently selected the simulated computer")
	}
}

func TestMemoryRuntimeRequiresExplicitSelection(t *testing.T) {
	t.Setenv("BOTBOX_COMPUTE_DRIVER", "memory")
	driver, closeDriver, err := initComputeDriver(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if closeDriver != nil {
		defer closeDriver()
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*compute.MemoryDriver); !ok {
		t.Fatal("explicit development simulator unavailable")
	}
	t.Setenv("BOTBOX_COMPUTE_DRIVER", "mistyped-driver")
	if _, _, err = initComputeDriver(slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("unknown driver silently fell back to simulation")
	}
}

func TestProxyDatabaseURL(t *testing.T) {
	for _, tc := range []struct{ database, override, want string }{
		{"postgres://user:fixture-password@127.0.0.1:5432/botbox?sslmode=disable", "", "postgres://user:fixture-password@host.docker.internal:5432/botbox?sslmode=disable"},
		{"postgres://user:fixture-password@127.0.0.1:15432/botbox?sslmode=disable", "", "postgres://user:fixture-password@host.docker.internal:15432/botbox?sslmode=disable"},
		{"postgresql://user@localhost/botbox", "", "postgresql://user@host.docker.internal:5432/botbox"},
		{"postgres://user@[::1]:5544/botbox", "", "postgres://user@host.docker.internal:5544/botbox"},
		{"postgres://user@database:5432/botbox", "", "postgres://user@database:5432/botbox"},
		{"postgres://user@localhost:5432/botbox", "postgres://user@explicit-db:5444/botbox", "postgres://user@explicit-db:5444/botbox"},
		{"", "", ""},
	} {
		got, err := proxyDatabaseURL(tc.database, tc.override)
		if err != nil || got != tc.want {
			t.Fatalf("proxy URL mismatch: %v", err)
		}
	}
	for _, bad := range []string{"not-a-url", "http://database", "postgres://", "postgres://user:fixture-password@%broken"} {
		if _, err := proxyDatabaseURL(bad, ""); err == nil {
			t.Fatal("invalid database URL admitted")
		} else if strings.Contains(err.Error(), "fixture-password") {
			t.Fatal("database credentials leaked")
		}
	}
}
