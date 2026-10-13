package docker

import (
	"context"
	"errors"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/desk"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

// DesktopAction forwards one computer-use call to the sandbox agent.
func (d *Driver) DesktopAction(ctx context.Context, id compute.EnvID, kind string, payload []byte) (desk.Result, error) {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return desk.Result{}, err
	}
	defer closeFn()
	resp, err := client.DesktopAction(rpcCtx, &agentv1.DesktopActionRequest{Kind: kind, PayloadJson: string(payload)})
	if err != nil {
		return desk.Result{}, err
	}
	result := desk.Result{
		Text:   resp.GetResultText(),
		Image:  resp.GetImage(),
		Mime:   resp.GetMimeType(),
		Width:  resp.GetWidth(),
		Height: resp.GetHeight(),
	}
	switch resp.GetErrorCode() {
	case "":
		return result, nil
	case "control.locked":
		return result, compute.ErrDesktopLocked
	case "validation":
		return result, desk.ErrInvalid
	default:
		return result, errors.New("desktop action failed")
	}
}

// PullDesktopActions reads the agent journal. Image bytes are copied for storage
// and are not logged.
func (d *Driver) PullDesktopActions(ctx context.Context, id compute.EnvID) ([]desk.StoredAction, error) {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	resp, err := client.PullDesktopActions(rpcCtx, &agentv1.PullDesktopActionsRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]desk.StoredAction, 0, len(resp.GetRecords()))
	for _, rec := range resp.GetRecords() {
		out = append(out, desk.StoredAction{
			Seq:       rec.GetSeq(),
			Session:   rec.GetSessionId(),
			Kind:      rec.GetKind(),
			Params:    []byte(rec.GetParamsJson()),
			Result:    rec.GetResultText(),
			ErrorCode: rec.GetErrorCode(),
			Image:     append([]byte(nil), rec.GetImage()...),
			Mime:      rec.GetMimeType(),
			X:         rec.GetCoordX(),
			Y:         rec.GetCoordY(),
			HasPoint:  rec.GetHasCoord(),
			At:        time.Unix(0, rec.GetUnixNano()).UTC(),
		})
	}
	return out, nil
}

// AckDesktopActions drops journal entries the API has stored.
func (d *Driver) AckDesktopActions(ctx context.Context, id compute.EnvID, session string, through uint64) error {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return err
	}
	defer closeFn()
	_, err = client.AckDesktopActions(rpcCtx, &agentv1.AckDesktopActionsRequest{SessionId: session, ThroughSeq: through})
	return err
}

// SetDesktopLock tells the agent whether a human currently owns the desktop.
func (d *Driver) SetDesktopLock(ctx context.Context, id compute.EnvID, human bool) error {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return err
	}
	defer closeFn()
	_, err = client.SetControlLock(rpcCtx, &agentv1.SetControlLockRequest{Human: human})
	return err
}
