package authbroker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Session status values persisted by the broker and API.
const (
	StatusBound         = "bound"
	StatusAuthenticated = "authenticated"
	StatusMFARequired   = "mfa_required"
	StatusTakeover      = "takeover"
	StatusRevoked       = "revoked"
	StatusExpired       = "expired"
)

// OpenSessionInput binds an isolated broker browser target to a reviewed adapter.
type OpenSessionInput struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	BotID       uuid.UUID `json:"bot_id"`
	SessionID   uuid.UUID `json:"session_id,omitempty"`
	AdapterID   string    `json:"adapter_id"`
}

// SubmitCredentialsInput delivers an encrypted-in-transit user secret solely to the broker fill boundary.
type SubmitCredentialsInput struct {
	WorkspaceID uuid.UUID     `json:"workspace_id"`
	BotID       uuid.UUID     `json:"bot_id"`
	SessionID   uuid.UUID     `json:"session_id"`
	AdapterID   string        `json:"adapter_id"`
	Expected    TargetBinding `json:"expected"`
	Username    string        `json:"username,omitempty"`
	Secret      string        `json:"secret"`
}

// SubmitCredentialsResult reports non-secret completion status after atomic bind-and-fill.
type SubmitCredentialsResult struct {
	Session SessionInfo `json:"session"`
	Outcome FillOutcome `json:"outcome"`
}

// SessionInfo exposes non-secret metadata about an isolated broker session.
type SessionInfo struct {
	SessionID         uuid.UUID     `json:"session_id"`
	WorkspaceID       uuid.UUID     `json:"workspace_id"`
	BotID             uuid.UUID     `json:"bot_id"`
	AdapterID         string        `json:"adapter_id"`
	Origin            string        `json:"origin"`
	Status            string        `json:"status"`
	Binding           TargetBinding `json:"binding"`
	TakeoverActive    bool          `json:"takeover_active"`
	TakeoverUserID    *uuid.UUID    `json:"takeover_user_id,omitempty"`
	CaptureSuppressed bool          `json:"capture_suppressed"`
	ExpiresAt         time.Time     `json:"expires_at"`
}

// Client is implemented by both the in-process Service and the HTTP control-plane client.
type Client interface {
	OpenSession(ctx context.Context, in OpenSessionInput) (SessionInfo, error)
	GetSession(ctx context.Context, workspaceID, sessionID uuid.UUID) (SessionInfo, error)
	SubmitCredentials(ctx context.Context, in SubmitCredentialsInput) (SubmitCredentialsResult, error)
	BotNavigate(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, rawURL string) (SessionInfo, error)
	BotRead(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, selector string) (ReadResult, error)
	BotClick(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, selector string) (ClickResult, error)
	BotScreenshot(ctx context.Context, workspaceID, botID, sessionID uuid.UUID) ([]byte, TargetBinding, error)
	StartTakeover(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) (SessionInfo, error)
	TakeoverFrame(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) ([]byte, SessionInfo, error)
	TakeoverInput(ctx context.Context, workspaceID, sessionID, userID uuid.UUID, input TakeoverInput) (SessionInfo, error)
	CompleteTakeover(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) (SessionInfo, error)
	RevokeSession(ctx context.Context, workspaceID, sessionID uuid.UUID) error
	RevokeBotSessions(ctx context.Context, workspaceID, botID uuid.UUID) error
}

type managedSession struct {
	mu             sync.Mutex
	id             uuid.UUID
	workspaceID    uuid.UUID
	botID          uuid.UUID
	adapter        LoginAdapter
	status         string
	binding        TargetBinding
	submitted      bool
	sensitivePhase bool
	takeoverActive bool
	takeoverUserID *uuid.UUID
	expiresAt      time.Time
}

// Service coordinates isolated broker sessions, atomic fill locks, and human takeovers.
type Service struct {
	engine BrowserEngine
	now    func() time.Time

	mu       sync.RWMutex
	sessions map[uuid.UUID]*managedSession
}

// NewService constructs a broker Service backed by the given isolated BrowserEngine.
func NewService(engine BrowserEngine) *Service {
	return &Service{
		engine:   engine,
		now:      func() time.Time { return time.Now().UTC() },
		sessions: make(map[uuid.UUID]*managedSession),
	}
}

// SetNow overrides the clock for deterministic expiration tests.
func (s *Service) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

func (ms *managedSession) snapshot() SessionInfo {
	var takeoverUserCopy *uuid.UUID
	if ms.takeoverUserID != nil {
		id := *ms.takeoverUserID
		takeoverUserCopy = &id
	}
	captureSuppressed := ms.sensitivePhase || ms.takeoverActive ||
		ms.status == StatusBound || ms.status == StatusMFARequired || ms.status == StatusTakeover
	return SessionInfo{
		SessionID:         ms.id,
		WorkspaceID:       ms.workspaceID,
		BotID:             ms.botID,
		AdapterID:         ms.adapter.ID,
		Origin:            ms.adapter.Origin,
		Status:            ms.status,
		Binding:           ms.binding,
		TakeoverActive:    ms.takeoverActive,
		TakeoverUserID:    takeoverUserCopy,
		CaptureSuppressed: captureSuppressed,
		ExpiresAt:         ms.expiresAt,
	}
}

func (s *Service) lookup(workspaceID, sessionID uuid.UUID) (*managedSession, error) {
	s.mu.RLock()
	ms, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok || ms.workspaceID != workspaceID {
		return nil, ErrSessionNotFound
	}
	return ms, nil
}

func (s *Service) checkActiveLocked(ctx context.Context, ms *managedSession) error {
	if ms.status == StatusRevoked {
		return ErrSessionNotFound
	}
	if !s.now().Before(ms.expiresAt) {
		ms.status = StatusExpired
		ms.takeoverActive = false
		_ = s.engine.CloseSession(ctx, ms.id)
		return ErrSessionExpired
	}
	return nil
}

// OpenSession launches or returns a bound target in the isolated broker for a reviewed adapter.
func (s *Service) OpenSession(ctx context.Context, in OpenSessionInput) (SessionInfo, error) {
	if in.WorkspaceID == uuid.Nil || in.BotID == uuid.Nil {
		return SessionInfo{}, ErrSessionNotFound
	}
	adapter, ok := LookupLoginAdapter(in.AdapterID)
	if !ok {
		return SessionInfo{}, ErrAdapterNotFound
	}
	sessionID := in.SessionID
	if sessionID == uuid.Nil {
		sessionID = uuid.New()
	}

	s.mu.Lock()
	if existing, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		if existing.workspaceID != in.WorkspaceID || existing.botID != in.BotID {
			return SessionInfo{}, ErrSessionNotFound
		}
		existing.mu.Lock()
		defer existing.mu.Unlock()
		if err := s.checkActiveLocked(ctx, existing); err != nil {
			return SessionInfo{}, err
		}
		if live, err := s.engine.InspectBinding(ctx, sessionID); err == nil {
			existing.binding = live
		}
		return existing.snapshot(), nil
	}
	s.mu.Unlock()

	binding, err := s.engine.OpenTarget(ctx, in.WorkspaceID, in.BotID, sessionID, adapter)
	if err != nil {
		return SessionInfo{}, err
	}
	ttl := adapter.SessionTTL
	if ttl <= 0 {
		ttl = DefaultBrokerSessionTTL
	}
	ms := &managedSession{
		id:          sessionID,
		workspaceID: in.WorkspaceID,
		botID:       in.BotID,
		adapter:     adapter,
		status:      StatusBound,
		binding:     binding,
		// Keep capture suppressed during the entire credential entry window.
		sensitivePhase: true,
		expiresAt:      s.now().Add(ttl),
	}

	s.mu.Lock()
	if existing, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		_ = s.engine.CloseSession(ctx, sessionID)
		if existing.workspaceID != in.WorkspaceID || existing.botID != in.BotID {
			return SessionInfo{}, ErrSessionNotFound
		}
		existing.mu.Lock()
		defer existing.mu.Unlock()
		return existing.snapshot(), nil
	}
	s.sessions[sessionID] = ms
	s.mu.Unlock()

	return ms.snapshot(), nil
}

// GetSession returns current non-secret metadata for a workspace-scoped broker session.
func (s *Service) GetSession(ctx context.Context, workspaceID, sessionID uuid.UUID) (SessionInfo, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.checkActiveLocked(ctx, ms); err != nil {
		return SessionInfo{}, err
	}
	if live, err := s.engine.InspectBinding(ctx, sessionID); err == nil {
		ms.binding = live
	}
	return ms.snapshot(), nil
}

// SubmitCredentials performs an atomic bind check and blind fill in the isolated broker.
func (s *Service) SubmitCredentials(ctx context.Context, in SubmitCredentialsInput) (SubmitCredentialsResult, error) {
	if in.Secret == "" {
		return SubmitCredentialsResult{}, ErrActionForbidden
	}
	ms, err := s.lookup(in.WorkspaceID, in.SessionID)
	if err != nil {
		return SubmitCredentialsResult{}, err
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.botID != in.BotID {
		return SubmitCredentialsResult{}, ErrSessionNotFound
	}
	if ms.adapter.ID != in.AdapterID {
		return SubmitCredentialsResult{}, ErrBindingMismatch
	}
	if err := s.checkActiveLocked(ctx, ms); err != nil {
		return SubmitCredentialsResult{}, err
	}
	if ms.submitted {
		return SubmitCredentialsResult{}, ErrSessionAlreadyUsed
	}
	if ms.takeoverActive {
		return SubmitCredentialsResult{}, ErrTakeoverActive
	}

	expected := in.Expected
	if expected.TargetID == "" {
		expected = ms.binding
	}
	if !bindingsEqualForSubmit(expected, ms.binding) {
		return SubmitCredentialsResult{}, ErrBindingMismatch
	}

	ms.sensitivePhase = true
	outcome, err := s.engine.AtomicBindAndFill(ctx, ms.id, expected, ms.adapter, in.Username, in.Secret)
	if err != nil {
		// Abort leave session non-reusable if the browser page mutated mid-flight.
		return SubmitCredentialsResult{}, err
	}

	ms.submitted = true
	ms.binding = outcome.Binding
	if outcome.RequiresMFA {
		ms.status = StatusMFARequired
		ms.sensitivePhase = true
	} else {
		ms.status = StatusAuthenticated
		ms.sensitivePhase = false
	}

	return SubmitCredentialsResult{
		Session: ms.snapshot(),
		Outcome: outcome,
	}, nil
}

func bindingsEqualForSubmit(expected, actual TargetBinding) bool {
	if expected.TargetID != actual.TargetID ||
		expected.FrameID != actual.FrameID ||
		expected.DocumentID != actual.DocumentID ||
		expected.Origin != actual.Origin ||
		expected.FormAction != actual.FormAction {
		return false
	}
	if expected.PageURL != "" && expected.PageURL != actual.PageURL {
		return false
	}
	return true
}

func (s *Service) checkBotAccessLocked(ctx context.Context, ms *managedSession, botID uuid.UUID) error {
	if ms.botID != botID {
		return ErrSessionNotFound
	}
	if err := s.checkActiveLocked(ctx, ms); err != nil {
		return err
	}
	if ms.takeoverActive || ms.status == StatusTakeover {
		return ErrTakeoverActive
	}
	return nil
}

// BotNavigate directs the isolated broker session only to adapter-permitted HTTPS destinations.
func (s *Service) BotNavigate(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, rawURL string) (SessionInfo, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.checkBotAccessLocked(ctx, ms, botID); err != nil {
		return SessionInfo{}, err
	}
	if ms.sensitivePhase {
		return SessionInfo{}, ErrCaptureSuppressed
	}
	binding, err := s.engine.Navigate(ctx, ms.id, ms.adapter, rawURL)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.binding = binding
	return ms.snapshot(), nil
}

// BotRead returns sanitized, secret-stripped text from a non-credential selector.
func (s *Service) BotRead(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, selector string) (ReadResult, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return ReadResult{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.checkBotAccessLocked(ctx, ms, botID); err != nil {
		return ReadResult{}, err
	}
	if ms.sensitivePhase {
		return ReadResult{}, ErrCaptureSuppressed
	}
	res, err := s.engine.ReadSanitized(ctx, ms.id, ms.adapter, selector)
	if err != nil {
		return ReadResult{}, err
	}
	ms.binding = res.Binding
	return res, nil
}

// BotClick clicks a permitted non-credential element inside the broker session.
func (s *Service) BotClick(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, selector string) (ClickResult, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return ClickResult{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.checkBotAccessLocked(ctx, ms, botID); err != nil {
		return ClickResult{}, err
	}
	if ms.sensitivePhase {
		return ClickResult{}, ErrCaptureSuppressed
	}
	res, err := s.engine.ClickElement(ctx, ms.id, ms.adapter, selector)
	if err != nil {
		return ClickResult{}, err
	}
	ms.binding = res.Binding
	return res, nil
}

// BotScreenshot captures a viewport image only when no sensitive or takeover phase is active.
func (s *Service) BotScreenshot(ctx context.Context, workspaceID, botID, sessionID uuid.UUID) ([]byte, TargetBinding, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.checkBotAccessLocked(ctx, ms, botID); err != nil {
		return nil, TargetBinding{}, err
	}
	if ms.sensitivePhase || ms.status == StatusBound || ms.status == StatusMFARequired {
		return nil, TargetBinding{}, ErrCaptureSuppressed
	}
	img, binding, err := s.engine.CaptureSanitized(ctx, ms.id, ms.adapter)
	if err != nil {
		return nil, TargetBinding{}, err
	}
	ms.binding = binding
	return img, binding, nil
}

// StartTakeover puts the session into human-only takeover pinned to userID, disabling bot access & captures.
func (s *Service) StartTakeover(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) (SessionInfo, error) {
	if userID == uuid.Nil {
		return SessionInfo{}, ErrTakeoverForbidden
	}
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.checkActiveLocked(ctx, ms); err != nil {
		return SessionInfo{}, err
	}
	if ms.takeoverActive && ms.takeoverUserID != nil && *ms.takeoverUserID != userID {
		return SessionInfo{}, ErrTakeoverForbidden
	}
	uid := userID
	ms.takeoverActive = true
	ms.takeoverUserID = &uid
	ms.sensitivePhase = true
	ms.status = StatusTakeover
	if live, err := s.engine.InspectBinding(ctx, ms.id); err == nil {
		ms.binding = live
	}
	return ms.snapshot(), nil
}

func (s *Service) verifyTakeoverOwnerLocked(ctx context.Context, ms *managedSession, userID uuid.UUID) error {
	if err := s.checkActiveLocked(ctx, ms); err != nil {
		return err
	}
	if !ms.takeoverActive {
		return ErrTakeoverNotActive
	}
	if ms.takeoverUserID == nil || *ms.takeoverUserID != userID {
		return ErrTakeoverForbidden
	}
	return nil
}

// TakeoverFrame captures a broker frame exclusively for the authorized takeover human user.
func (s *Service) TakeoverFrame(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) ([]byte, SessionInfo, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return nil, SessionInfo{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.verifyTakeoverOwnerLocked(ctx, ms, userID); err != nil {
		return nil, SessionInfo{}, err
	}
	img, binding, err := s.engine.CaptureTakeoverFrame(ctx, ms.id, ms.adapter)
	if err != nil {
		return nil, SessionInfo{}, err
	}
	ms.binding = binding
	return img, ms.snapshot(), nil
}

// TakeoverInput forwards mouse/keyboard events solely from the takeover owner to the isolated broker.
func (s *Service) TakeoverInput(ctx context.Context, workspaceID, sessionID, userID uuid.UUID, input TakeoverInput) (SessionInfo, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.verifyTakeoverOwnerLocked(ctx, ms, userID); err != nil {
		return SessionInfo{}, err
	}
	binding, err := s.engine.SendTakeoverInput(ctx, ms.id, ms.adapter, input)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.binding = binding
	return ms.snapshot(), nil
}

// CompleteTakeover verifies the isolated broker landed on an adapter-permitted origin, ends takeover,
// and re-enables non-sensitive bot operations on the authenticated session.
func (s *Service) CompleteTakeover(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) (SessionInfo, error) {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := s.verifyTakeoverOwnerLocked(ctx, ms, userID); err != nil {
		return SessionInfo{}, err
	}
	live, err := s.engine.InspectBinding(ctx, ms.id)
	if err != nil {
		return SessionInfo{}, err
	}
	if _, err := ms.adapter.PermitsNavigation(live.PageURL); err != nil {
		return SessionInfo{}, fmt.Errorf("%w: %v", ErrNavigationForbidden, err)
	}
	ms.binding = live
	ms.takeoverActive = false
	ms.takeoverUserID = nil
	ms.sensitivePhase = false
	ms.submitted = true
	ms.status = StatusAuthenticated
	return ms.snapshot(), nil
}

// RevokeSession closes and purges a single isolated broker session.
func (s *Service) RevokeSession(ctx context.Context, workspaceID, sessionID uuid.UUID) error {
	ms, err := s.lookup(workspaceID, sessionID)
	if err != nil {
		return err
	}
	ms.mu.Lock()
	ms.status = StatusRevoked
	ms.takeoverActive = false
	ms.takeoverUserID = nil
	ms.mu.Unlock()

	err = s.engine.CloseSession(ctx, sessionID)
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	return err
}

// RevokeBotSessions revokes and purges all broker sessions owned by a bot within a workspace.
func (s *Service) RevokeBotSessions(ctx context.Context, workspaceID, botID uuid.UUID) error {
	s.mu.Lock()
	var targets []*managedSession
	for id, ms := range s.sessions {
		if ms.workspaceID == workspaceID && ms.botID == botID {
			targets = append(targets, ms)
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()

	for _, ms := range targets {
		ms.mu.Lock()
		ms.status = StatusRevoked
		ms.takeoverActive = false
		ms.takeoverUserID = nil
		ms.mu.Unlock()
		_ = s.engine.CloseSession(ctx, ms.id)
	}
	return nil
}
