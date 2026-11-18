// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// MaxCDPMessageBytes caps one DevTools response frame. The WebSocket library
// defaults to 32 KiB, which rejects viewport screenshots and large page reads.
const MaxCDPMessageBytes = 16 << 20

// Browser is the recommended page tool. Pixel clicks stay available for
// everything that is not a web page.
type Browser interface {
	Navigate(ctx context.Context, rawURL string) error
	Click(ctx context.Context, selector string) error
	Fill(ctx context.Context, selector, text string) error
	Read(ctx context.Context, selector string) (string, error)
	Shot(ctx context.Context, selector string) ([]byte, error)
}

// CDPBrowser drives Chromium's localhost DevTools endpoint inside the sandbox.
type CDPBrowser struct {
	BaseURL string
	HTTP    *http.Client
}

// NewCDPBrowser talks to Chromium remote debugging on the sandbox loopback.
func NewCDPBrowser(base string) *CDPBrowser {
	if base == "" {
		base = "http://127.0.0.1:9222"
	}
	return &CDPBrowser{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Navigate opens an http(s) URL. Other schemes are rejected before CDP sees them.
func (b *CDPBrowser) Navigate(ctx context.Context, rawURL string) error {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return fmt.Errorf("%w: navigate url", desk.ErrInvalid)
	}
	_, err := b.call(ctx, "Page.navigate", map[string]any{"url": rawURL})
	return err
}

// Click runs element.click() for a CSS selector.
func (b *CDPBrowser) Click(ctx context.Context, selector string) error {
	_, err := b.eval(ctx, "click", selector, "")
	return err
}

// Fill sets a form control value and fires an input event.
func (b *CDPBrowser) Fill(ctx context.Context, selector, text string) error {
	_, err := b.eval(ctx, "fill", selector, text)
	return err
}

// Read returns the element's text, or the document text when selector is empty.
func (b *CDPBrowser) Read(ctx context.Context, selector string) (string, error) {
	if selector == "" {
		selector = "body"
	}
	return b.eval(ctx, "read", selector, "")
}

// Shot captures the element, or the viewport when selector is empty.
func (b *CDPBrowser) Shot(ctx context.Context, selector string) ([]byte, error) {
	params := map[string]any{"format": "png"}
	if selector != "" {
		box, err := b.eval(ctx, "box", selector, "")
		if err != nil {
			return nil, err
		}
		var rect struct {
			X, Y, Width, Height, Scale float64
		}
		if json.Unmarshal([]byte(box), &rect) != nil || rect.Width <= 0 || rect.Height <= 0 {
			return nil, fmt.Errorf("%w: element box", desk.ErrInvalid)
		}
		params["clip"] = map[string]any{"x": rect.X, "y": rect.Y, "width": rect.Width, "height": rect.Height, "scale": rect.Scale}
	}
	out, err := b.call(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}
	var body struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(out, &body) != nil || body.Data == "" {
		return nil, errors.New("browser screenshot was empty")
	}
	return base64.StdEncoding.DecodeString(body.Data)
}

func (b *CDPBrowser) eval(ctx context.Context, op, selector, text string) (string, error) {
	// Selector and typed text are JSON data. The operation name is a fixed switch
	// inside the page, so neither value is parsed as script source.
	input, err := json.Marshal(map[string]string{"op": op, "selector": selector, "text": text})
	if err != nil {
		return "", err
	}
	script := `(() => {
		const input = ` + string(input) + `;
		const el = document.querySelector(input.selector);
		if (!el) { throw new Error("selector not found"); }
		switch (input.op) {
		case "click": el.click(); return "clicked";
		case "fill": el.focus(); el.value = input.text; el.dispatchEvent(new Event("input", {bubbles:true})); return "filled";
		case "read": return el.innerText || el.value || "";
		case "box": { const r = el.getBoundingClientRect(); return JSON.stringify({x:r.x,y:r.y,width:r.width,height:r.height,scale:window.devicePixelRatio||1}); }
		default: throw new Error("unknown browser op");
		}
	})()`
	raw, err := b.call(ctx, "Runtime.evaluate", map[string]any{"expression": script, "returnByValue": true})
	if err != nil {
		return "", err
	}
	var body struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return "", errors.New("browser evaluation failed")
	}
	if len(body.ExceptionDetails) > 0 && string(body.ExceptionDetails) != "null" {
		return "", fmt.Errorf("%w: selector", desk.ErrInvalid)
	}
	switch v := body.Result.Value.(type) {
	case string:
		return v, nil
	default:
		encoded, err := json.Marshal(v)
		return string(encoded), err
	}
}

func (b *CDPBrowser) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	wsURL, err := b.pageSocket(ctx)
	if err != nil {
		return nil, err
	}
	conn, httpResp, err := websocket.Dial(ctx, wsURL, nil)
	if httpResp != nil && httpResp.Body != nil {
		_ = httpResp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("browser debugging unavailable: %w", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	conn.SetReadLimit(MaxCDPMessageBytes)
	payload, err := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err = conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, err
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil, errors.New("invalid browser response")
	}
	if resp.Error != nil {
		return nil, errors.New("browser command failed")
	}
	return resp.Result, nil
}

func (b *CDPBrowser) pageSocket(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+"/json/list", nil)
	if err != nil {
		return "", err
	}
	client := b.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("browser debugging unavailable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("browser debugging unavailable")
	}
	var pages []struct {
		Type                 string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&pages) != nil {
		return "", errors.New("invalid browser target list")
	}
	for _, page := range pages {
		if page.Type != "page" || page.WebSocketDebuggerURL == "" {
			continue
		}
		parsed, err := url.Parse(page.WebSocketDebuggerURL)
		if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
			continue
		}
		return parsed.String(), nil
	}
	return "", errors.New("browser has no page target")
}
