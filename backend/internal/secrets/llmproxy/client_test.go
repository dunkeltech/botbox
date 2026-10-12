package llmproxy_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

func TestHandlerClientStreamsBeforeUpstreamCompletes(t *testing.T) {
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-provider-key" {
			t.Error("credential injection missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture-provider-key"}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://proxy/llm/openai/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := llmproxy.ClientForHandler(handler).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	close(finish)
	if err != nil || line != "data: first\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
}

func TestHTTPProxyFlushesBeforeUpstreamCompletes(t *testing.T) {
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	handler, err := llmproxy.NewScopedHandler("workspace", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture-key"}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", proxy.URL+"/llm/openai/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		close(finish)
		t.Fatalf("proxy buffered live headers: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	close(finish)
	if err != nil || line != "data: first\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
}
