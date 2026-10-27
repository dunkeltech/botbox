package authbroker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOpenSessionRejectsUnreviewedAdapter(t *testing.T) {
	t.Parallel()
	svc := NewService(NewMemoryBrowserEngine())
	_, err := svc.OpenSession(context.Background(), OpenSessionInput{
		WorkspaceID: uuid.New(),
		BotID:       uuid.New(),
		AdapterID:   "arbitrary_bank",
	})
	if !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("expected ErrAdapterNotFound, got %v", err)
	}
}

func TestAtomicBindingRejectsTamperedOrStaleTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding
	}{
		{
			name: "switched_tab",
			mutate: func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding {
				eng.SwitchActiveTab(sid, "https://evil.example:443", "https://evil.example/phish")
				return expected
			},
		},
		{
			name: "wrong_frame_id",
			mutate: func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding {
				eng.ReplaceFrame(sid)
				return expected
			},
		},
		{
			name: "same_origin_document_replacement",
			mutate: func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding {
				eng.ReplaceDocument(sid, "https://github.com:443/login?step=2")
				return expected
			},
		},
		{
			name: "cross_origin_redirect",
			mutate: func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding {
				eng.RedirectTab(sid, "https://attacker.example:443", "https://attacker.example/login", "https://attacker.example/steal")
				return expected
			},
		},
		{
			name: "tampered_form_action_same_origin",
			mutate: func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding {
				eng.RedirectTab(sid, "https://github.com:443", "https://github.com:443/login", "/evil/collect")
				return expected
			},
		},
		{
			name: "closed_target",
			mutate: func(eng *MemoryBrowserEngine, sid uuid.UUID, expected TargetBinding) TargetBinding {
				eng.CloseActiveTab(sid)
				return expected
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng := NewMemoryBrowserEngine()
			svc := NewService(eng)
			wsID := uuid.New()
			botID := uuid.New()

			sess, err := svc.OpenSession(ctx, OpenSessionInput{
				WorkspaceID: wsID,
				BotID:       botID,
				AdapterID:   "github_web",
			})
			if err != nil {
				t.Fatalf("OpenSession failed: %v", err)
			}

			expected := tc.mutate(eng, sess.SessionID, sess.Binding)
			_, err = svc.SubmitCredentials(ctx, SubmitCredentialsInput{
				WorkspaceID: wsID,
				BotID:       botID,
				SessionID:   sess.SessionID,
				AdapterID:   "github_web",
				Expected:    expected,
				Username:    "alice",
				Secret:      "super-secret-password-123",
			})
			if !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("expected ErrBindingMismatch, got %v", err)
			}
			if !eng.StoredPasswordEmpty(sess.SessionID) {
				t.Fatal("password was stored despite binding mismatch")
			}
		})
	}
}

func TestAtomicBindAndFillDeterministicMidFlightRaceBarrier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	raceModes := []string{"switch_tab", "replace_doc", "cross_origin", "close_target"}
	for _, mode := range raceModes {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			eng := NewMemoryBrowserEngine()
			svc := NewService(eng)
			wsID := uuid.New()
			botID := uuid.New()

			sess, err := svc.OpenSession(ctx, OpenSessionInput{
				WorkspaceID: wsID,
				BotID:       botID,
				AdapterID:   "gitlab_web",
			})
			if err != nil {
				t.Fatalf("OpenSession failed: %v", err)
			}

			enteredHook := make(chan struct{})
			releaseHook := make(chan struct{})
			eng.OnBeforeFill = func(uuid.UUID) {
				close(enteredHook)
				<-releaseHook
			}

			var submitErr error
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, submitErr = svc.SubmitCredentials(ctx, SubmitCredentialsInput{ //nolint:gosec // synthetic test fixture
					WorkspaceID: wsID,
					BotID:       botID,
					SessionID:   sess.SessionID,
					AdapterID:   "gitlab_web",
					Expected:    sess.Binding,
					Username:    "alice",
					Secret:      "gitlab-secret-999",
				})
			}()

			<-enteredHook
			switch mode {
			case "switch_tab":
				eng.SwitchActiveTab(sess.SessionID, "https://gitlab.com:443", "https://gitlab.com:443/explore")
			case "replace_doc":
				eng.ReplaceDocument(sess.SessionID, "https://gitlab.com:443/users/sign_in?reloaded=1")
			case "cross_origin":
				eng.RedirectTab(sess.SessionID, "https://evil.example:443", "https://evil.example/login", "/collect")
			case "close_target":
				eng.CloseActiveTab(sess.SessionID)
			}
			close(releaseHook)
			wg.Wait()

			if !errors.Is(submitErr, ErrBindingMismatch) {
				t.Fatalf("expected mid-flight race abort ErrBindingMismatch, got %v", submitErr)
			}
			if !eng.StoredPasswordEmpty(sess.SessionID) {
				t.Fatal("password leaked into tab after mid-flight mutation")
			}
		})
	}
}

func TestSessionCannotBeReusedAcrossConcurrentSubmissions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := NewMemoryBrowserEngine()
	svc := NewService(eng)
	wsID := uuid.New()
	botID := uuid.New()

	sess, err := svc.OpenSession(ctx, OpenSessionInput{
		WorkspaceID: wsID,
		BotID:       botID,
		AdapterID:   "odoo_staging",
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	res, err := svc.SubmitCredentials(ctx, SubmitCredentialsInput{
		WorkspaceID: wsID,
		BotID:       botID,
		SessionID:   sess.SessionID,
		AdapterID:   "odoo_staging",
		Expected:    sess.Binding,
		Username:    "ops",
		Secret:      "odoo-secret-1",
	})
	if err != nil {
		t.Fatalf("first SubmitCredentials failed: %v", err)
	}
	if res.Session.Status != StatusAuthenticated || res.Session.CaptureSuppressed {
		t.Fatalf("unexpected post-login session state: %+v", res.Session)
	}

	_, err = svc.SubmitCredentials(ctx, SubmitCredentialsInput{ //nolint:gosec // synthetic test fixture
		WorkspaceID: wsID,
		BotID:       botID,
		SessionID:   sess.SessionID,
		AdapterID:   "odoo_staging",
		Expected:    res.Session.Binding,
		Username:    "ops",
		Secret:      "odoo-secret-2",
	})
	if !errors.Is(err, ErrSessionAlreadyUsed) {
		t.Fatalf("expected ErrSessionAlreadyUsed on second fill, got %v", err)
	}
}

func TestBotBoundariesTakeoverIsolationAndSanitization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := NewMemoryBrowserEngine()
	svc := NewService(eng)
	wsID := uuid.New()
	botID := uuid.New()
	ownerUser := uuid.New()
	otherUser := uuid.New()

	sess, err := svc.OpenSession(ctx, OpenSessionInput{
		WorkspaceID: wsID,
		BotID:       botID,
		AdapterID:   "github_web",
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}
	eng.SetMFARequired(sess.SessionID, true)

	// Capture & bot reads are suppressed during initial credential phase.
	if _, _, shotErr := svc.BotScreenshot(ctx, wsID, botID, sess.SessionID); !errors.Is(shotErr, ErrCaptureSuppressed) {
		t.Fatalf("expected ErrCaptureSuppressed before auth, got %v", shotErr)
	}

	// Password submission succeeds, landing on MFA state (still capture-suppressed).
	subRes, err := svc.SubmitCredentials(ctx, SubmitCredentialsInput{ //nolint:gosec // synthetic test fixture
		WorkspaceID: wsID,
		BotID:       botID,
		SessionID:   sess.SessionID,
		AdapterID:   "github_web",
		Expected:    sess.Binding,
		Username:    "alice",
		Secret:      "gh-pw-never-leak-777",
	})
	if err != nil {
		t.Fatalf("SubmitCredentials failed: %v", err)
	}
	if !subRes.Outcome.RequiresMFA || subRes.Session.Status != StatusMFARequired {
		t.Fatalf("expected MFA required state, got %+v", subRes)
	}
	if _, _, shotErr := svc.BotScreenshot(ctx, wsID, botID, sess.SessionID); !errors.Is(shotErr, ErrCaptureSuppressed) {
		t.Fatalf("expected ErrCaptureSuppressed during MFA, got %v", shotErr)
	}

	// Human takeover locks out ALL bot actions and pins to ownerUser.
	tkSess, err := svc.StartTakeover(ctx, wsID, sess.SessionID, ownerUser)
	if err != nil {
		t.Fatalf("StartTakeover failed: %v", err)
	}
	if !tkSess.TakeoverActive || tkSess.Status != StatusTakeover {
		t.Fatalf("expected takeover active, got %+v", tkSess)
	}

	if _, err := svc.BotRead(ctx, wsID, botID, sess.SessionID, "body"); !errors.Is(err, ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive for BotRead, got %v", err)
	}
	if _, err := svc.BotClick(ctx, wsID, botID, sess.SessionID, "button"); !errors.Is(err, ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive for BotClick, got %v", err)
	}
	if _, err := svc.BotNavigate(ctx, wsID, botID, sess.SessionID, "https://github.com/settings"); !errors.Is(err, ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive for BotNavigate, got %v", err)
	}
	if _, _, err := svc.BotScreenshot(ctx, wsID, botID, sess.SessionID); !errors.Is(err, ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive for BotScreenshot, got %v", err)
	}

	// Another user cannot view takeover frames, send input, start takeover, or complete it.
	if _, _, err := svc.TakeoverFrame(ctx, wsID, sess.SessionID, otherUser); !errors.Is(err, ErrTakeoverForbidden) {
		t.Fatalf("expected ErrTakeoverForbidden for other user frame, got %v", err)
	}
	if _, err := svc.TakeoverInput(ctx, wsID, sess.SessionID, otherUser, TakeoverInput{Kind: "key", Key: "Enter"}); !errors.Is(err, ErrTakeoverForbidden) {
		t.Fatalf("expected ErrTakeoverForbidden for other user input, got %v", err)
	}
	if _, err := svc.StartTakeover(ctx, wsID, sess.SessionID, otherUser); !errors.Is(err, ErrTakeoverForbidden) {
		t.Fatalf("expected ErrTakeoverForbidden for takeover steal, got %v", err)
	}
	if _, err := svc.CompleteTakeover(ctx, wsID, sess.SessionID, otherUser); !errors.Is(err, ErrTakeoverForbidden) {
		t.Fatalf("expected ErrTakeoverForbidden for complete, got %v", err)
	}

	// Cross-workspace isolation.
	if _, err := svc.GetSession(ctx, uuid.New(), sess.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound for cross-workspace GetSession, got %v", err)
	}

	// Authorized owner completes takeover.
	frame, _, err := svc.TakeoverFrame(ctx, wsID, sess.SessionID, ownerUser)
	if err != nil || len(frame) == 0 {
		t.Fatalf("TakeoverFrame failed: %v (len=%d)", err, len(frame))
	}
	if _, err := svc.TakeoverInput(ctx, wsID, sess.SessionID, ownerUser, TakeoverInput{Kind: "key", Key: "Enter"}); err != nil {
		t.Fatalf("TakeoverInput failed: %v", err)
	}
	doneSess, err := svc.CompleteTakeover(ctx, wsID, sess.SessionID, ownerUser)
	if err != nil {
		t.Fatalf("CompleteTakeover failed: %v", err)
	}
	if doneSess.TakeoverActive || doneSess.Status != StatusAuthenticated || doneSess.CaptureSuppressed {
		t.Fatalf("unexpected state after CompleteTakeover: %+v", doneSess)
	}

	// Bot restricted read/navigation/capture now works within adapter origin only.
	if _, err := svc.BotRead(ctx, wsID, botID, sess.SessionID, "input[type='password']"); !errors.Is(err, ErrActionForbidden) {
		t.Fatalf("expected ErrActionForbidden reading password input, got %v", err)
	}
	if _, err := svc.BotNavigate(ctx, wsID, botID, sess.SessionID, "https://evil.example/steal"); !errors.Is(err, ErrNavigationForbidden) {
		t.Fatalf("expected ErrNavigationForbidden for non-adapter host, got %v", err)
	}
	readRes, err := svc.BotRead(ctx, wsID, botID, sess.SessionID, "body")
	if err != nil {
		t.Fatalf("BotRead after auth failed: %v", err)
	}
	if strings.Contains(readRes.Text, "gh-pw-never-leak-777") {
		t.Fatalf("secret leaked in sanitized output: %q", readRes.Text)
	}
	img, _, err := svc.BotScreenshot(ctx, wsID, botID, sess.SessionID)
	if err != nil || len(img) == 0 {
		t.Fatalf("BotScreenshot after auth failed: %v", err)
	}
}

func TestSessionExpiryAndBotCleanupRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := NewMemoryBrowserEngine()
	svc := NewService(eng)

	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return now })

	wsID := uuid.New()
	botID := uuid.New()
	sess, err := svc.OpenSession(ctx, OpenSessionInput{
		WorkspaceID: wsID,
		BotID:       botID,
		AdapterID:   "odoo_staging",
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	now = now.Add(DefaultBrokerSessionTTL + time.Second)
	if _, err := svc.GetSession(ctx, wsID, sess.SessionID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired after TTL, got %v", err)
	}

	// Active session is wiped cleanly by RevokeBotSessions.
	sess2, err := svc.OpenSession(ctx, OpenSessionInput{
		WorkspaceID: wsID,
		BotID:       botID,
		AdapterID:   "odoo_staging",
	})
	if err != nil {
		t.Fatalf("second OpenSession failed: %v", err)
	}
	if err := svc.RevokeBotSessions(ctx, wsID, botID); err != nil {
		t.Fatalf("RevokeBotSessions failed: %v", err)
	}
	if _, err := svc.GetSession(ctx, wsID, sess2.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after RevokeBotSessions, got %v", err)
	}
}
