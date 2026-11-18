// SPDX-License-Identifier: AGPL-3.0-only

package api

import "testing"

func TestToolResultImagePath(t *testing.T) {
	t.Parallel()
	const good = "/home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/mcp-botbox-desktop-blob-1794831139043-dsdyks.png"
	got, ok := toolResultImagePath("[Image: source: " + good + "]\nbrowser screenshot")
	if !ok || got != good {
		t.Fatalf("path = %q ok=%v", got, ok)
	}
	rejected := []string{
		"",
		"browser screenshot",
		"[Image: source: /home/bot/.ssh/id_rsa]",
		"[Image: source: /home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/../../.ssh/id_rsa.png]",
		"[Image: source: /etc/passwd]",
		"[Image: source: /home/bot/.claude/projects/-home-bot/not-a-uuid/tool-results/shot.png]",
		"[Image: source: /home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/notes.txt]",
	}
	for _, content := range rejected {
		if path, ok := toolResultImagePath(content); ok {
			t.Fatalf("accepted %q as %q", content, path)
		}
	}
}

func TestImageContentType(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0}
	if mime, ok := imageContentType(png); !ok || mime != "image/png" {
		t.Fatalf("png = %q %v", mime, ok)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	if mime, ok := imageContentType(jpeg); !ok || mime != "image/jpeg" {
		t.Fatalf("jpeg = %q %v", mime, ok)
	}
	for _, data := range [][]byte{nil, []byte("not a png"), {0x89, 'P', 'N'}} {
		if _, ok := imageContentType(data); ok {
			t.Fatalf("accepted %q", data)
		}
	}
}
