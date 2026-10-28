package authbroker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

var (
	// ErrSessionNotFound indicates the broker session does not exist or has expired.
	ErrSessionNotFound = errors.New("broker session not found")
	// ErrSessionExpired indicates the broker session exceeded its TTL.
	ErrSessionExpired = errors.New("broker session expired")
	// ErrSessionAlreadyUsed indicates credential submission was attempted twice on one session.
	ErrSessionAlreadyUsed = errors.New("broker session credentials already submitted")
	// ErrBindingMismatch indicates the live page, frame, document, origin, or form changed before fill.
	ErrBindingMismatch = errors.New("broker page binding mismatch")
	// ErrNavigationForbidden indicates a bot or redirect attempted to leave adapter-approved HTTPS origins.
	ErrNavigationForbidden = errors.New("broker navigation outside adapter origins")
	// ErrActionForbidden indicates a blocked selector or sensitive read/inspect attempt.
	ErrActionForbidden = errors.New("broker action forbidden by policy")
	// ErrTakeoverActive indicates bot operations are suspended while a human controls the broker.
	ErrTakeoverActive = errors.New("broker human takeover active")
	// ErrCaptureSuppressed indicates screenshot capture is disabled during sensitive or takeover phases.
	ErrCaptureSuppressed = errors.New("broker capture suppressed during sensitive phase")
	// ErrTakeoverForbidden indicates another user attempted to access a user-pinned takeover session.
	ErrTakeoverForbidden = errors.New("broker takeover belongs to another user")
	// ErrTakeoverNotActive indicates takeover completion was called without an active takeover.
	ErrTakeoverNotActive = errors.New("broker takeover is not active")
	// ErrAdapterNotFound indicates an unknown or unreviewed browser login adapter ID.
	ErrAdapterNotFound = errors.New("reviewed browser login adapter not found")
)

// TargetBinding captures the atomic browser target/frame/document identity for a broker session.
type TargetBinding struct {
	TargetID   string `json:"target_id"`
	FrameID    string `json:"frame_id"`
	DocumentID string `json:"document_id"`
	Origin     string `json:"origin"`
	PageURL    string `json:"page_url"`
	FormAction string `json:"form_action"`
}

// FillOutcome describes the non-secret state after an atomic credential submission.
type FillOutcome struct {
	Binding      TargetBinding `json:"binding"`
	Submitted    bool          `json:"submitted"`
	RequiresMFA  bool          `json:"requires_mfa"`
	LandedOrigin string        `json:"landed_origin"`
}

// ReadResult holds sanitized, secret-redacted element text from the broker viewport.
type ReadResult struct {
	Selector string        `json:"selector"`
	Text     string        `json:"text"`
	Binding  TargetBinding `json:"binding"`
}

// ClickResult describes the non-secret page state after clicking a permitted element.
type ClickResult struct {
	Selector string        `json:"selector"`
	Binding  TargetBinding `json:"binding"`
}

// TakeoverInput represents a human UI event forwarded only to the isolated broker viewport.
type TakeoverInput struct {
	Kind string  `json:"kind"` // "click", "type", "key"
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	Text string  `json:"text,omitempty"`
	Key  string  `json:"key,omitempty"`
}

// BrowserEngine isolates low-level CDP/browser operations behind explicit security boundaries.
type BrowserEngine interface {
	OpenTarget(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, adapter LoginAdapter) (TargetBinding, error)
	AtomicBindAndFill(ctx context.Context, sessionID uuid.UUID, expected TargetBinding, adapter LoginAdapter, username, secret string) (FillOutcome, error)
	Navigate(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter, rawURL string) (TargetBinding, error)
	ReadSanitized(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter, selector string) (ReadResult, error)
	ClickElement(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter, selector string) (ClickResult, error)
	CaptureSanitized(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter) ([]byte, TargetBinding, error)
	CaptureTakeoverFrame(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter) ([]byte, TargetBinding, error)
	SendTakeoverInput(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter, input TakeoverInput) (TargetBinding, error)
	InspectBinding(ctx context.Context, sessionID uuid.UUID) (TargetBinding, error)
	CloseSession(ctx context.Context, sessionID uuid.UUID) error
}

// ValidateBotSelector rejects selectors that target password fields or attempt script injection.
func ValidateBotSelector(selector string) (string, error) {
	trimmed := strings.TrimSpace(selector)
	if trimmed == "" || len(trimmed) > 256 {
		return "", fmt.Errorf("%w: invalid selector length", ErrActionForbidden)
	}
	lower := strings.ToLower(trimmed)
	blocked := []string{
		"password",
		"type=\"hidden\"",
		"type='hidden'",
		"cookie",
		"localstorage",
		"sessionstorage",
		"<script",
		"javascript:",
		"\n",
		"\r",
	}
	for _, token := range blocked {
		if strings.Contains(lower, token) {
			return "", fmt.Errorf("%w: selector targets forbidden credential or storage surface", ErrActionForbidden)
		}
	}
	return trimmed, nil
}

// tinyValidPNG is a minimal 1x1 PNG used by the deterministic in-memory engine for image assertions.
var tinyValidPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

type memoryTab struct {
	TargetID      string
	FrameID       string
	DocumentID    string
	Origin        string
	PageURL       string
	FormAction    string
	Elements      map[string]string
	Username      string
	Password      string
	PasswordRead  bool
	Submitted     bool
	RequiresMFA   bool
	Closed        bool
	ClickHistory  []string
	TakeoverLog   []TakeoverInput
	PostLoginURL  string
	PostLoginText string
}

type memorySession struct {
	WorkspaceID uuid.UUID
	BotID       uuid.UUID
	SessionID   uuid.UUID
	Adapter     LoginAdapter
	ActiveTabID string
	Tabs        map[string]*memoryTab
	FillCount   int
}

// MemoryBrowserEngine is an in-memory deterministic BrowserEngine with concurrency
// hooks for race-barrier and page-binding boundary tests.
type MemoryBrowserEngine struct {
	mu           sync.Mutex
	sessions     map[uuid.UUID]*memorySession
	seq          atomic.Uint64
	OnBeforeFill func(sessionID uuid.UUID)
}

// NewMemoryBrowserEngine constructs a MemoryBrowserEngine for unit and integration tests.
func NewMemoryBrowserEngine() *MemoryBrowserEngine {
	return &MemoryBrowserEngine{
		sessions: make(map[uuid.UUID]*memorySession),
	}
}

func (m *MemoryBrowserEngine) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, m.seq.Add(1))
}

// OpenTarget creates an isolated browser context and binds the initial login page.
func (m *MemoryBrowserEngine) OpenTarget(_ context.Context, workspaceID, botID, sessionID uuid.UUID, adapter LoginAdapter) (TargetBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[sessionID]; ok {
		tab := sess.Tabs[sess.ActiveTabID]
		if tab == nil || tab.Closed {
			return TargetBinding{}, ErrBindingMismatch
		}
		return tab.binding(), nil
	}

	targetID := m.nextID("target")
	tab := &memoryTab{
		TargetID:   targetID,
		FrameID:    m.nextID("frame"),
		DocumentID: m.nextID("doc"),
		Origin:     adapter.Origin,
		PageURL:    adapter.LoginURL(),
		FormAction: adapter.FormActionOrigin + adapter.FormActionPath,
		Elements: map[string]string{
			"body":                 adapter.Label + " Sign in",
			"main":                 adapter.Label + " Sign in",
			".status":              "awaiting credentials",
			adapter.SubmitSelector: "Sign in",
		},
		PostLoginURL:  adapter.Origin + "/",
		PostLoginText: "Signed in to " + adapter.Label,
	}
	m.sessions[sessionID] = &memorySession{
		WorkspaceID: workspaceID,
		BotID:       botID,
		SessionID:   sessionID,
		Adapter:     adapter,
		ActiveTabID: targetID,
		Tabs:        map[string]*memoryTab{targetID: tab},
	}
	return tab.binding(), nil
}

func (t *memoryTab) binding() TargetBinding {
	return TargetBinding{
		TargetID:   t.TargetID,
		FrameID:    t.FrameID,
		DocumentID: t.DocumentID,
		Origin:     t.Origin,
		PageURL:    t.PageURL,
		FormAction: t.FormAction,
	}
}

func (m *MemoryBrowserEngine) activeTabLocked(sessionID uuid.UUID) (*memorySession, *memoryTab, error) {
	sess, ok := m.sessions[sessionID]
	if !ok {
		return nil, nil, ErrSessionNotFound
	}
	tab := sess.Tabs[sess.ActiveTabID]
	if tab == nil || tab.Closed {
		return nil, nil, ErrBindingMismatch
	}
	return sess, tab, nil
}

func verifyBindingMatch(expected, actual TargetBinding, adapter LoginAdapter) error {
	if expected.TargetID == "" || expected.FrameID == "" || expected.DocumentID == "" {
		return ErrBindingMismatch
	}
	if expected.TargetID != actual.TargetID || expected.FrameID != actual.FrameID || expected.DocumentID != actual.DocumentID {
		return ErrBindingMismatch
	}
	if expected.Origin != actual.Origin || actual.Origin != adapter.Origin {
		return ErrBindingMismatch
	}
	if !adapter.MatchesFormDestination(actual.PageURL, actual.FormAction) {
		return ErrBindingMismatch
	}
	return nil
}

// AtomicBindAndFill validates the exact target/frame/document/origin/form binding
// both before and after the concurrency test hook before touching credentials.
func (m *MemoryBrowserEngine) AtomicBindAndFill(_ context.Context, sessionID uuid.UUID, expected TargetBinding, adapter LoginAdapter, username, secret string) (FillOutcome, error) {
	m.mu.Lock()
	sess, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		m.mu.Unlock()
		return FillOutcome{}, err
	}
	if sess.FillCount > 0 {
		m.mu.Unlock()
		return FillOutcome{}, ErrSessionAlreadyUsed
	}
	if err := verifyBindingMatch(expected, tab.binding(), adapter); err != nil {
		m.mu.Unlock()
		return FillOutcome{}, err
	}
	hook := m.OnBeforeFill
	m.mu.Unlock()

	if hook != nil {
		hook(sessionID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	sess, tab, err = m.activeTabLocked(sessionID)
	if err != nil {
		return FillOutcome{}, err
	}
	if sess.FillCount > 0 {
		return FillOutcome{}, ErrSessionAlreadyUsed
	}
	if err := verifyBindingMatch(expected, tab.binding(), adapter); err != nil {
		return FillOutcome{}, err
	}

	// Store only in the isolated broker tab state; never expose back through reads.
	tab.Username = username
	tab.Password = secret
	tab.Submitted = true
	sess.FillCount++

	if !tab.RequiresMFA {
		tab.DocumentID = m.nextID("doc")
		tab.PageURL = tab.PostLoginURL
		tab.FormAction = ""
		tab.Elements["body"] = tab.PostLoginText
		tab.Elements["main"] = tab.PostLoginText
		tab.Elements[".status"] = "authenticated"
	} else {
		tab.DocumentID = m.nextID("doc")
		tab.FormAction = ""
		tab.Elements["body"] = "Two-factor verification required"
		tab.Elements["main"] = "Two-factor verification required"
		tab.Elements[".status"] = "mfa_required"
	}

	return FillOutcome{
		Binding:      tab.binding(),
		Submitted:    true,
		RequiresMFA:  tab.RequiresMFA,
		LandedOrigin: tab.Origin,
	}, nil
}

// Navigate moves the active broker tab only to adapter-permitted HTTPS origins.
func (m *MemoryBrowserEngine) Navigate(_ context.Context, sessionID uuid.UUID, adapter LoginAdapter, rawURL string) (TargetBinding, error) {
	canon, err := adapter.PermitsNavigation(rawURL)
	if err != nil {
		return TargetBinding{}, fmt.Errorf("%w: %v", ErrNavigationForbidden, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return TargetBinding{}, err
	}
	tab.Origin = canon
	tab.PageURL = strings.TrimSpace(rawURL)
	tab.DocumentID = m.nextID("doc")
	return tab.binding(), nil
}

// ReadSanitized returns redacted visible text for safe non-credential selectors only.
func (m *MemoryBrowserEngine) ReadSanitized(_ context.Context, sessionID uuid.UUID, _ LoginAdapter, selector string) (ReadResult, error) {
	cleanSelector, err := ValidateBotSelector(selector)
	if err != nil {
		return ReadResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return ReadResult{}, err
	}
	text := tab.Elements[cleanSelector]
	return ReadResult{
		Selector: cleanSelector,
		Text:     SanitizeOutput(text, tab.Password),
		Binding:  tab.binding(),
	}, nil
}

// ClickElement clicks a non-credential selector within the isolated broker tab.
func (m *MemoryBrowserEngine) ClickElement(_ context.Context, sessionID uuid.UUID, _ LoginAdapter, selector string) (ClickResult, error) {
	cleanSelector, err := ValidateBotSelector(selector)
	if err != nil {
		return ClickResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return ClickResult{}, err
	}
	tab.ClickHistory = append(tab.ClickHistory, cleanSelector)
	return ClickResult{
		Selector: cleanSelector,
		Binding:  tab.binding(),
	}, nil
}

// CaptureSanitized returns a clean screenshot once sensitive entry is finished.
func (m *MemoryBrowserEngine) CaptureSanitized(_ context.Context, sessionID uuid.UUID, _ LoginAdapter) ([]byte, TargetBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	return slices.Clone(tinyValidPNG), tab.binding(), nil
}

// CaptureTakeoverFrame captures a frame specifically for the authorized human takeover UI.
func (m *MemoryBrowserEngine) CaptureTakeoverFrame(_ context.Context, sessionID uuid.UUID, _ LoginAdapter) ([]byte, TargetBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	return slices.Clone(tinyValidPNG), tab.binding(), nil
}

// SendTakeoverInput applies a human takeover UI action inside the isolated broker tab.
func (m *MemoryBrowserEngine) SendTakeoverInput(_ context.Context, sessionID uuid.UUID, adapter LoginAdapter, input TakeoverInput) (TargetBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return TargetBinding{}, err
	}
	tab.TakeoverLog = append(tab.TakeoverLog, input)
	if input.Kind == "key" && strings.EqualFold(input.Key, "Enter") && tab.RequiresMFA {
		tab.RequiresMFA = false
		tab.DocumentID = m.nextID("doc")
		tab.PageURL = adapter.Origin + "/dashboard"
		tab.Elements["body"] = tab.PostLoginText
		tab.Elements[".status"] = "authenticated"
	}
	return tab.binding(), nil
}

// InspectBinding returns the current active tab binding in the broker.
func (m *MemoryBrowserEngine) InspectBinding(_ context.Context, sessionID uuid.UUID) (TargetBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab, err := m.activeTabLocked(sessionID)
	if err != nil {
		return TargetBinding{}, err
	}
	return tab.binding(), nil
}

// CloseSession drops the broker session and zeroes stored credentials.
func (m *MemoryBrowserEngine) CloseSession(_ context.Context, sessionID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		for _, tab := range sess.Tabs {
			tab.Password = ""
			tab.Username = ""
			tab.Closed = true
		}
		delete(m.sessions, sessionID)
	}
	return nil
}

// Test helpers on MemoryBrowserEngine for adversarial race and boundary testing:

// SwitchActiveTab opens a second tab and switches focus to it to test tab-switch races.
func (m *MemoryBrowserEngine) SwitchActiveTab(sessionID uuid.UUID, newOrigin, newURL string) TargetBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[sessionID]
	targetID := m.nextID("target")
	tab := &memoryTab{
		TargetID:   targetID,
		FrameID:    m.nextID("frame"),
		DocumentID: m.nextID("doc"),
		Origin:     newOrigin,
		PageURL:    newURL,
		FormAction: newURL,
		Elements:   map[string]string{"body": "other tab"},
	}
	sess.Tabs[targetID] = tab
	sess.ActiveTabID = targetID
	return tab.binding()
}

// ReplaceFrame swaps the active frame ID on the active tab.
func (m *MemoryBrowserEngine) ReplaceFrame(sessionID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if tab := sess.Tabs[sess.ActiveTabID]; tab != nil {
			tab.FrameID = m.nextID("frame")
		}
	}
}

// ReplaceDocument replaces the active document ID without changing origin (same-origin nav).
func (m *MemoryBrowserEngine) ReplaceDocument(sessionID uuid.UUID, newURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if tab := sess.Tabs[sess.ActiveTabID]; tab != nil {
			tab.DocumentID = m.nextID("doc")
			if newURL != "" {
				tab.PageURL = newURL
			}
		}
	}
}

// RedirectTab moves the active tab to a new origin/URL and document.
func (m *MemoryBrowserEngine) RedirectTab(sessionID uuid.UUID, origin, pageURL, formAction string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if tab := sess.Tabs[sess.ActiveTabID]; tab != nil {
			tab.Origin = origin
			tab.PageURL = pageURL
			tab.FormAction = formAction
			tab.DocumentID = m.nextID("doc")
		}
	}
}

// CloseActiveTab marks the active target closed mid-flight.
func (m *MemoryBrowserEngine) CloseActiveTab(sessionID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if tab := sess.Tabs[sess.ActiveTabID]; tab != nil {
			tab.Closed = true
		}
	}
}

// SetMFARequired configures whether credential submission triggers an MFA step in tests.
func (m *MemoryBrowserEngine) SetMFARequired(sessionID uuid.UUID, required bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if tab := sess.Tabs[sess.ActiveTabID]; tab != nil {
			tab.RequiresMFA = required
		}
	}
}

// StoredPasswordEmpty reports whether no password is stored in the test session tabs
// (used only to verify whether an aborted fill prevented credential injection).
func (m *MemoryBrowserEngine) StoredPasswordEmpty(sessionID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return true
	}
	for _, tab := range sess.Tabs {
		if tab.Password != "" {
			return false
		}
	}
	return true
}

// CDPBrowsingEngine talks exclusively to the isolated broker Chromium instance over CDP
// (never to any bot sandbox container).
type CDPBrowsingEngine struct {
	debugHTTPURL string
	httpClient   *http.Client

	mu       sync.Mutex
	sessions map[uuid.UUID]*cdpSessionState
}

type cdpSessionState struct {
	WorkspaceID      uuid.UUID
	BotID            uuid.UUID
	BrowserContextID string
	TargetID         string
	CDPSessionID     string
	DocumentNonce    string
	PublicProxy      *publicOriginProxy
	FillCount        int
}

// NewCDPBrowsingEngine constructs a BrowserEngine pointing at an isolated Chromium DevTools
// endpoint inside the auth-broker container (e.g. http://127.0.0.1:9223).
func NewCDPBrowsingEngine(debugHTTPURL string) *CDPBrowsingEngine {
	return &CDPBrowsingEngine{
		debugHTTPURL: strings.TrimRight(debugHTTPURL, "/"),
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		sessions:     make(map[uuid.UUID]*cdpSessionState),
	}
}

type cdpConn struct {
	ws              *websocket.Conn
	seq             atomic.Int64
	targetSessionID string
}

func (e *CDPBrowsingEngine) dialBrowser(ctx context.Context) (*cdpConn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.debugHTTPURL+"/json/version", nil) //nolint:gosec // configured loopback Chromium DevTools URL
	if err != nil {
		return nil, err
	}
	resp, err := e.httpClient.Do(req) //nolint:gosec // configured loopback Chromium DevTools URL
	if err != nil {
		return nil, fmt.Errorf("cdp /json/version: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close after read
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.WebSocketDebuggerURL == "" {
		return nil, errors.New("cdp /json/version missing webSocketDebuggerUrl")
	}
	ws, wsResp, err := websocket.Dial(ctx, info.WebSocketDebuggerURL, nil)
	if wsResp != nil && wsResp.Body != nil {
		_ = wsResp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("cdp dial: %w", err)
	}
	ws.SetReadLimit(16 << 20)
	return &cdpConn{ws: ws}, nil
}

// Flattened target sessions belong to one websocket. Reattach on each broker operation.
func (e *CDPBrowsingEngine) dialTarget(ctx context.Context, sess *cdpSessionState) (*cdpConn, error) {
	c, err := e.dialBrowser(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": sess.TargetID, "flatten": true})
	if err != nil {
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err = json.Unmarshal(raw, &attached); err != nil || attached.SessionID == "" {
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
		return nil, ErrBindingMismatch
	}
	c.targetSessionID = attached.SessionID
	return c, nil
}

func (c *cdpConn) call(ctx context.Context, cdpSessionID, method string, params any) (json.RawMessage, error) {
	id := c.seq.Add(1)
	payload := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		payload["params"] = params
	}
	if cdpSessionID != "" {
		payload["sessionId"] = cdpSessionID
		if c.targetSessionID != "" {
			payload["sessionId"] = c.targetSessionID
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, raw); err != nil {
		return nil, err
	}
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		var msg struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return nil, errors.New(msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func randomHexToken(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (e *CDPBrowsingEngine) getSession(sessionID uuid.UUID) (*cdpSessionState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

func (e *CDPBrowsingEngine) inspectLive(ctx context.Context, c *cdpConn, sess *cdpSessionState) (TargetBinding, error) {
	treeRaw, err := c.call(ctx, sess.CDPSessionID, "Page.getFrameTree", nil)
	if err != nil {
		return TargetBinding{}, fmt.Errorf("%w: %v", ErrBindingMismatch, err)
	}
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID       string `json:"id"`
				LoaderID string `json:"loaderId"`
				URL      string `json:"url"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := json.Unmarshal(treeRaw, &tree); err != nil {
		return TargetBinding{}, ErrBindingMismatch
	}
	f := tree.FrameTree.Frame
	u, err := url.Parse(f.URL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return TargetBinding{}, ErrBindingMismatch
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	origin := fmt.Sprintf("https://%s:%s", strings.ToLower(u.Hostname()), port)

	// Extract only non-secret form action + page document token via strict read-only helper.
	evalRaw, err := c.call(ctx, sess.CDPSessionID, "Runtime.evaluate", map[string]any{
		"expression": `(() => {
			if (!window.__botboxDocToken) { window.__botboxDocToken = crypto.randomUUID(); }
			const pw = document.querySelector("input[type='password']");
			const form = pw ? pw.closest("form") : document.querySelector("form");
			return JSON.stringify({
				token: window.__botboxDocToken,
				formAction: form ? (form.getAttribute("action") || location.href) : ""
			});
		})()`,
		"returnByValue": true,
	})
	if err != nil {
		return TargetBinding{}, ErrBindingMismatch
	}
	var evalRes struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(evalRaw, &evalRes); err != nil {
		return TargetBinding{}, ErrBindingMismatch
	}
	var meta struct {
		Token      string `json:"token"`
		FormAction string `json:"formAction"`
	}
	if err := json.Unmarshal([]byte(evalRes.Result.Value), &meta); err != nil {
		return TargetBinding{}, ErrBindingMismatch
	}

	return TargetBinding{
		TargetID:   sess.TargetID,
		FrameID:    f.ID,
		DocumentID: f.LoaderID + ":" + meta.Token,
		Origin:     origin,
		PageURL:    f.URL,
		FormAction: meta.FormAction,
	}, nil
}

// OpenTarget spawns a fresh incognito browser context and target pointed at adapter.LoginURL().
func (e *CDPBrowsingEngine) OpenTarget(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, adapter LoginAdapter) (TargetBinding, error) {
	c, err := e.dialBrowser(ctx)
	if err != nil {
		return TargetBinding{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup

	if existing, err := e.getSession(sessionID); err == nil {
		return e.inspectLive(ctx, c, existing)
	}

	// Contexts outlive individual CDP calls; CloseSession owns their disposal.
	contextParams := map[string]any{"disposeOnDetach": false}
	var publicProxy *publicOriginProxy
	committed := false
	if isCustomGitLab(adapter) {
		publicProxy, err = newPublicOriginProxy(adapter.Origin)
		if err != nil {
			return TargetBinding{}, err
		}
		defer func() {
			if !committed {
				publicProxy.close()
			}
		}()
		contextParams["proxyServer"] = publicProxy.url
		// Chromium otherwise implicitly bypasses proxies for loopback URLs.
		contextParams["proxyBypassList"] = "<-loopback>"
	}
	ctxRaw, err := c.call(ctx, "", "Target.createBrowserContext", contextParams)
	if err != nil {
		return TargetBinding{}, err
	}
	var ctxRes struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := json.Unmarshal(ctxRaw, &ctxRes); err != nil {
		return TargetBinding{}, err
	}

	defer func() {
		if !committed {
			cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, _ = c.call(cleanCtx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": ctxRes.BrowserContextID})
		}
	}()

	tRaw, err := c.call(ctx, "", "Target.createTarget", map[string]any{
		"url":              adapter.LoginURL(),
		"browserContextId": ctxRes.BrowserContextID,
	})
	if err != nil {
		return TargetBinding{}, err
	}
	var tRes struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(tRaw, &tRes); err != nil {
		return TargetBinding{}, err
	}

	aRaw, err := c.call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": tRes.TargetID,
		"flatten":  true,
	})
	if err != nil {
		return TargetBinding{}, err
	}
	var aRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(aRaw, &aRes); err != nil {
		return TargetBinding{}, err
	}

	_, _ = c.call(ctx, aRes.SessionID, "Page.enable", nil)
	_, _ = c.call(ctx, aRes.SessionID, "DOM.enable", nil)

	nonce, err := randomHexToken(12)
	if err != nil {
		return TargetBinding{}, err
	}
	sess := &cdpSessionState{
		WorkspaceID:      workspaceID,
		BotID:            botID,
		BrowserContextID: ctxRes.BrowserContextID,
		TargetID:         tRes.TargetID,
		CDPSessionID:     aRes.SessionID,
		DocumentNonce:    nonce,
		PublicProxy:      publicProxy,
	}
	e.mu.Lock()
	e.sessions[sessionID] = sess
	e.mu.Unlock()

	binding, err := e.inspectLive(ctx, c, sess)
	if err != nil {
		e.mu.Lock()
		delete(e.sessions, sessionID)
		e.mu.Unlock()
		return TargetBinding{}, err
	}
	committed = true
	return binding, nil
}

// AtomicBindAndFill validates binding, injects credentials via DOM.focus + Input.insertText
// (never placing the password inside evaluated JavaScript), and submits once.
func (e *CDPBrowsingEngine) AtomicBindAndFill(ctx context.Context, sessionID uuid.UUID, expected TargetBinding, adapter LoginAdapter, username, secret string) (FillOutcome, error) {
	sess, err := e.getSession(sessionID)
	if err != nil {
		return FillOutcome{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return FillOutcome{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup

	e.mu.Lock()
	if sess.FillCount > 0 {
		e.mu.Unlock()
		return FillOutcome{}, ErrSessionAlreadyUsed
	}
	e.mu.Unlock()

	live, err := e.inspectLive(ctx, c, sess)
	if err != nil {
		return FillOutcome{}, err
	}
	if err := verifyBindingMatch(expected, live, adapter); err != nil {
		return FillOutcome{}, err
	}

	docRaw, err := c.call(ctx, sess.CDPSessionID, "DOM.getDocument", map[string]any{"depth": 1})
	if err != nil {
		return FillOutcome{}, ErrBindingMismatch
	}
	var docRes struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(docRaw, &docRes); err != nil || docRes.Root.NodeID == 0 {
		return FillOutcome{}, ErrBindingMismatch
	}

	focusAndInsert := func(selector, val string) error {
		qRaw, qErr := c.call(ctx, sess.CDPSessionID, "DOM.querySelector", map[string]any{
			"nodeId":   docRes.Root.NodeID,
			"selector": selector,
		})
		if qErr != nil {
			return ErrBindingMismatch
		}
		var qRes struct {
			NodeID int `json:"nodeId"`
		}
		if uErr := json.Unmarshal(qRaw, &qRes); uErr != nil || qRes.NodeID == 0 {
			return ErrBindingMismatch
		}
		if _, fErr := c.call(ctx, sess.CDPSessionID, "DOM.focus", map[string]any{"nodeId": qRes.NodeID}); fErr != nil {
			return ErrBindingMismatch
		}
		_, iErr := c.call(ctx, sess.CDPSessionID, "Input.insertText", map[string]any{"text": val})
		return iErr
	}

	if username != "" && adapter.UsernameSelector != "" {
		if err := focusAndInsert(adapter.UsernameSelector, username); err != nil {
			return FillOutcome{}, err
		}
	}

	// Re-verify binding right before sensitive password injection.
	liveBeforePw, err := e.inspectLive(ctx, c, sess)
	if err != nil || verifyBindingMatch(expected, liveBeforePw, adapter) != nil {
		return FillOutcome{}, ErrBindingMismatch
	}
	if err := focusAndInsert(adapter.PasswordSelector, secret); err != nil {
		return FillOutcome{}, err
	}

	// Re-verify before submitting.
	liveBeforeSubmit, err := e.inspectLive(ctx, c, sess)
	if err != nil || verifyBindingMatch(expected, liveBeforeSubmit, adapter) != nil {
		return FillOutcome{}, ErrBindingMismatch
	}
	qRaw, err := c.call(ctx, sess.CDPSessionID, "DOM.querySelector", map[string]any{
		"nodeId":   docRes.Root.NodeID,
		"selector": adapter.SubmitSelector,
	})
	if err == nil {
		var qRes struct {
			NodeID int `json:"nodeId"`
		}
		if json.Unmarshal(qRaw, &qRes) == nil && qRes.NodeID != 0 {
			_, _ = c.call(ctx, sess.CDPSessionID, "DOM.focus", map[string]any{"nodeId": qRes.NodeID})
			_, _ = c.call(ctx, sess.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{
				"type": "keyDown",
				"key":  "Enter",
			})
			_, _ = c.call(ctx, sess.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{
				"type": "keyUp",
				"key":  "Enter",
			})
		}
	}

	e.mu.Lock()
	sess.FillCount++
	e.mu.Unlock()

	after, _ := e.inspectLive(ctx, c, sess)
	if after.Origin == "" {
		after = liveBeforeSubmit
	}
	return FillOutcome{
		Binding:      after,
		Submitted:    true,
		RequiresMFA:  after.Origin == adapter.Origin && after.PageURL == liveBeforeSubmit.PageURL,
		LandedOrigin: after.Origin,
	}, nil
}

// Navigate moves the isolated broker target only to URLs allowed by the LoginAdapter.
func (e *CDPBrowsingEngine) Navigate(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter, rawURL string) (TargetBinding, error) {
	if _, err := adapter.PermitsNavigation(rawURL); err != nil {
		return TargetBinding{}, fmt.Errorf("%w: %v", ErrNavigationForbidden, err)
	}
	sess, err := e.getSession(sessionID)
	if err != nil {
		return TargetBinding{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return TargetBinding{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup
	if _, err := c.call(ctx, sess.CDPSessionID, "Page.navigate", map[string]any{"url": rawURL}); err != nil {
		return TargetBinding{}, err
	}
	return e.inspectLive(ctx, c, sess)
}

// ReadSanitized extracts redacted visible text for safe non-credential selectors only.
func (e *CDPBrowsingEngine) ReadSanitized(ctx context.Context, sessionID uuid.UUID, adapter LoginAdapter, selector string) (ReadResult, error) {
	cleanSelector, err := ValidateBotSelector(selector)
	if err != nil {
		return ReadResult{}, err
	}
	sess, err := e.getSession(sessionID)
	if err != nil {
		return ReadResult{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return ReadResult{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup

	live, err := e.inspectLive(ctx, c, sess)
	if err != nil {
		return ReadResult{}, err
	}
	if _, err := adapter.PermitsNavigation(live.PageURL); err != nil {
		return ReadResult{}, ErrNavigationForbidden
	}

	selJSON, _ := json.Marshal(cleanSelector)
	expr := fmt.Sprintf(`(() => {
		const el = document.querySelector(%s);
		if (!el) return "";
		const tag = (el.tagName || "").toLowerCase();
		if (tag === "input" && ((el.type || "").toLowerCase() === "password" || (el.type || "").toLowerCase() === "hidden")) {
			return "[BLOCKED_CREDENTIAL_INPUT]";
		}
		return (el.innerText || el.textContent || "").slice(0, 4096);
	})()`, string(selJSON))
	resRaw, err := c.call(ctx, sess.CDPSessionID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	})
	if err != nil {
		return ReadResult{}, err
	}
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resRaw, &res); err != nil {
		return ReadResult{}, err
	}
	if res.Result.Value == "[BLOCKED_CREDENTIAL_INPUT]" {
		return ReadResult{}, ErrActionForbidden
	}
	return ReadResult{
		Selector: cleanSelector,
		Text:     SanitizeOutput(res.Result.Value),
		Binding:  live,
	}, nil
}

// ClickElement clicks a safe selector inside the broker viewport.
func (e *CDPBrowsingEngine) ClickElement(ctx context.Context, sessionID uuid.UUID, _ LoginAdapter, selector string) (ClickResult, error) {
	cleanSelector, err := ValidateBotSelector(selector)
	if err != nil {
		return ClickResult{}, err
	}
	sess, err := e.getSession(sessionID)
	if err != nil {
		return ClickResult{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return ClickResult{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup

	selJSON, _ := json.Marshal(cleanSelector)
	_, err = c.call(ctx, sess.CDPSessionID, "Runtime.evaluate", map[string]any{
		"expression": fmt.Sprintf(`(() => {
			const el = document.querySelector(%s);
			if (el) el.click();
		})()`, string(selJSON)),
	})
	if err != nil {
		return ClickResult{}, err
	}
	live, err := e.inspectLive(ctx, c, sess)
	if err != nil {
		return ClickResult{}, err
	}
	return ClickResult{Selector: cleanSelector, Binding: live}, nil
}

func (e *CDPBrowsingEngine) capturePNG(ctx context.Context, sessionID uuid.UUID) ([]byte, TargetBinding, error) {
	sess, err := e.getSession(sessionID)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup
	live, err := e.inspectLive(ctx, c, sess)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	shotRaw, err := c.call(ctx, sess.CDPSessionID, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return nil, TargetBinding{}, err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(shotRaw, &shot); err != nil {
		return nil, TargetBinding{}, err
	}
	pngBytes, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	return pngBytes, live, nil
}

// CaptureSanitized captures a screenshot outside sensitive/takeover states.
func (e *CDPBrowsingEngine) CaptureSanitized(ctx context.Context, sessionID uuid.UUID, _ LoginAdapter) ([]byte, TargetBinding, error) {
	return e.capturePNG(ctx, sessionID)
}

// CaptureTakeoverFrame captures a live broker viewport frame for the authorized human takeover UI.
func (e *CDPBrowsingEngine) CaptureTakeoverFrame(ctx context.Context, sessionID uuid.UUID, _ LoginAdapter) ([]byte, TargetBinding, error) {
	return e.capturePNG(ctx, sessionID)
}

// SendTakeoverInput dispatches mouse/key events from the authorized human into the isolated broker viewport.
func (e *CDPBrowsingEngine) SendTakeoverInput(ctx context.Context, sessionID uuid.UUID, _ LoginAdapter, input TakeoverInput) (TargetBinding, error) {
	sess, err := e.getSession(sessionID)
	if err != nil {
		return TargetBinding{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return TargetBinding{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup

	switch input.Kind {
	case "click":
		_, _ = c.call(ctx, sess.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": "mousePressed", "x": input.X, "y": input.Y, "button": "left", "clickCount": 1,
		})
		_, _ = c.call(ctx, sess.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": input.X, "y": input.Y, "button": "left", "clickCount": 1,
		})
	case "type":
		_, _ = c.call(ctx, sess.CDPSessionID, "Input.insertText", map[string]any{"text": input.Text})
	case "key":
		_, _ = c.call(ctx, sess.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": input.Key})
		_, _ = c.call(ctx, sess.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": input.Key})
	default:
		return TargetBinding{}, ErrActionForbidden
	}
	return e.inspectLive(ctx, c, sess)
}

// InspectBinding returns the current target/frame/document binding of the CDP session.
func (e *CDPBrowsingEngine) InspectBinding(ctx context.Context, sessionID uuid.UUID) (TargetBinding, error) {
	sess, err := e.getSession(sessionID)
	if err != nil {
		return TargetBinding{}, err
	}
	c, err := e.dialTarget(ctx, sess)
	if err != nil {
		return TargetBinding{}, err
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup
	return e.inspectLive(ctx, c, sess)
}

// CloseSession closes and disposes the isolated Chromium BrowserContext.
func (e *CDPBrowsingEngine) CloseSession(ctx context.Context, sessionID uuid.UUID) error {
	e.mu.Lock()
	sess, ok := e.sessions[sessionID]
	if ok {
		delete(e.sessions, sessionID)
	}
	e.mu.Unlock()
	if !ok {
		return nil
	}
	if sess.PublicProxy != nil {
		sess.PublicProxy.close()
	}
	c, err := e.dialBrowser(ctx)
	if err != nil {
		return nil
	}
	defer c.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck // cleanup
	_, _ = c.call(ctx, "", "Target.disposeBrowserContext", map[string]any{
		"browserContextId": sess.BrowserContextID,
	})
	return nil
}
