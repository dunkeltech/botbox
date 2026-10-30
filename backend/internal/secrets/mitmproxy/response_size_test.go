package mitmproxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitPackResponseAboveAPILimit(t *testing.T) {
	// A shallow clone can exceed the API cap even when refs fit below it.
	pack := append([]byte("PACK\x00"), bytes.Repeat([]byte{0x42}, (2<<20)+1)...)
	for _, origin := range []string{"https://gitlab.com", "https://gitlab.example.org"} {
		for _, encoding := range []string{"identity", "gzip"} {
			t.Run(origin+"/"+encoding, func(t *testing.T) {
				adapter, err := ResolveAdapter("gitlab_git", origin)
				if err != nil {
					t.Fatal(err)
				}
				body := pack
				if encoding == "gzip" {
					var compressed bytes.Buffer
					writer := gzip.NewWriter(&compressed)
					if _, err := writer.Write(pack); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					body = compressed.Bytes()
				}
				response := &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type":     {"application/x-git-upload-pack-result"},
						"Content-Encoding": {encoding},
					},
					Body: io.NopCloser(bytes.NewReader(body)),
				}
				recorder := httptest.NewRecorder()
				mediateAndWriteResponse(recorder, response, SecretRule{Value: "synthetic-git-credential"}, &adapter)
				if recorder.Code != http.StatusOK {
					t.Fatalf("clone response: status=%d body=%q", recorder.Code, recorder.Body.String())
				}
				if !bytes.Equal(recorder.Body.Bytes(), pack) {
					t.Fatal("pack response changed during mediation")
				}
			})
		}
	}
}

func TestMediatedResponseSizeFailures(t *testing.T) {
	const secret = "synthetic-git-credential"
	adapter, err := ResolveAdapter("gitlab_git", "https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	// Use a small cap to exercise both wire and decoded limits without large allocations.
	adapter.MaxResponseBytes = 128
	var gzipped, deflated bytes.Buffer
	for _, writer := range []io.WriteCloser{gzip.NewWriter(&gzipped), zlib.NewWriter(&deflated)} {
		if _, err := io.WriteString(writer, strings.Repeat("x", 129)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name       string
		body       io.Reader
		encoding   string
		wantStatus int
		wantError  string
	}{
		{name: "at limit", body: strings.NewReader(strings.Repeat("x", 128)), wantStatus: http.StatusOK},
		{name: "above limit", body: strings.NewReader(strings.Repeat("x", 129)), wantStatus: http.StatusBadGateway, wantError: "upstream response exceeds mediation size limit (128 bytes)"},
		{name: "gzip decoded above limit", body: bytes.NewReader(gzipped.Bytes()), encoding: "gzip", wantStatus: http.StatusBadGateway, wantError: "upstream response exceeds mediation size limit (128 bytes)"},
		{name: "deflate decoded above limit", body: bytes.NewReader(deflated.Bytes()), encoding: "deflate", wantStatus: http.StatusBadGateway, wantError: "upstream response exceeds mediation size limit (128 bytes)"},
		{name: "malformed gzip", body: strings.NewReader("invalid gzip"), encoding: "gzip", wantStatus: http.StatusBadGateway, wantError: "malformed compressed upstream response"},
		{name: "read failure", body: failingResponseReader{err: errors.New("upstream echoed " + secret)}, wantStatus: http.StatusBadGateway, wantError: "failed reading upstream response"},
		{name: "partial read failure", body: io.MultiReader(strings.NewReader("PACK"), failingResponseReader{err: io.ErrUnexpectedEOF}), wantStatus: http.StatusBadGateway, wantError: "failed reading upstream response"},
		{name: "cancelled read", body: failingResponseReader{err: context.Canceled}, wantStatus: http.StatusBadGateway, wantError: "failed reading upstream response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			response := &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":     {"application/x-git-upload-pack-result"},
					"Content-Encoding": {tc.encoding},
				},
				Body: io.NopCloser(tc.body),
			}
			mediateAndWriteResponse(recorder, response, SecretRule{Value: secret}, &adapter)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d", recorder.Code, tc.wantStatus)
			}
			if tc.wantError != "" && recorder.Body.String() != tc.wantError+"\n" {
				t.Fatalf("unexpected error response: %q", recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), secret) {
				t.Fatal("upstream error exposed credential")
			}
		})
	}
}

func TestGitPackResponseStillFailsClosed(t *testing.T) {
	const secret = "synthetic-git-credential"
	adapter, err := ResolveAdapter("gitlab_git", "https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		contentType string
		encoding    string
		location    string
		secretForm  string
		status      int
	}{
		{name: "raw credential", secretForm: secret, status: http.StatusOK},
		{name: "encoded credential", secretForm: base64.StdEncoding.EncodeToString([]byte(secret)), status: http.StatusOK},
		{name: "basic credential", secretForm: base64.StdEncoding.EncodeToString([]byte("oauth2:" + secret)), status: http.StatusInternalServerError},
		{name: "unsupported binary", contentType: "application/octet-stream", status: http.StatusOK},
		{name: "unsupported compression", encoding: "br", status: http.StatusOK},
		{name: "redirect credential", location: "https://gitlab.example.org/" + secret, status: http.StatusFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pack := append([]byte("PACK\x00"), bytes.Repeat([]byte{0x42}, (2<<20)+1)...)
			pack = append(pack, tc.secretForm...)
			contentType := tc.contentType
			if contentType == "" {
				contentType = "application/x-git-upload-pack-result"
			}
			response := &http.Response{
				StatusCode: tc.status,
				Header: http.Header{
					"Content-Type":     {contentType},
					"Content-Encoding": {tc.encoding},
					"Location":         {tc.location},
				},
				Body: io.NopCloser(bytes.NewReader(pack)),
			}
			recorder := httptest.NewRecorder()
			mediateAndWriteResponse(recorder, response, SecretRule{Scheme: SchemeBasic, Username: "oauth2", Value: secret}, &adapter)
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("unsafe response accepted: status=%d", recorder.Code)
			}
			if bytes.Contains(recorder.Body.Bytes(), []byte("PACK")) || containsAnySecretForm(recorder.Body.Bytes(), SecretRule{Value: secret}) {
				t.Fatal("rejected response leaked body bytes")
			}
		})
	}
}

func TestAdapterResponseLimits(t *testing.T) {
	for _, adapter := range ListAdapters() {
		wantLimit := int64(2 << 20)
		if adapter.ID == "gitlab_git" {
			wantLimit = 64 << 20
		}
		if adapter.MaxResponseBytes != wantLimit {
			t.Fatalf("%s response limit=%d want=%d", adapter.ID, adapter.MaxResponseBytes, wantLimit)
		}
	}
	adapter, err := ResolveAdapter("gitlab_api", "https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(strings.Repeat(" ", (2<<20)+1))),
	}
	recorder := httptest.NewRecorder()
	mediateAndWriteResponse(recorder, response, SecretRule{Value: "synthetic-credential"}, &adapter)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "size limit (2097152 bytes)") {
		t.Fatalf("API size limit not enforced: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

type failingResponseReader struct{ err error }

func (r failingResponseReader) Read([]byte) (int, error) { return 0, r.err }
