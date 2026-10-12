// Package enginetest provides deterministic local doubles for engine and
// orchestrator tests. It never contacts a provider or starts a real agent CLI.
package enginetest

import (
	"errors"
	"net/http"
	"sync"
)

// Chunk waits for an optional test signal before writing and flushing Data.
// Gates provide deterministic streaming and cancellation tests without sleeps.
type Chunk struct {
	Data string
	Gate <-chan struct{}
}

// Response describes one expected provider request and its scripted response.
type Response struct {
	Status      int
	ContentType string
	Check       func(*http.Request) error
	Chunks      []Chunk
}

// Provider serves an ordered request script through httptest or an HTTP transport.
// Unexpected or invalid requests fail closed and are available through Errors.
type Provider struct {
	mu       sync.Mutex
	script   []Response
	next     int
	failures []error
}

// NewProvider copies a script so tests can safely reuse their fixture definitions.
func NewProvider(script ...Response) *Provider {
	copied := append([]Response(nil), script...)
	for i := range copied {
		copied[i].Chunks = append([]Chunk(nil), copied[i].Chunks...)
	}
	return &Provider{script: copied}
}

// ServeHTTP consumes one response per request, including rejected request checks.
func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.next == len(p.script) {
		p.failures = append(p.failures, errors.New("unexpected provider request"))
		p.mu.Unlock()
		http.Error(w, "unexpected provider request", http.StatusInternalServerError)
		return
	}
	response := p.script[p.next]
	p.next++
	p.mu.Unlock()
	if response.Check != nil {
		if err := response.Check(r); err != nil {
			p.mu.Lock()
			p.failures = append(p.failures, err)
			p.mu.Unlock()
			http.Error(w, "provider request did not match script", http.StatusBadRequest)
			return
		}
	}
	content := response.ContentType
	if content == "" {
		content = "text/event-stream"
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", content)
	w.WriteHeader(status)
	for _, chunk := range response.Chunks {
		if chunk.Gate != nil {
			select {
			case <-chunk.Gate:
			case <-r.Context().Done():
				return
			}
		}
		if r.Context().Err() != nil {
			return
		}
		if _, err := w.Write([]byte(chunk.Data)); err != nil {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
	}
}

// Remaining returns the number of unconsumed expected requests.
func (p *Provider) Remaining() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.script) - p.next }

// Errors returns a snapshot of request mismatches without exposing them over HTTP.
func (p *Provider) Errors() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]error(nil), p.failures...)
}
