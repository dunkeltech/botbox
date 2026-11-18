// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"

	"github.com/dunkeltech/botbox/backend/internal/desk"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DesktopAction runs one parsed computer-use action and reports a stable error code.
// Validation and lock failures stay in the response so the timeline can store them.
func (s *Server) DesktopAction(ctx context.Context, req *agentv1.DesktopActionRequest) (*agentv1.DesktopActionResponse, error) {
	if err := checkRPCContext(ctx, "desktop action"); err != nil {
		return nil, err
	}
	if req == nil || req.Kind == "" {
		return nil, status.Error(codes.InvalidArgument, "desktop action is required")
	}
	payload := req.PayloadJson
	if payload == "" {
		payload = "{}"
	}
	act, err := desk.Parse(req.Kind, []byte(payload))
	if err != nil {
		return &agentv1.DesktopActionResponse{ErrorCode: "validation"}, nil
	}
	if s.desktop == nil {
		return nil, status.Error(codes.FailedPrecondition, "desktop service is not configured")
	}
	result, err := s.desktop.Perform(ctx, act)
	resp := &agentv1.DesktopActionResponse{
		ResultText: result.Text,
		Image:      result.Image,
		MimeType:   result.Mime,
		Width:      result.Width,
		Height:     result.Height,
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrControlLocked):
			resp.ErrorCode = "control.locked"
		case errors.Is(err, ErrInvalidDesktopInput), errors.Is(err, desk.ErrInvalid):
			resp.ErrorCode = "validation"
		default:
			resp.ErrorCode = "desktop.failed"
		}
	}
	return resp, nil
}

// SetControlLock applies a human takeover inside this agent process.
func (s *Server) SetControlLock(ctx context.Context, req *agentv1.SetControlLockRequest) (*agentv1.SetControlLockResponse, error) {
	if err := checkRPCContext(ctx, "set control lock"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "control lock request is required")
	}
	if s.desktop == nil || s.desktop.control == nil {
		return nil, status.Error(codes.FailedPrecondition, "desktop control is unavailable")
	}
	s.desktop.SetHuman(req.Human)
	return &agentv1.SetControlLockResponse{}, nil
}

// PullDesktopActions copies the unacked journal. Screenshot bytes stay out of logs.
func (s *Server) PullDesktopActions(ctx context.Context, req *agentv1.PullDesktopActionsRequest) (*agentv1.PullDesktopActionsResponse, error) {
	if err := checkRPCContext(ctx, "pull desktop actions"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "pull request is required")
	}
	if s.desktop == nil {
		return &agentv1.PullDesktopActionsResponse{}, nil
	}
	journal := s.desktop.Journal()
	if journal == nil {
		return &agentv1.PullDesktopActionsResponse{}, nil
	}
	entries := journal.Pull()
	resp := &agentv1.PullDesktopActionsResponse{SessionId: journal.Session()}
	for _, entry := range entries {
		resp.Records = append(resp.Records, &agentv1.DesktopActionRecord{
			Seq:        entry.Seq,
			SessionId:  entry.Session,
			Kind:       entry.Kind,
			ParamsJson: string(entry.Params),
			ResultText: entry.Result,
			ErrorCode:  entry.ErrorCode,
			Image:      entry.Image,
			MimeType:   entry.Mime,
			CoordX:     entry.X,
			CoordY:     entry.Y,
			HasCoord:   entry.HasPoint,
			UnixNano:   entry.At.UnixNano(),
		})
	}
	return resp, nil
}

// AckDesktopActions drops journal entries the API has stored.
func (s *Server) AckDesktopActions(ctx context.Context, req *agentv1.AckDesktopActionsRequest) (*agentv1.AckDesktopActionsResponse, error) {
	if err := checkRPCContext(ctx, "ack desktop actions"); err != nil {
		return nil, err
	}
	if req == nil || req.SessionId == "" || req.ThroughSeq == 0 {
		return nil, status.Error(codes.InvalidArgument, "ack requires a session and sequence")
	}
	if s.desktop != nil {
		if journal := s.desktop.Journal(); journal != nil {
			journal.Ack(req.SessionId, req.ThroughSeq)
		}
	}
	return &agentv1.AckDesktopActionsResponse{}, nil
}
