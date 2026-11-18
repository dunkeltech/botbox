// SPDX-License-Identifier: AGPL-3.0-only

package authbroker

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestCustomGitLabPolicyIsRequiredAndRechecked(t *testing.T) {
	ctx := context.Background()
	ws, bot := uuid.New(), uuid.New()
	in := OpenSessionInput{WorkspaceID: ws, BotID: bot, AdapterID: "gitlab_web", TargetOrigin: "https://gitlab.example.org"}
	svc := NewService(NewMemoryBrowserEngine())
	if _, err := svc.OpenSession(ctx, in); !errors.Is(err, ErrNavigationForbidden) {
		t.Fatalf("missing policy: %v", err)
	}

	operations := []string{"get", "submit", "navigate", "read", "click", "screenshot", "start", "frame", "input", "complete", "reopen"}
	for _, op := range operations {
		t.Run(op, func(t *testing.T) {
			eng := NewMemoryBrowserEngine()
			svc := NewService(eng)
			allowed := true
			svc.SetGitLabPolicy(func(ctx context.Context, gotWS, gotBot, _ uuid.UUID, origin string) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !allowed || gotWS != ws || gotBot != bot || origin != "https://gitlab.example.org:443" {
					return ErrNavigationForbidden
				}
				return nil
			})
			session, err := svc.OpenSession(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if session.Origin != "https://gitlab.example.org:443" || session.Binding.FormAction != "https://gitlab.example.org:443/users/sign_in" {
				t.Fatalf("wrong binding: %+v", session)
			}
			allowed = false
			id := session.SessionID
			switch op {
			case "get":
				_, err = svc.GetSession(ctx, ws, id)
			case "submit":
				_, err = svc.SubmitCredentials(ctx, SubmitCredentialsInput{WorkspaceID: ws, BotID: bot, SessionID: id, AdapterID: "gitlab_web", Expected: session.Binding, Secret: "must-never-be-filled"})
			case "navigate":
				_, err = svc.BotNavigate(ctx, ws, bot, id, "https://gitlab.example.org/")
			case "read":
				_, err = svc.BotRead(ctx, ws, bot, id, "body")
			case "click":
				_, err = svc.BotClick(ctx, ws, bot, id, "a")
			case "screenshot":
				_, _, err = svc.BotScreenshot(ctx, ws, bot, id)
			case "start":
				_, err = svc.StartTakeover(ctx, ws, id, uuid.New())
			case "frame":
				_, _, err = svc.TakeoverFrame(ctx, ws, id, uuid.New())
			case "input":
				_, err = svc.TakeoverInput(ctx, ws, id, uuid.New(), TakeoverInput{})
			case "complete":
				_, err = svc.CompleteTakeover(ctx, ws, id, uuid.New())
			case "reopen":
				reopen := in
				reopen.SessionID = id
				_, err = svc.OpenSession(ctx, reopen)
			}
			if !errors.Is(err, ErrNavigationForbidden) {
				t.Fatalf("revocation bypass for %s: %v", op, err)
			}
			if !eng.StoredPasswordEmpty(id) {
				t.Fatal("credential filled after revocation")
			}
		})
	}
}

func TestCustomGitLabSessionCannotBeRebound(t *testing.T) {
	svc := NewService(NewMemoryBrowserEngine())
	svc.SetGitLabPolicy(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) error { return nil })
	in := OpenSessionInput{WorkspaceID: uuid.New(), BotID: uuid.New(), AdapterID: "gitlab_web", TargetOrigin: "https://gitlab.example.org"}
	sess, err := svc.OpenSession(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.SessionID = sess.SessionID
	in.TargetOrigin = "https://other.example.org"
	if _, err := svc.OpenSession(context.Background(), in); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session rebound: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.SetGitLabPolicy(func(ctx context.Context, _, _, _ uuid.UUID, _ string) error { return ctx.Err() })
	if _, err := svc.GetSession(ctx, in.WorkspaceID, sess.SessionID); !errors.Is(err, ErrNavigationForbidden) {
		t.Fatalf("cancellation did not fail closed: %v", err)
	}
}
