package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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

	for _, row := range rows {
		if row.Status == string(compute.StatusRunning) && missingSet[row.EnvID] {
			if _, updErr := queries.UpdateEnvironmentStatus(ctx, db.UpdateEnvironmentStatusParams{
				BotID:     row.BotID,
				Status:    string(compute.StatusStopped),
				StartedAt: pgtype.Timestamptz{},
			}); updErr != nil {
				return fmt.Errorf("update reconciled environment %s: %w", fromPGUUID(row.BotID), updErr)
			}
			logger.Info("reconciled missing sandbox container to stopped",
				slog.String("bot_id", fromPGUUID(row.BotID).String()),
				slog.String("env_id", row.EnvID),
			)
		}
	}
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

	envRow, holderName, holderEmail, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}

	s.touchWatcher(fromPGUUID(botRow.ID), claims.Subject, claims.DisplayName)
	stats := s.fetchEnvStats(r.Context(), envRow)
	writeJSON(w, s.logger, http.StatusOK, s.toAPIEnvironmentState(wsRow.ID, envRow, holderName, holderEmail, stats))
}

func (s *server) handleStartBotEnvironment(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	if !s.requireManageBot(w, actorRole, claims.Subject, botRow, "start sandbox") {
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}

	envID, err := s.ensureCreatedAndStarted(r.Context(), botRow, envRow)
	if err != nil {
		s.logger.Error("start sandbox environment", "err", err, "bot_id", fromPGUUID(botRow.ID))
		s.writeComputeError(w, err, "failed to start sandbox environment")
		return
	}

	now := s.now().UTC()
	updated, err := s.persistStartedEnvironment(r.Context(), botRow, envRow, string(envID), now, claims.Subject, "environment.started")
	if err != nil {
		s.logger.Error("persist started environment", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save environment state")
		return
	}

	s.touchWatcher(fromPGUUID(botRow.ID), claims.Subject, claims.DisplayName)
	_, holderName, holderEmail, _ := s.lookupControlHolder(r.Context(), wsRow.ID, botRow.ID)
	stats := s.fetchEnvStats(r.Context(), updated)
	writeJSON(w, s.logger, http.StatusOK, s.toAPIEnvironmentState(wsRow.ID, updated, holderName, holderEmail, stats))
}

func (s *server) handleStopBotEnvironment(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	if !s.requireManageBot(w, actorRole, claims.Subject, botRow, "stop sandbox") {
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}

	if strings.TrimSpace(envRow.EnvID) != "" {
		if err := s.compute.StopEnvironment(r.Context(), compute.EnvID(envRow.EnvID)); err != nil && !errors.Is(err, compute.ErrEnvironmentNotFound) {
			s.logger.Error("stop sandbox environment", "err", err, "bot_id", fromPGUUID(botRow.ID))
			s.writeComputeError(w, err, "failed to stop sandbox environment")
			return
		}
	}

	updated, err := s.persistStoppedEnvironment(r.Context(), botRow, envRow, claims.Subject, "environment.stopped")
	if err != nil {
		s.logger.Error("persist stopped environment", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save environment state")
		return
	}

	stats := s.fetchEnvStats(r.Context(), updated)
	writeJSON(w, s.logger, http.StatusOK, s.toAPIEnvironmentState(wsRow.ID, updated, "", "", stats))
}

func (s *server) handleRestartBotEnvironment(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	if !s.requireManageBot(w, actorRole, claims.Subject, botRow, "restart sandbox") {
		return
	}

	envRow, _, _, ok := s.loadOrInitEnvironment(w, r, botRow)
	if !ok {
		return
	}

	if strings.TrimSpace(envRow.EnvID) != "" {
		if err := s.compute.StopEnvironment(r.Context(), compute.EnvID(envRow.EnvID)); err != nil && !errors.Is(err, compute.ErrEnvironmentNotFound) {
			s.logger.Error("stop sandbox before restart", "err", err)
			s.writeComputeError(w, err, "failed to stop sandbox before restart")
			return
		}
	}

	envID, err := s.ensureCreatedAndStarted(r.Context(), botRow, envRow)
	if err != nil {
		s.logger.Error("restart sandbox environment", "err", err)
		s.writeComputeError(w, err, "failed to restart sandbox environment")
		return
	}

	now := s.now().UTC()
	updated, err := s.persistStartedEnvironment(r.Context(), botRow, envRow, string(envID), now, claims.Subject, "environment.restarted")
	if err != nil {
		s.logger.Error("persist restarted environment", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to save environment state")
		return
	}

	s.touchWatcher(fromPGUUID(botRow.ID), claims.Subject, claims.DisplayName)
	_, holderName, holderEmail, _ := s.lookupControlHolder(r.Context(), wsRow.ID, botRow.ID)
	stats := s.fetchEnvStats(r.Context(), updated)
	writeJSON(w, s.logger, http.StatusOK, s.toAPIEnvironmentState(wsRow.ID, updated, holderName, holderEmail, stats))
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

	workdir := agent.DefaultHomeDir
	if req.Workdir != nil && strings.TrimSpace(*req.Workdir) != "" {
		if _, err := agent.ResolveHomePath(agent.DefaultHomeDir, *req.Workdir); err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "workdir must stay inside /home/bot")
			return
		}
		workdir = strings.TrimSpace(*req.Workdir)
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

func (s *server) loadOrInitEnvironment(w http.ResponseWriter, r *http.Request, botRow db.Bot) (db.Environment, string, string, bool) {
	row, err := s.queries.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: botRow.WorkspaceID,
		ID:          botRow.ID,
	})
	if err == nil {
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
		}, row.ControlHolderDisplayName, row.ControlHolderEmail, true
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("load bot environment", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load sandbox environment")
		return db.Environment{}, "", "", false
	}

	botID := fromPGUUID(botRow.ID)
	created, upsertErr := s.queries.UpsertEnvironment(r.Context(), db.UpsertEnvironmentParams{
		BotID:       botRow.ID,
		Driver:      s.computeDriverName(),
		EnvID:       "",
		Status:      string(compute.StatusStopped),
		Image:       compute.DefaultSandboxImage,
		CpuMillis:   compute.DefaultCPUMillis,
		MemoryMib:   compute.DefaultMemoryMiB,
		VolumeRef:   "botbox-home-" + botID.String(),
		ControlNote: "",
	})
	if upsertErr != nil {
		s.logger.Error("initialize bot environment row", "err", upsertErr)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to initialize sandbox environment")
		return db.Environment{}, "", "", false
	}
	return created, "", "", true
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

func (s *server) persistStartedEnvironment(
	ctx context.Context,
	botRow db.Bot,
	envRow db.Environment,
	envID string,
	startedAt time.Time,
	actorID uuid.UUID,
	auditAction string,
) (db.Environment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Environment{}, err
	}
	defer rollbackTx(ctx, tx)

	txq := s.queries.WithTx(tx)
	updated, err := txq.UpsertEnvironment(ctx, db.UpsertEnvironmentParams{
		BotID:               botRow.ID,
		Driver:              s.computeDriverName(),
		EnvID:               envID,
		Status:              string(compute.StatusRunning),
		Image:               envRow.Image,
		CpuMillis:           envRow.CpuMillis,
		MemoryMib:           envRow.MemoryMib,
		VolumeRef:           envRow.VolumeRef,
		ControlHolderUserID: envRow.ControlHolderUserID,
		ControlNote:         envRow.ControlNote,
		StartedAt:           toPGTimestamptz(startedAt),
	})
	if err != nil {
		return db.Environment{}, err
	}

	wsUUID := fromPGUUID(botRow.WorkspaceID)
	botUUID := fromPGUUID(botRow.ID)
	if err := recordAudit(ctx, txq, &wsUUID, &actorID, auditAction, "bot", &botUUID, map[string]any{
		"env_id": envID,
		"driver": s.computeDriverName(),
	}); err != nil {
		return db.Environment{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return db.Environment{}, err
	}
	return updated, nil
}

func (s *server) persistStoppedEnvironment(
	ctx context.Context,
	botRow db.Bot,
	envRow db.Environment,
	actorID uuid.UUID,
	auditAction string,
) (db.Environment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Environment{}, err
	}
	defer rollbackTx(ctx, tx)

	txq := s.queries.WithTx(tx)
	updated, err := txq.UpsertEnvironment(ctx, db.UpsertEnvironmentParams{
		BotID:       botRow.ID,
		Driver:      envRow.Driver,
		EnvID:       envRow.EnvID,
		Status:      string(compute.StatusStopped),
		Image:       envRow.Image,
		CpuMillis:   envRow.CpuMillis,
		MemoryMib:   envRow.MemoryMib,
		VolumeRef:   envRow.VolumeRef,
		ControlNote: envRow.ControlNote,
		StartedAt:   pgtype.Timestamptz{},
	})
	if err != nil {
		return db.Environment{}, err
	}

	wsUUID := fromPGUUID(botRow.WorkspaceID)
	botUUID := fromPGUUID(botRow.ID)
	if err := recordAudit(ctx, txq, &wsUUID, &actorID, auditAction, "bot", &botUUID, map[string]any{
		"env_id": envRow.EnvID,
	}); err != nil {
		return db.Environment{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return db.Environment{}, err
	}
	return updated, nil
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
