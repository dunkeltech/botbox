// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
)

const (
	defaultVNCTicketTTL = 60 * time.Second
	watcherPresenceTTL  = 2 * time.Minute
	maxControlNoteLen   = 500
	maxTerminalCmdLen   = 4096
)

type watcherEntry struct {
	user     api.WatcherUser
	lastSeen time.Time
}

// ReconcileEnvironments brings persisted environment and runtime states back into sync
// with the configured compute driver on API startup.
func ReconcileEnvironments(ctx context.Context, pool *pgxpool.Pool, driver compute.Driver, logger *slog.Logger) error {
	if pool == nil || driver == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	queries := db.New(pool)
	rows, err := queries.ListEnvironmentsForReconcile(ctx)
	if err != nil {
		return fmt.Errorf("list environments for reconcile: %w", err)
	}

	var knownRunning []compute.EnvID
	for _, row := range rows {
		if row.Status == string(compute.StatusRunning) && row.EnvID != "" {
			knownRunning = append(knownRunning, compute.EnvID(row.EnvID))
		}
	}
	if len(knownRunning) == 0 {
		return nil
	}

	missing, err := driver.Reconcile(ctx, knownRunning)
	if err != nil {
		return fmt.Errorf("reconcile compute driver: %w", err)
	}
	missingSet := make(map[string]bool, len(missing))
	for _, id := range missing {
		missingSet[string(id)] = true
	}

	var reconcileErr error
	for _, row := range rows {
		if row.Status != string(compute.StatusRunning) || row.EnvID == "" {
			continue
		}
		if err := reconcileRunningEnvironment(ctx, pool, driver, logger, row, missingSet[row.EnvID]); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reconcile environment %s: %w", row.EnvID, err))
		}
	}
	return reconcileErr
}

func reconcileRunningEnvironment(ctx context.Context, pool *pgxpool.Pool, driver compute.Driver, logger *slog.Logger, row db.ListEnvironmentsForReconcileRow, driverMissing bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	q := db.New(tx)
	bot, err := q.LockBotByWorkspaceAndID(ctx, db.LockBotByWorkspaceAndIDParams{WorkspaceID: row.WorkspaceID, ID: row.BotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	env, err := q.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{WorkspaceID: bot.WorkspaceID, ID: bot.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if env.Status != string(compute.StatusRunning) || env.EnvID != row.EnvID {
		return nil
	}

	action := "environment.stopped"
	if !driverMissing {
		missingPrice, err := missingEnvironmentModelPrice(ctx, q, bot)
		if err != nil || missingPrice == nil {
			return err
		}
		action = "environment.stopped_missing_model_price"
	}
	if _, err := stopEnvironment(ctx, q, driver, bot, env.EnvID, nil, action); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	logger.Info("reconciled sandbox environment to stopped", "action", action, "bot_id", fromPGUUID(bot.ID), "env_id", env.EnvID)
	return nil
}

func (s *server) handleGetBotEnvironment(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}

	missingPrice, err := missingEnvironmentModelPrice(r.Context(), s.queries, botRow)
	if err != nil {
		s.environmentFailure(w, err)
		return
	}
	envRow, holderName, holderEmail, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}

	s.touchWatcher(fromPGUUID(botRow.ID), claims.Subject, claims.DisplayName)
	stats := s.fetchEnvStats(r.Context(), envRow)
	state := s.toAPIEnvironmentState(wsRow.ID, envRow, holderName, holderEmail, stats)
	state.MissingModelPrice = missingPrice
	writeJSON(w, s.logger, http.StatusOK, state)
}

func (s *server) handleStartBotEnvironment(w http.ResponseWriter, r *http.Request) {
	s.changeBotEnvironment(w, r, "start", "environment.started")
}

func (s *server) handleStopBotEnvironment(w http.ResponseWriter, r *http.Request) {
	s.changeBotEnvironment(w, r, "stop", "environment.stopped")
}

func (s *server) handleRestartBotEnvironment(w http.ResponseWriter, r *http.Request) {
	s.changeBotEnvironment(w, r, "restart", "environment.restarted")
}

func (s *server) changeBotEnvironment(w http.ResponseWriter, r *http.Request, operation, auditAction string) {
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}
	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.environmentFailure(w, err)
		return
	}
	defer rollbackTx(ctx, tx)
	q := s.queries.WithTx(tx)

	// Hold the bot row through Docker and persistence so a settings update
	// cannot commit an unpriced model between the price check and startup.
	bot, err := q.LockBotByWorkspaceAndID(ctx, db.LockBotByWorkspaceAndIDParams{WorkspaceID: ws.ID, ID: toPGUUID(botID)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
		return
	}
	if err != nil {
		s.environmentFailure(w, err)
		return
	}
	if !s.requireManageBot(w, role, claims.Subject, bot, operation+" sandbox") {
		return
	}

	env, holderName, holderEmail, ok := s.loadEnvironment(w, r, q, bot)
	if !ok {
		return
	}
	missingPrice, err := missingEnvironmentModelPrice(ctx, q, bot)
	if err != nil {
		s.environmentFailure(w, err)
		return
	}

	if operation == "stop" {
		env, err = stopEnvironment(ctx, q, s.compute, bot, env.EnvID, &claims.Subject, auditAction)
	} else if missingPrice != nil {
		if env.Status == string(compute.StatusRunning) {
			_, err = stopEnvironment(ctx, q, s.compute, bot, env.EnvID, &claims.Subject, "environment.stopped_missing_model_price")
		}
	} else {
		env, err = s.startEnvironment(ctx, q, bot, env, claims.Subject, operation == "restart", auditAction)
	}
	if err != nil {
		s.logger.Error("change sandbox environment", "operation", operation, "bot_id", botID, "err", err)
		s.writeComputeError(w, err, "failed to "+operation+" sandbox environment")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.environmentFailure(w, err)
		return
	}
	if missingPrice != nil && operation != "stop" {
		writeError(w, s.logger, http.StatusConflict, "environment.model_price_required",
			fmt.Sprintf("add a workspace model price for %s / %s", missingPrice.Provider, missingPrice.Model))
		return
	}

	if operation != "stop" {
		s.touchWatcher(botID, claims.Subject, claims.DisplayName)
	}
	stats := s.fetchEnvStats(ctx, env)
	state := s.toAPIEnvironmentState(ws.ID, env, holderName, holderEmail, stats)
	state.MissingModelPrice = missingPrice
	writeJSON(w, s.logger, http.StatusOK, state)
}

func (s *server) environmentFailure(w http.ResponseWriter, err error) {
	s.logger.Error("sandbox environment state unavailable", "err", err)
	writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to access sandbox environment state")
}

func (s *server) handleTakeBotControl(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	if actorRole == workspaces.RoleViewer {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot take desktop control")
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin take control tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to claim control")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	if err := txq.LockBotQueue(r.Context(), fromPGUUID(wsRow.ID).String()+"/"+fromPGUUID(botRow.ID).String()); err != nil {
		s.chatFailure(w)
		return
	}
	claimed, err := txq.ClaimEnvironmentControl(r.Context(), db.ClaimEnvironmentControlParams{
		BotID:               envRow.BotID,
		ControlHolderUserID: toPGUUID(claims.Subject),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusConflict, "control.locked", "another user currently holds desktop control")
			return
		}
		s.logger.Error("claim environment control", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to claim control")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	botUUID := fromPGUUID(botRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "environment.control_taken", "bot", &botUUID, nil); err != nil {
		s.logger.Error("audit take control", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit take control", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to claim control")
		return
	}

	s.touchWatcher(botUUID, claims.Subject, claims.DisplayName)
	s.touchEnvironmentActivity(r.Context(), wsRow.ID, botRow.ID)
	if queue, ok := s.turnQueue.(interface {
		Interrupt(context.Context, uuid.UUID, uuid.UUID) error
	}); ok {
		if err := queue.Interrupt(r.Context(), fromPGUUID(wsRow.ID), fromPGUUID(botRow.ID)); err != nil {
			s.chatFailure(w)
			return
		}
	}
	if err := s.pushDesktopLock(r.Context(), claimed, true); err != nil {
		s.logger.Error("push desktop lock", "bot_id", botUUID.String())
		_, _ = s.queries.ReleaseEnvironmentControl(r.Context(), db.ReleaseEnvironmentControlParams{
			BotID:               claimed.BotID,
			ControlHolderUserID: toPGUUID(claims.Subject),
			Column4:             true,
		})
		writeError(w, s.logger, http.StatusServiceUnavailable, "agent.unavailable", "sandbox did not accept the control lock")
		return
	}
	_, holderName, holderEmail, _ := s.lookupControlHolder(r.Context(), wsRow.ID, botRow.ID)
	stats := s.fetchEnvStats(r.Context(), claimed)
	writeJSON(w, s.logger, http.StatusOK, s.toAPIEnvironmentState(wsRow.ID, claimed, holderName, holderEmail, stats))
}

func (s *server) handleReleaseBotControl(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	if actorRole == workspaces.RoleViewer {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot release desktop control")
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}

	var req api.ReleaseControlRequest
	if !decodeOptionalJSON(w, r, s.logger, &req) {
		return
	}
	note := ""
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		if len(trimmed) > maxControlNoteLen {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "hand-back note exceeds 500 characters")
			return
		}
		note = trimmed
	}

	if _, _, _, ok := s.loadOrInitEnvironment(w, r, botRow); !ok {
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin release control tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to release control")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	if err := txq.LockBotQueue(r.Context(), fromPGUUID(wsRow.ID).String()+"/"+fromPGUUID(botRow.ID).String()); err != nil {
		s.chatFailure(w)
		return
	}
	// Read the holder under the queue lock so a duplicate release cannot retry a failed turn.
	envRow, err := txq.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{WorkspaceID: wsRow.ID, ID: botRow.ID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	controlWasHeld := envRow.ControlHolderUserID.Valid
	canOverride := !controlWasHeld || actorRole == workspaces.RoleOwner || actorRole == workspaces.RoleAdmin
	released, err := txq.ReleaseEnvironmentControl(r.Context(), db.ReleaseEnvironmentControlParams{
		BotID:               envRow.BotID,
		ControlHolderUserID: toPGUUID(claims.Subject),
		ControlNote:         note,
		Column4:             canOverride,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusConflict, "control.locked", "another user currently holds desktop control")
			return
		}
		s.logger.Error("release environment control", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to release control")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	botUUID := fromPGUUID(botRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "environment.control_released", "bot", &botUUID, map[string]any{
		"note": note,
	}); err != nil {
		s.logger.Error("audit release control", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}
	turnID, err := s.queueHandback(r.Context(), txq, wsRow.ID, botRow.ID, note, controlWasHeld)
	if err != nil {
		s.logger.Error("queue desktop handback", "bot_id", botUUID.String())
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to resume the bot")
		return
	}
	if turnID != uuid.Nil {
		if s.turnQueue == nil {
			writeError(w, s.logger, http.StatusServiceUnavailable, "dependency_unavailable", "turn queue is unavailable")
			return
		}
		if err := s.turnQueue.Enqueue(r.Context(), wsUUID, botUUID, turnID); err != nil {
			s.chatFailure(w)
			return
		}
	}
	if err := s.pushDesktopLock(r.Context(), released, false); err != nil {
		s.logger.Error("release desktop lock", "bot_id", botUUID.String())
		writeError(w, s.logger, http.StatusServiceUnavailable, "agent.unavailable", "sandbox did not accept the control lock")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit release control", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to release control")
		return
	}
	s.touchEnvironmentActivity(r.Context(), wsRow.ID, botRow.ID)

	stats := s.fetchEnvStats(r.Context(), released)
	writeJSON(w, s.logger, http.StatusOK, s.toAPIEnvironmentState(wsRow.ID, released, "", "", stats))
}

func (s *server) handleCreateVNCTicket(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}
	if envRow.Status != string(compute.StatusRunning) {
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
		return
	}

	canControl := actorRole != workspaces.RoleViewer &&
		envRow.ControlHolderUserID.Valid &&
		fromPGUUID(envRow.ControlHolderUserID) == claims.Subject

	now := s.now().UTC()
	expiresAt := now.Add(defaultVNCTicketTTL)
	wsUUID := fromPGUUID(wsRow.ID)
	botUUID := fromPGUUID(botRow.ID)

	token, err := s.ticketSigner.Sign(vncrelay.TicketClaims{
		WorkspaceID: wsUUID.String(),
		BotID:       botUUID.String(),
		UserID:      claims.Subject.String(),
		CanControl:  canControl,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		s.logger.Error("sign vnc ticket", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to issue stream ticket")
		return
	}

	s.touchWatcher(botUUID, claims.Subject, claims.DisplayName)
	s.touchEnvironmentActivity(r.Context(), wsRow.ID, botRow.ID)
	writeJSON(w, s.logger, http.StatusOK, api.StreamTicketResponse{
		Ticket:     token,
		WsPath:     fmt.Sprintf("/ws/bots/%s/%s/vnc", wsUUID.String(), botUUID.String()),
		CanControl: canControl,
		ExpiresAt:  expiresAt,
	})
}

func (s *server) handleExecBotTerminal(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	if !s.requireManageBot(w, actorRole, claims.Subject, botRow, "execute terminal commands") {
		return
	}

	var req api.TerminalExecRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	command := strings.TrimSpace(req.Command)
	if command == "" || len(command) > maxTerminalCmdLen {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "command must be 1..4096 characters")
		return
	}

	workdir := ""
	if req.Workdir != nil {
		workdir = strings.TrimSpace(*req.Workdir)
	}
	if workdir == "" {
		workdir = agent.DefaultHomeDir
	}
	// The working directory belongs to the sandbox. Only the agent can check
	// existence and resolve symlinks against its filesystem.
	cleaned := path.Clean(workdir)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		(path.IsAbs(cleaned) && cleaned != agent.DefaultHomeDir && !strings.HasPrefix(cleaned, agent.DefaultHomeDir+"/")) {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "workdir must stay inside /home/bot")
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}
	if envRow.Status != string(compute.StatusRunning) || envRow.EnvID == "" {
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
		return
	}

	res, err := s.compute.ExecCommand(r.Context(), compute.EnvID(envRow.EnvID), workdir, []string{"sh", "-c", command})
	if err != nil {
		s.writeComputeError(w, err, "terminal command failed")
		return
	}
	s.touchEnvironmentActivity(r.Context(), wsRow.ID, botRow.ID)

	writeJSON(w, s.logger, http.StatusOK, api.TerminalExecResponse{
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
		Cwd:      workdir,
	})
}

func (s *server) handleListBotFiles(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}

	requestedPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if requestedPath == "" {
		requestedPath = agent.DefaultHomeDir
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}
	if envRow.Status != string(compute.StatusRunning) || envRow.EnvID == "" {
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
		return
	}

	entries, err := s.compute.ListFiles(r.Context(), compute.EnvID(envRow.EnvID), requestedPath)
	if err != nil {
		s.writeComputeError(w, err, "failed to list sandbox files")
		return
	}

	out := make([]api.FileEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, api.FileEntry{
			Name:      entry.Name,
			Path:      entry.Path,
			IsDir:     entry.IsDir,
			SizeBytes: entry.SizeBytes,
			UpdatedAt: entry.UpdatedAt,
		})
	}
	writeJSON(w, s.logger, http.StatusOK, api.FileListResponse{
		Path:    requestedPath,
		Entries: out,
	})
}

func (s *server) handleReadBotFile(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}

	filePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if filePath == "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "file path is required")
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}
	if envRow.Status != string(compute.StatusRunning) || envRow.EnvID == "" {
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
		return
	}

	content, err := s.compute.ReadFile(r.Context(), compute.EnvID(envRow.EnvID), filePath)
	if err != nil {
		s.writeComputeError(w, err, "failed to read sandbox file")
		return
	}
	s.touchEnvironmentActivity(r.Context(), wsRow.ID, botRow.ID)

	writeJSON(w, s.logger, http.StatusOK, api.FileContentResponse{
		Path:      filePath,
		Content:   string(content),
		SizeBytes: int64(len(content)),
	})
}

func (s *server) handleWriteBotFile(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	if !s.requireManageBot(w, actorRole, claims.Subject, botRow, "write sandbox files") {
		return
	}

	var req api.WriteFileContentRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	filePath := strings.TrimSpace(req.Path)
	if filePath == "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "file path is required")
		return
	}
	if len(req.Content) > agent.MaxFileBytes {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "file exceeds 1 MiB limit")
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}
	if envRow.Status != string(compute.StatusRunning) || envRow.EnvID == "" {
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
		return
	}

	if err := s.compute.WriteFile(r.Context(), compute.EnvID(envRow.EnvID), filePath, []byte(req.Content)); err != nil {
		s.writeComputeError(w, err, "failed to write sandbox file")
		return
	}
	s.touchEnvironmentActivity(r.Context(), wsRow.ID, botRow.ID)

	writeJSON(w, s.logger, http.StatusOK, api.FileContentResponse{
		Path:      filePath,
		Content:   req.Content,
		SizeBytes: int64(len(req.Content)),
	})
}

func (s *server) loadWorkspaceBot(w http.ResponseWriter, r *http.Request, wsID pgtype.UUID) (db.Bot, bool) {
	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return db.Bot{}, false
	}

	botRow, err := s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsID,
		ID:          toPGUUID(botID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
			return db.Bot{}, false
		}
		s.logger.Error("load bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot")
		return db.Bot{}, false
	}
	return botRow, true
}

func (s *server) requireManageBot(w http.ResponseWriter, actorRole workspaces.Role, callerID uuid.UUID, botRow db.Bot, op string) bool {
	isBotOwner := botRow.OwnerUserID.Valid && fromPGUUID(botRow.OwnerUserID) == callerID
	if !workspaces.Can(actorRole, workspaces.ActionManageBot, isBotOwner) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient permissions to "+op)
		return false
	}
	return true
}

func (s *server) loadOrInitEnvironment(w http.ResponseWriter, r *http.Request, bot db.Bot) (db.Environment, string, string, bool) {
	return s.loadEnvironment(w, r, s.queries, bot)
}

func (s *server) loadEnvironment(w http.ResponseWriter, r *http.Request, q *db.Queries, bot db.Bot) (db.Environment, string, string, bool) {
	row, err := q.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: bot.WorkspaceID,
		ID:          bot.ID,
	})
	if err == nil {
		return environmentFromRow(row), row.ControlHolderDisplayName, row.ControlHolderEmail, true
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.environmentFailure(w, err)
		return db.Environment{}, "", "", false
	}

	created, err := q.InitializeEnvironment(r.Context(), db.InitializeEnvironmentParams{
		WorkspaceID: bot.WorkspaceID,
		BotID:       bot.ID,
		Driver:      s.computeDriverName(),
		Image:       compute.DefaultSandboxImage,
		CpuMillis:   compute.DefaultCPUMillis,
		MemoryMib:   compute.DefaultMemoryMiB,
		VolumeRef:   "botbox-home-" + fromPGUUID(bot.ID).String(),
	})
	if err != nil {
		s.environmentFailure(w, err)
		return db.Environment{}, "", "", false
	}
	return created, "", "", true
}

func environmentFromRow(row db.GetEnvironmentByWorkspaceAndBotRow) db.Environment {
	return db.Environment{
		BotID:               row.BotID,
		Driver:              row.Driver,
		EnvID:               row.EnvID,
		Status:              row.Status,
		Image:               row.Image,
		CpuMillis:           row.CpuMillis,
		MemoryMib:           row.MemoryMib,
		VolumeRef:           row.VolumeRef,
		ControlHolderUserID: row.ControlHolderUserID,
		ControlNote:         row.ControlNote,
		StartedAt:           row.StartedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

func (s *server) lookupControlHolder(ctx context.Context, wsID, botID pgtype.UUID) (pgtype.UUID, string, string, error) {
	row, err := s.queries.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: wsID,
		ID:          botID,
	})
	if err != nil {
		return pgtype.UUID{}, "", "", err
	}
	return row.ControlHolderUserID, row.ControlHolderDisplayName, row.ControlHolderEmail, nil
}

func (s *server) ensureCreatedAndStarted(ctx context.Context, botRow db.Bot, envRow db.Environment) (compute.EnvID, error) {
	cfg := compute.BotConfig{
		WorkspaceID: fromPGUUID(botRow.WorkspaceID).String(),
		BotID:       fromPGUUID(botRow.ID).String(),
		Image:       envRow.Image,
		CPUMillis:   int(envRow.CpuMillis),
		MemoryMiB:   int(envRow.MemoryMib),
		VolumeRef:   envRow.VolumeRef,
	}

	envID := compute.EnvID(strings.TrimSpace(envRow.EnvID))
	if envID != "" {
		err := s.compute.StartEnvironment(ctx, envID)
		if err == nil {
			return envID, nil
		}
		if !errors.Is(err, compute.ErrEnvironmentNotFound) {
			return "", err
		}
	}

	createdID, err := s.compute.CreateEnvironment(ctx, cfg)
	if err != nil {
		return "", err
	}
	if err := s.compute.StartEnvironment(ctx, createdID); err != nil {
		return "", err
	}
	return createdID, nil
}

func (s *server) startEnvironment(ctx context.Context, q *db.Queries, bot db.Bot, env db.Environment, actorID uuid.UUID, restart bool, auditAction string) (db.Environment, error) {
	if s.maxRunningSandboxes > 0 || s.maxSandboxMemoryMiB > 0 {
		if err := q.LockHostCapacity(ctx); err != nil {
			return db.Environment{}, fmt.Errorf("lock host capacity: %w", err)
		}
		capRow, err := q.RunningEnvironmentCapacity(ctx, bot.ID)
		if err != nil {
			return db.Environment{}, fmt.Errorf("query running capacity: %w", err)
		}
		if s.maxRunningSandboxes > 0 && capRow.RunningCount+1 > int64(s.maxRunningSandboxes) {
			return db.Environment{}, compute.ErrHostCapacity
		}
		if s.maxSandboxMemoryMiB > 0 && capRow.RunningMemoryMib+int64(env.MemoryMib) > int64(s.maxSandboxMemoryMiB) {
			return db.Environment{}, compute.ErrHostCapacity
		}
	}
	if restart {
		if err := stopComputeEnvironment(ctx, s.compute, env.EnvID); err != nil {
			return db.Environment{}, err
		}
	}
	id, err := s.ensureCreatedAndStarted(ctx, bot, env)
	if err != nil {
		return db.Environment{}, err
	}

	now := s.now().UTC()
	updated, err := q.UpsertEnvironment(ctx, db.UpsertEnvironmentParams{
		BotID:               bot.ID,
		Driver:              s.computeDriverName(),
		EnvID:               string(id),
		Status:              string(compute.StatusRunning),
		Image:               env.Image,
		CpuMillis:           env.CpuMillis,
		MemoryMib:           env.MemoryMib,
		VolumeRef:           env.VolumeRef,
		ControlHolderUserID: env.ControlHolderUserID,
		ControlNote:         env.ControlNote,
		StartedAt:           toPGTimestamptz(now),
	})
	if err == nil {
		if touchErr := q.TouchEnvironmentActivity(ctx, db.TouchEnvironmentActivityParams{
			WorkspaceID: bot.WorkspaceID,
			BotID:       bot.ID,
			AsOf:        toPGTimestamptz(now),
		}); touchErr != nil {
			err = touchErr
		}
	}
	if err == nil {
		wsID, botID := fromPGUUID(bot.WorkspaceID), fromPGUUID(bot.ID)
		err = recordAudit(ctx, q, &wsID, &actorID, auditAction, "bot", &botID, map[string]any{
			"env_id": string(id),
			"driver": s.computeDriverName(),
		})
	}
	if err != nil && (restart || env.Status != string(compute.StatusRunning) || string(id) != env.EnvID) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		err = errors.Join(err, stopComputeEnvironment(cleanup, s.compute, string(id)))
	}
	return updated, err
}

func stopEnvironment(ctx context.Context, q *db.Queries, driver compute.Driver, bot db.Bot, envID string, actorID *uuid.UUID, auditAction string) (db.Environment, error) {
	stopped, err := q.MarkEnvironmentStopped(ctx, db.MarkEnvironmentStoppedParams{WorkspaceID: bot.WorkspaceID, BotID: bot.ID})
	if err != nil {
		return db.Environment{}, err
	}
	if err := finishEnvironmentStop(ctx, q, driver, bot, envID, actorID, auditAction); err != nil {
		return db.Environment{}, err
	}
	return stopped, nil
}

func finishEnvironmentStop(ctx context.Context, q *db.Queries, driver compute.Driver, bot db.Bot, envID string, actorID *uuid.UUID, auditAction string) error {
	wsID, botID := fromPGUUID(bot.WorkspaceID), fromPGUUID(bot.ID)
	if err := recordAudit(ctx, q, &wsID, actorID, auditAction, "bot", &botID, map[string]any{"env_id": envID}); err != nil {
		return err
	}
	return stopComputeEnvironment(ctx, driver, envID)
}

func stopComputeEnvironment(ctx context.Context, driver compute.Driver, envID string) error {
	if strings.TrimSpace(envID) == "" {
		return nil
	}
	if err := driver.StopEnvironment(ctx, compute.EnvID(envID)); !errors.Is(err, compute.ErrEnvironmentNotFound) {
		return err
	}
	return nil
}

func (s *server) authorizeVNC(ctx context.Context, claims vncrelay.TicketClaims) error {
	if s.queries == nil {
		return errors.New("desktop authorization database unavailable")
	}
	wsID, wsErr := uuid.Parse(claims.WorkspaceID)
	botID, botErr := uuid.Parse(claims.BotID)
	userID, userErr := uuid.Parse(claims.UserID)
	if wsErr != nil || botErr != nil || userErr != nil {
		return vncrelay.ErrAccessRevoked
	}
	allowed, err := s.queries.CanAccessDesktop(ctx, db.CanAccessDesktopParams{
		WorkspaceID: toPGUUID(wsID), BotID: toPGUUID(botID), UserID: toPGUUID(userID), CanControl: claims.CanControl,
	})
	if err != nil {
		return fmt.Errorf("check desktop authorization: %w", err)
	}
	if !allowed {
		return vncrelay.ErrAccessRevoked
	}
	s.touchEnvironmentActivity(ctx, toPGUUID(wsID), toPGUUID(botID))
	return nil
}

func (s *server) resolveVNCUpstream(ctx context.Context, workspaceIDStr, botIDStr string) (string, error) {
	row, err := s.runningEnvironment(ctx, workspaceIDStr, botIDStr)
	if err != nil {
		return "", err
	}
	return s.compute.GetVNCStreamURL(ctx, compute.EnvID(row.EnvID))
}

func (s *server) dialVNCUpstream(ctx context.Context, workspaceIDStr, botIDStr string) (net.Conn, error) {
	dialer, ok := s.compute.(compute.VNCDialer)
	if !ok {
		return nil, errors.New("sandbox desktop dialer unavailable")
	}
	row, err := s.runningEnvironment(ctx, workspaceIDStr, botIDStr)
	if err != nil {
		return nil, err
	}
	return dialer.DialVNC(ctx, compute.EnvID(row.EnvID))
}

func (s *server) runningEnvironment(ctx context.Context, workspaceIDStr, botIDStr string) (db.GetEnvironmentByWorkspaceAndBotRow, error) {
	if s.queries == nil || s.compute == nil {
		return db.GetEnvironmentByWorkspaceAndBotRow{}, errors.New("sandbox runtime unavailable")
	}
	wsID, err := uuid.Parse(workspaceIDStr)
	if err != nil {
		return db.GetEnvironmentByWorkspaceAndBotRow{}, err
	}
	botID, err := uuid.Parse(botIDStr)
	if err != nil {
		return db.GetEnvironmentByWorkspaceAndBotRow{}, err
	}
	row, err := s.queries.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: toPGUUID(wsID),
		ID:          toPGUUID(botID),
	})
	if err != nil {
		return db.GetEnvironmentByWorkspaceAndBotRow{}, err
	}
	if row.Status != string(compute.StatusRunning) || strings.TrimSpace(row.EnvID) == "" {
		return db.GetEnvironmentByWorkspaceAndBotRow{}, compute.ErrEnvironmentNotRunning
	}
	return row, nil
}

func (s *server) fetchEnvStats(ctx context.Context, env db.Environment) api.EnvironmentStats {
	fallback := api.EnvironmentStats{
		CpuPercent:     0,
		MemoryUsedMiB:  0,
		MemoryLimitMiB: int(env.MemoryMib),
		VolumeUsedMiB:  0,
		VolumeLimitMiB: compute.DefaultVolumeMiB,
	}
	if s.compute == nil || strings.TrimSpace(env.EnvID) == "" {
		return fallback
	}
	stats, err := s.compute.Stats(ctx, compute.EnvID(env.EnvID))
	if err != nil {
		return fallback
	}
	limitMiB := stats.MemoryLimitMiB
	if limitMiB <= 0 {
		limitMiB = int(env.MemoryMib)
	}
	volLimitMiB := stats.VolumeLimitMiB
	if volLimitMiB <= 0 {
		volLimitMiB = compute.DefaultVolumeMiB
	}
	return api.EnvironmentStats{
		CpuPercent:     stats.CPUPercent,
		MemoryUsedMiB:  stats.MemoryUsedMiB,
		MemoryLimitMiB: limitMiB,
		VolumeUsedMiB:  stats.VolumeUsedMiB,
		VolumeLimitMiB: volLimitMiB,
	}
}

func (s *server) toAPIEnvironmentState(
	wsID pgtype.UUID,
	env db.Environment,
	holderName string,
	holderEmail string,
	stats api.EnvironmentStats,
) api.EnvironmentStateResponse {
	botUUID := fromPGUUID(env.BotID)
	var holder *api.ControlHolder
	if env.ControlHolderUserID.Valid {
		name := holderName
		if name == "" {
			name = "Workspace user"
		}
		email := holderEmail
		if email == "" {
			email = "user@example.invalid"
		}
		holder = &api.ControlHolder{
			UserId:      fromPGUUID(env.ControlHolderUserID),
			DisplayName: name,
			Email:       openapi_types.Email(email),
		}
	}

	var startedAt *time.Time
	if env.StartedAt.Valid {
		t := env.StartedAt.Time.UTC()
		startedAt = &t
	}
	updatedAt := s.now().UTC()
	if env.UpdatedAt.Valid {
		updatedAt = env.UpdatedAt.Time.UTC()
	}

	return api.EnvironmentStateResponse{
		BotId:         botUUID,
		WorkspaceId:   fromPGUUID(wsID),
		Driver:        env.Driver,
		EnvId:         env.EnvID,
		Status:        api.EnvironmentStatus(env.Status),
		Image:         env.Image,
		CpuMillis:     int(env.CpuMillis),
		MemoryMiB:     int(env.MemoryMib),
		VolumeRef:     env.VolumeRef,
		ControlHolder: holder,
		ControlNote:   env.ControlNote,
		Stats:         stats,
		Watchers:      s.listWatchers(botUUID),
		StartedAt:     startedAt,
		UpdatedAt:     updatedAt,
	}
}

func (s *server) touchWatcher(botID, userID uuid.UUID, displayName string) {
	now := s.now().UTC()
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "Member"
	}

	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()

	perBot, ok := s.watchers[botID]
	if !ok {
		perBot = make(map[uuid.UUID]watcherEntry)
		s.watchers[botID] = perBot
	}
	perBot[userID] = watcherEntry{
		user: api.WatcherUser{
			UserId:      userID,
			DisplayName: name,
		},
		lastSeen: now,
	}
}

func (s *server) listWatchers(botID uuid.UUID) []api.WatcherUser {
	now := s.now().UTC()
	cutoff := now.Add(-watcherPresenceTTL)

	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()

	perBot := s.watchers[botID]
	if len(perBot) == 0 {
		return []api.WatcherUser{}
	}
	out := make([]api.WatcherUser, 0, len(perBot))
	for uid, entry := range perBot {
		if entry.lastSeen.Before(cutoff) {
			delete(perBot, uid)
			continue
		}
		out = append(out, entry.user)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].UserId.String() < out[j].UserId.String()
	})
	return out
}

func (s *server) hasActiveWatchers(botID uuid.UUID) bool {
	return len(s.listWatchers(botID)) > 0
}

func (s *server) touchEnvironmentActivity(ctx context.Context, workspaceID, botID pgtype.UUID) {
	if s.queries == nil {
		return
	}
	if err := s.queries.TouchEnvironmentActivity(ctx, db.TouchEnvironmentActivityParams{
		WorkspaceID: workspaceID,
		BotID:       botID,
		AsOf:        toPGTimestamptz(s.now().UTC()),
	}); err != nil && s.logger != nil {
		s.logger.Debug("touch environment activity failed", slog.Any("error", err))
	}
}

func (s *server) computeDriverName() string {
	if _, ok := s.compute.(*docker.Driver); ok {
		return compute.DefaultDriverDocker
	}
	return compute.DefaultDriverFake
}

func (s *server) writeComputeError(w http.ResponseWriter, err error, fallbackMsg string) {
	switch {
	case errors.Is(err, compute.ErrEnvironmentNotRunning):
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
	case errors.Is(err, compute.ErrEnvironmentNotFound):
		writeError(w, s.logger, http.StatusNotFound, "environment.not_found", "sandbox environment not found")
	case errors.Is(err, compute.ErrInvalidConfig):
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, compute.ErrSecurityViolation):
		writeError(w, s.logger, http.StatusForbidden, "security_violation", err.Error())
	case errors.Is(err, compute.ErrHostCapacity):
		writeError(w, s.logger, http.StatusServiceUnavailable, "environment.capacity_exceeded",
			"the container host has reached its configured running sandbox capacity")
	case errors.Is(err, compute.ErrNetworkCapacity):
		// A fixed message: the wrapped Docker error names host networks that
		// are none of the caller's business.
		writeError(w, s.logger, http.StatusServiceUnavailable, "environment.network_capacity",
			"the container host has no free network address range for another computer")
	case status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.InvalidArgument:
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", status.Convert(err).Message())
	case status.Code(err) == codes.NotFound:
		writeError(w, s.logger, http.StatusNotFound, "file.not_found", status.Convert(err).Message())
	default:
		writeError(w, s.logger, http.StatusBadGateway, "sandbox_unavailable", fallbackMsg)
	}
}

func mustEphemeralTicketSigner() *vncrelay.Signer {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// Fallback to non-empty deterministic-length slice only if CSPRNG fails.
		secret = []byte("botbox-ephemeral-vnc-signing-key")
	}
	signer, _ := vncrelay.NewSigner(secret)
	return signer
}
