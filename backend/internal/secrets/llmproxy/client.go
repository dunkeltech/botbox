package llmproxy

import (
	"io"
	"net/http"
	"sync"
)

// ClientForHandler routes host-side model traffic through the same credential and
// budget boundary as sandbox traffic, without exposing an internal HTTP listener.
// Callers must set a request context with a deadline and close response bodies.
func ClientForHandler(handler http.Handler) *http.Client {
	return &http.Client{Transport: handlerTransport{handler}}
}

type handlerTransport struct{ handler http.Handler }
type pipeResponse struct {
	header http.Header
	ready  chan struct{}
	once   sync.Once
	writer *io.PipeWriter
	status int
}

func (w *pipeResponse) Header() http.Header { return w.header }
func (w *pipeResponse) WriteHeader(status int) {
	w.once.Do(func() { w.status = status; close(w.ready) })
}
func (w *pipeResponse) Write(data []byte) (int, error) {
	w.WriteHeader(200)
	return w.writer.Write(data)
}
func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reader, writer := io.Pipe()
	w := &pipeResponse{header: make(http.Header), ready: make(chan struct{}), writer: writer}
	go func() { t.handler.ServeHTTP(w, req); w.WriteHeader(200); _ = writer.Close() }()
	select {
	case <-req.Context().Done():
		_ = reader.CloseWithError(req.Context().Err())
		return nil, req.Context().Err()
	case <-w.ready:
		return &http.Response{StatusCode: w.status, Header: w.header.Clone(), Body: reader, Request: req}, nil
	}
}
