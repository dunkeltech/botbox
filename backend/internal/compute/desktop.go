package compute

import (
	"context"
	"errors"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// ErrDesktopLocked means a human holds the desktop, so the bot action was rejected.
var ErrDesktopLocked = errors.New("desktop control lock held by human")

// DesktopBridge is the sandbox-agent half of computer use.
// Memory drivers do not implement it; API-mode tools then stay unavailable.
type DesktopBridge interface {
	DesktopAction(ctx context.Context, id EnvID, kind string, payload []byte) (desk.Result, error)
	PullDesktopActions(ctx context.Context, id EnvID) ([]desk.StoredAction, error)
	AckDesktopActions(ctx context.Context, id EnvID, session string, through uint64) error
	SetDesktopLock(ctx context.Context, id EnvID, human bool) error
}
