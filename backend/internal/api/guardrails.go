package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/guardrails"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	defaultGuardrailLogLimit = 50
	maxGuardrailLogLimit     = 200
)

func (s *server) handleGetWorkspaceGuardrails(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	wsID := fromPGUUID(ws.ID)

	policyRow, err := s.queries.GetWorkspaceGuardrailPolicy(r.Context(), ws.ID)
	var policy guardrails.Policy
	if errors.Is(err, pgx.ErrNoRows) {
		policy = guardrails.DefaultPolicy(wsID)
		policy.UpdatedAt = ws.CreatedAt.Time.UTC()
	} else if err != nil {
		s.logger.Error("load workspace guardrail policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load guardrail policy")
		return
	} else {
		policy = guardrails.PolicyFromDB(policyRow)
	}

	ruleRows, err := s.queries.ListWorkspaceGuardrailRules(r.Context(), ws.ID)
	if err != nil {
		s.logger.Error("list workspace guardrail rules", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list guardrail rules")
		return
	}
	rules := make([]guardrails.CustomRule, 0, len(ruleRows))
	apiRules := make([]apigen.WorkspaceGuardrailRule, 0, len(ruleRows))
	for _, row := range ruleRows {
		rDomain := guardrails.RuleFromDB(row)
		rules = append(rules, rDomain)
		apiRules = append(apiRules, toAPIGuardrailRule(rDomain))
	}

	since := s.now().UTC().Add(-24 * time.Hour)
	summaryRow, err := s.queries.SummarizeLLMGuardrailEvents24h(r.Context(), db.SummarizeLLMGuardrailEvents24hParams{
		WorkspaceID: ws.ID,
		Since:       pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		s.logger.Error("summarize guardrail events", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to summarize guardrail events")
		return
	}

	providers := append([]string(nil), guardrails.CoveredProviders...)
	writeJSON(w, s.logger, http.StatusOK, apigen.WorkspaceGuardrailsResponse{
		Policy: toAPIGuardrailPolicy(policy),
		Rules:  apiRules,
		Summary24h: apigen.GuardrailSummary24h{
			TotalEvaluations: summaryRow.TotalCount,
			RedactedCount:    summaryRow.RedactedCount,
			BlockedCount:     summaryRow.BlockedCount,
			AuditedCount:     summaryRow.AuditedCount,
		},
		ActiveRuleCount:  guardrails.ActiveRuleCount(policy, rules),
		CoveredProviders: providers,
	})
}

func (s *server) handleUpdateWorkspaceGuardrailPolicy(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGuardrails)
	if !ok {
		return
	}
	var req apigen.UpdateGuardrailPolicyRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	failureMode, err := guardrails.ValidateFailureMode(string(req.FailureMode))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	secDir, err := guardrails.ValidateDirection(string(req.Secrets.Direction))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	secAct, err := guardrails.ValidateAction(string(req.Secrets.Action))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	piiDir, err := guardrails.ValidateDirection(string(req.Pii.Direction))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	piiAct, err := guardrails.ValidateAction(string(req.Pii.Action))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	rawEntities := make([]string, 0, len(req.Pii.Entities))
	for _, ent := range req.Pii.Entities {
		rawEntities = append(rawEntities, string(ent))
	}
	piiEntities, err := guardrails.ValidatePIIEntities(rawEntities)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	injDir, err := guardrails.ValidateDirection(string(req.PromptInjection.Direction))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	injAct, err := guardrails.ValidateAction(string(req.PromptInjection.Action))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update guardrail policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail policy")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	row, err := q.UpsertWorkspaceGuardrailPolicy(r.Context(), db.UpsertWorkspaceGuardrailPolicyParams{
		WorkspaceID:              ws.ID,
		Enabled:                  req.Enabled,
		FailureMode:              failureMode,
		SecretsEnabled:           req.Secrets.Enabled,
		SecretsDirection:         secDir,
		SecretsAction:            secAct,
		PiiEnabled:               req.Pii.Enabled,
		PiiDirection:             piiDir,
		PiiAction:                piiAct,
		PiiEntities:              piiEntities,
		PromptInjectionEnabled:   req.PromptInjection.Enabled,
		PromptInjectionDirection: injDir,
		PromptInjectionAction:    injAct,
	})
	if err != nil {
		s.logger.Error("upsert workspace guardrail policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail policy")
		return
	}

	wsID := fromPGUUID(ws.ID)
	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "guardrails.policy_updated", "workspace", &wsID, map[string]any{
		"enabled":     row.Enabled,
		"failureMode": row.FailureMode,
		"revision":    row.Revision,
	})
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit update guardrail policy", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail policy")
		return
	}
	s.invalidateGuardrails(r.Context(), wsID)
	writeJSON(w, s.logger, http.StatusOK, toAPIGuardrailPolicy(guardrails.PolicyFromDB(row)))
}

func (s *server) handleCreateWorkspaceGuardrailRule(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGuardrails)
	if !ok {
		return
	}
	var req apigen.CreateGuardrailRuleRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	slug, err := guardrails.ValidateSlug(req.Slug)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	name, err := guardrails.ValidateName(req.Name)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	mode, err := guardrails.ValidateMatchMode(string(req.MatchMode))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	pattern, _, _, err := guardrails.ValidatePattern(mode, req.Pattern)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "guardrail.invalid_pattern", err.Error())
		return
	}
	direction, err := guardrails.ValidateDirection(string(req.Direction))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	action, err := guardrails.ValidateAction(string(req.Action))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	rawTok := ""
	if req.ReplacementToken != nil {
		rawTok = *req.ReplacementToken
	}
	replacementToken, err := guardrails.ValidateReplacementToken(rawTok)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin create guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create guardrail rule")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	count, err := q.CountWorkspaceGuardrailRules(r.Context(), ws.ID)
	if err != nil {
		s.logger.Error("count workspace guardrail rules", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create guardrail rule")
		return
	}
	if count >= guardrails.MaxCustomRules {
		writeError(w, s.logger, http.StatusConflict, "guardrail.rule_limit", "workspace guardrail rule limit reached")
		return
	}

	ruleID := uuid.New()
	row, err := q.CreateWorkspaceGuardrailRule(r.Context(), db.CreateWorkspaceGuardrailRuleParams{
		ID:               toPGUUID(ruleID),
		WorkspaceID:      ws.ID,
		Slug:             slug,
		Name:             name,
		MatchMode:        mode,
		Pattern:          pattern,
		Direction:        direction,
		Action:           action,
		ReplacementToken: replacementToken,
		Enabled:          req.Enabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "guardrail.rule_conflict", "a guardrail rule with this slug already exists")
			return
		}
		s.logger.Error("create guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create guardrail rule")
		return
	}

	wsID := fromPGUUID(ws.ID)
	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "guardrails.rule_created", "guardrail_rule", &ruleID, map[string]any{
		"slug":      row.Slug,
		"matchMode": row.MatchMode,
		"direction": row.Direction,
		"action":    row.Action,
	})
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit create guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create guardrail rule")
		return
	}
	s.invalidateGuardrails(r.Context(), wsID)
	writeJSON(w, s.logger, http.StatusCreated, toAPIGuardrailRule(guardrails.RuleFromDB(row)))
}

func (s *server) handleUpdateWorkspaceGuardrailRule(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGuardrails)
	if !ok {
		return
	}
	ruleID, err := uuid.Parse(chi.URLParam(r, "ruleID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid guardrail rule identifier")
		return
	}
	var req apigen.UpdateGuardrailRuleRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	existing, err := s.queries.GetWorkspaceGuardrailRule(r.Context(), db.GetWorkspaceGuardrailRuleParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(ruleID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "guardrail.rule_not_found", "guardrail rule not found")
		return
	}
	if err != nil {
		s.logger.Error("load guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail rule")
		return
	}

	name := existing.Name
	if req.Name != nil {
		name, err = guardrails.ValidateName(*req.Name)
		if err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	mode := existing.MatchMode
	if req.MatchMode != nil {
		mode, err = guardrails.ValidateMatchMode(string(*req.MatchMode))
		if err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	rawPattern := existing.Pattern
	if req.Pattern != nil {
		rawPattern = *req.Pattern
	}
	pattern, _, _, err := guardrails.ValidatePattern(mode, rawPattern)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "guardrail.invalid_pattern", err.Error())
		return
	}
	direction := existing.Direction
	if req.Direction != nil {
		direction, err = guardrails.ValidateDirection(string(*req.Direction))
		if err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	action := existing.Action
	if req.Action != nil {
		action, err = guardrails.ValidateAction(string(*req.Action))
		if err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	replacementToken := existing.ReplacementToken
	if req.ReplacementToken != nil {
		replacementToken, err = guardrails.ValidateReplacementToken(*req.ReplacementToken)
		if err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail rule")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	updated, err := q.UpdateWorkspaceGuardrailRule(r.Context(), db.UpdateWorkspaceGuardrailRuleParams{
		WorkspaceID:      ws.ID,
		ID:               existing.ID,
		Name:             name,
		MatchMode:        mode,
		Pattern:          pattern,
		Direction:        direction,
		Action:           action,
		ReplacementToken: replacementToken,
		Enabled:          enabled,
	})
	if err != nil {
		s.logger.Error("update guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail rule")
		return
	}

	wsID := fromPGUUID(ws.ID)
	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "guardrails.rule_updated", "guardrail_rule", &ruleID, map[string]any{
		"slug":      updated.Slug,
		"enabled":   updated.Enabled,
		"direction": updated.Direction,
		"action":    updated.Action,
	})
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit update guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update guardrail rule")
		return
	}
	s.invalidateGuardrails(r.Context(), wsID)
	writeJSON(w, s.logger, http.StatusOK, toAPIGuardrailRule(guardrails.RuleFromDB(updated)))
}

func (s *server) handleDeleteWorkspaceGuardrailRule(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGuardrails)
	if !ok {
		return
	}
	ruleID, err := uuid.Parse(chi.URLParam(r, "ruleID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid guardrail rule identifier")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin delete guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete guardrail rule")
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)

	deleted, err := q.DeleteWorkspaceGuardrailRule(r.Context(), db.DeleteWorkspaceGuardrailRuleParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(ruleID),
	})
	if err != nil {
		s.logger.Error("delete guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete guardrail rule")
		return
	}
	if deleted == 0 {
		writeError(w, s.logger, http.StatusNotFound, "guardrail.rule_not_found", "guardrail rule not found")
		return
	}

	wsID := fromPGUUID(ws.ID)
	_ = recordAudit(r.Context(), q, &wsID, &claims.Subject, "guardrails.rule_deleted", "guardrail_rule", &ruleID, nil)
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit delete guardrail rule", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete guardrail rule")
		return
	}
	s.invalidateGuardrails(r.Context(), wsID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleTestWorkspaceGuardrailRule(w http.ResponseWriter, r *http.Request) {
	_, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGuardrails)
	if !ok {
		return
	}
	var req apigen.TestGuardrailRuleRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if _, err := guardrails.ValidateMatchMode(string(req.MatchMode)); err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if strings.TrimSpace(req.Pattern) == "" || len(req.Pattern) > guardrails.MaxPatternLen {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "pattern must be 1..2000 characters")
		return
	}
	if len(req.SampleText) > 8000 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "sampleText exceeds 8000 characters")
		return
	}
	rawTok := ""
	if req.ReplacementToken != nil {
		rawTok = *req.ReplacementToken
	}
	res := guardrails.TestRule(string(req.MatchMode), req.Pattern, rawTok, req.SampleText)
	out := apigen.TestGuardrailRuleResponse{
		Valid:      res.Valid,
		MatchCount: res.MatchCount,
		Preview:    res.Preview,
	}
	if res.Error != "" {
		msg := res.Error
		out.Error = &msg
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleListWorkspaceGuardrailEvents(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}

	actionFilter := strings.TrimSpace(r.URL.Query().Get("action"))
	if actionFilter != "" && !apigen.GuardrailEventAction(actionFilter).Valid() {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid guardrail event action filter")
		return
	}
	directionFilter := strings.TrimSpace(r.URL.Query().Get("direction"))
	if directionFilter != "" && directionFilter != guardrails.DirectionInput && directionFilter != guardrails.DirectionOutput {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "direction must be input or output")
		return
	}
	var botFilter pgtype.UUID
	if rawBot := strings.TrimSpace(r.URL.Query().Get("botId")); rawBot != "" {
		parsed, err := uuid.Parse(rawBot)
		if err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid botId filter")
			return
		}
		botFilter = toPGUUID(parsed)
	}
	limit := defaultGuardrailLogLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > maxGuardrailLogLimit {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	offset := 0
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "offset must be non-negative")
			return
		}
		offset = parsed
	}

	rows, err := s.queries.ListLLMGuardrailEvents(r.Context(), db.ListLLMGuardrailEventsParams{
		WorkspaceID:     ws.ID,
		ActionFilter:    actionFilter,
		DirectionFilter: directionFilter,
		BotIDFilter:     botFilter,
		PageLimit:       int32(limit),  // #nosec G115 -- validated 1..200
		PageOffset:      int32(offset), // #nosec G115 -- validated >= 0
	})
	if err != nil {
		s.logger.Error("list guardrail events", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load guardrail events")
		return
	}

	eventsOut := make([]apigen.GuardrailEvent, 0, len(rows))
	for _, row := range rows {
		slugs := row.RuleSlugs
		if slugs == nil {
			slugs = []string{}
		}
		labels := row.MatchLabels
		if labels == nil {
			labels = []string{}
		}
		ev := apigen.GuardrailEvent{
			Id:          fromPGUUID(row.ID),
			WorkspaceId: fromPGUUID(row.WorkspaceID),
			BotId:       fromPGUUID(row.BotID),
			BotName:     row.BotName,
			Provider:    row.Provider,
			Model:       row.Model,
			RoutePath:   row.RoutePath,
			Direction:   apigen.GuardrailEventDirection(row.Direction),
			Action:      apigen.GuardrailEventAction(row.Action),
			StatusCode:  int(row.StatusCode),
			LatencyMs:   int(row.LatencyMs),
			RuleSlugs:   slugs,
			MatchLabels: labels,
			MatchCount:  int(row.MatchCount),
			Summary:     row.Summary,
			CreatedAt:   row.CreatedAt.Time.UTC(),
		}
		if row.ConversationID.Valid {
			id := fromPGUUID(row.ConversationID)
			ev.ConversationId = &id
		}
		if row.TurnID.Valid {
			id := fromPGUUID(row.TurnID)
			ev.TurnId = &id
		}
		eventsOut = append(eventsOut, ev)
	}
	writeJSON(w, s.logger, http.StatusOK, apigen.GuardrailEventListResponse{Events: eventsOut})
}

func (s *server) invalidateGuardrails(ctx context.Context, workspaceID uuid.UUID) {
	if s.turnQueue == nil {
		return
	}
	pub, ok := s.turnQueue.(interface {
		PublishGuardrailsInvalidate(context.Context, uuid.UUID) error
	})
	if !ok {
		return
	}
	if err := pub.PublishGuardrailsInvalidate(ctx, workspaceID); err != nil {
		s.logger.Warn("publish guardrails invalidation", "workspace_id", workspaceID.String(), "err", err)
	}
}

func toAPIGuardrailPolicy(p guardrails.Policy) apigen.WorkspaceGuardrailPolicy {
	entities := make([]apigen.GuardrailPIIEntity, 0, len(p.PII.Entities))
	for _, e := range p.PII.Entities {
		entities = append(entities, apigen.GuardrailPIIEntity(e))
	}
	return apigen.WorkspaceGuardrailPolicy{
		WorkspaceId: p.WorkspaceID,
		Enabled:     p.Enabled,
		FailureMode: apigen.GuardrailFailureMode(p.FailureMode),
		Secrets: apigen.GuardrailDetectorConfig{
			Enabled:   p.Secrets.Enabled,
			Direction: apigen.GuardrailDirection(p.Secrets.Direction),
			Action:    apigen.GuardrailAction(p.Secrets.Action),
		},
		Pii: apigen.GuardrailPIIDetectorConfig{
			Enabled:   p.PII.Enabled,
			Direction: apigen.GuardrailDirection(p.PII.Direction),
			Action:    apigen.GuardrailAction(p.PII.Action),
			Entities:  entities,
		},
		PromptInjection: apigen.GuardrailDetectorConfig{
			Enabled:   p.PromptInjection.Enabled,
			Direction: apigen.GuardrailDirection(p.PromptInjection.Direction),
			Action:    apigen.GuardrailAction(p.PromptInjection.Action),
		},
		Revision:  p.Revision,
		UpdatedAt: p.UpdatedAt,
	}
}

func toAPIGuardrailRule(r guardrails.CustomRule) apigen.WorkspaceGuardrailRule {
	return apigen.WorkspaceGuardrailRule{
		Id:               r.ID,
		WorkspaceId:      r.WorkspaceID,
		Slug:             r.Slug,
		Name:             r.Name,
		MatchMode:        apigen.GuardrailMatchMode(r.MatchMode),
		Pattern:          r.Pattern,
		Direction:        apigen.GuardrailDirection(r.Direction),
		Action:           apigen.GuardrailAction(r.Action),
		ReplacementToken: r.ReplacementToken,
		Enabled:          r.Enabled,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}
