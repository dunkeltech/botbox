package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindPlaywrightChromium(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nonExecDir := filepath.Join(root, "chromium-1248", "chrome-linux")
	if err := os.MkdirAll(nonExecDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonExecDir, "chrome"), []byte("not-exec"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findPlaywrightChromium(root); got != "" {
		t.Fatalf("expected non-executable file to be ignored, got %q", got)
	}

	execDir := filepath.Join(root, "chromium_headless_shell-1248", "chrome-headless-shell-mac-arm64")
	if err := os.MkdirAll(execDir, 0o750); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(execDir, "chrome-headless-shell")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test fixture binary
		t.Fatal(err)
	}
	if got := findPlaywrightChromium(root); got != want {
		t.Fatalf("findPlaywrightChromium = %q, want %q", got, want)
	}

	// Verify symlinked Nix-store style directories inside PLAYWRIGHT_BROWSERS_PATH are followed.
	symParent := t.TempDir()
	targetDir := t.TempDir()
	targetSub := filepath.Join(targetDir, "chrome-headless-shell-mac-arm64")
	if err := os.MkdirAll(targetSub, 0o750); err != nil {
		t.Fatal(err)
	}
	symWant := filepath.Join(targetSub, "chrome-headless-shell")
	if err := os.WriteFile(symWant, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test fixture binary
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(symParent, "chromium_headless_shell-1248")); err != nil {
		t.Fatal(err)
	}
	gotSym := findPlaywrightChromium(symParent)
	resolvedWant, _ := filepath.EvalSymlinks(symWant)
	if gotSym != resolvedWant {
		t.Fatalf("findPlaywrightChromium(symlink) = %q, want %q", gotSym, resolvedWant)
	}
}
