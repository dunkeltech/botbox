// SPDX-License-Identifier: AGPL-3.0-only

package enginetest_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/engine/enginetest"
)

func TestCLIRecordedContracts(t *testing.T) {
	root, err := os.OpenRoot("../testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"claude", "codex", "agy"} {
		t.Run(name, func(t *testing.T) {
			data, err := root.ReadFile(name + "_stream.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			fake := enginetest.CLI{Name: name, Stdout: string(data)}
			var events []engine.Event
			if err = fake.Run(context.Background(), engine.Session{}, func(ev engine.Event) error { events = append(events, ev); return nil }); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []engine.EventKind{engine.EventKindTextDelta, engine.EventKindToolCall, engine.EventKindToolResult, engine.EventKindUsage} {
				found := false
				for _, ev := range events {
					if ev.Kind == kind {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing %s", kind)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (enginetest.CLI{Gate: make(chan struct{})}).Run(ctx, engine.Session{}, func(engine.Event) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, fake := range []enginetest.CLI{{Name: "invalid"}, {Name: "claude", Stdout: "broken"}, {Name: "claude", ExitCode: 1}} {
		if err := fake.Run(context.Background(), engine.Session{}, func(engine.Event) error { return nil }); err == nil {
			t.Fatal("expected failure")
		}
	}
}

func TestProviderScriptStreamingAndRejection(t *testing.T) {
	gate := make(chan struct{})
	fake := enginetest.NewProvider(enginetest.Response{Chunks: []enginetest.Chunk{{Data: "data: first\n\n"}, {Data: "data: [DONE]\n\n", Gate: gate}}}, enginetest.Response{Check: func(*http.Request) error { return errors.New("private mismatch details") }})
	server := httptest.NewServer(fake)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	close(gate)
	rest, err := io.ReadAll(reader)
	_ = resp.Body.Close()
	if err != nil || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("rest=%q err=%v", rest, err)
	}
	for _, want := range []int{400, 500} {
		r, err := server.Client().Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.StatusCode != want || strings.Contains(string(body), "private mismatch") {
			t.Fatalf("status=%d body=%s", r.StatusCode, body)
		}
	}
	if fake.Remaining() != 0 || len(fake.Errors()) != 2 {
		t.Fatalf("remaining=%d errors=%v", fake.Remaining(), fake.Errors())
	}
}
