// SPDX-License-Identifier: AGPL-3.0-only

package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultCacheTTL = 15 * time.Second

type cachedWorkspacePolicy struct {
	policy        Policy
	rules         []CustomRule
	compiled      *CompiledPolicy
	loadedAt      time.Time
	secretsLoaded bool
}

// Service caches workspace guardrail policies and writes sanitized audit events
// and inline chat/tool-trace feedback when guardrails redact or block LLM calls.
type Service struct {
	Pool   *pgxpool.Pool
	Ring   *secrets.KeyRing
	Signal notify.Signal
	Logger *slog.Logger
	Now    func() time.Time

	cacheTTL time.Duration
	mu       sync.RWMutex
	cache    map[uuid.UUID]cachedWorkspacePolicy
}

// NewService constructs a workspace guardrail enforcer service.
func NewService(pool *pgxpool.Pool, ring *secrets.KeyRing, signal notify.Signal, logger *slog.Logger) *Service {
	return &Service{
		Pool:     pool,
		Ring:     ring,
		Signal:   signal,
		Logger:   logger,
		Now:      time.Now,
		cacheTTL: defaultCacheTTL,
		cache:    make(map[uuid.UUID]cachedWorkspacePolicy),
	}
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// InvalidateWorkspace drops cached policy and secret state for workspaceID.
func (s *Service) InvalidateWorkspace(workspaceID uuid.UUID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.cache, workspaceID)
	s.mu.Unlock()
}

// ForBot returns an llmproxy.GuardrailEnforcer pinned to workspaceID and botID.
func (s *Service) ForBot(workspaceID, botID uuid.UUID) *BotEnforcer {
	return &BotEnforcer{
		svc:         s,
		workspaceID: workspaceID,
		botID:       botID,
	}
}

// BotEnforcer implements llmproxy.GuardrailEnforcer for one pinned bot.
type BotEnforcer struct {
	svc         *Service
	workspaceID uuid.UUID
	botID       uuid.UUID
}

// EvaluateInput inspects an outbound LLM request payload before billing/forwarding.
func (e *BotEnforcer) EvaluateInput(ctx context.Context, provider, model, routePath string, body []byte) (llmproxy.GuardrailInputDecision, error) {
	started := e.svc.now()
	compiled, err := e.svc.loadCompiledForBot(ctx, e.workspaceID, e.botID)
	if err != nil {
		return e.handleFailureInput(ctx, provider, model, routePath, started, err)
	}
	if compiled == nil || !compiled.Policy.Enabled {
		return llmproxy.GuardrailInputDecision{Action: ActionAllowed}, nil
	}

	res, nextBody, evalErr := compiled.EvaluateJSON(DirectionInput, body)
	if evalErr != nil {
		return e.handleFailureInput(ctx, provider, model, routePath, started, evalErr)
	}

	latencyMs := int(e.svc.now().Sub(started).Milliseconds())
	if latencyMs < 0 {
		latencyMs = 0
	}

	switch res.Action {
	case ActionBlocked:
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionInput, res, http.StatusForbidden, latencyMs)
		return llmproxy.GuardrailInputDecision{
			Action:  ActionBlocked,
			Code:    "guardrail_blocked",
			Message: "LLM request blocked by workspace guardrail",
		}, nil
	case ActionRedacted:
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionInput, res, http.StatusOK, latencyMs)
		return llmproxy.GuardrailInputDecision{
			Action:  ActionRedacted,
			Body:    nextBody,
			Message: res.Summary,
		}, nil
	case ActionAudited:
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionInput, res, http.StatusOK, latencyMs)
		return llmproxy.GuardrailInputDecision{Action: ActionAudited}, nil
	default:
		e.svc.recordAllowed(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionInput, latencyMs)
		return llmproxy.GuardrailInputDecision{Action: ActionAllowed}, nil
	}
}

// EvaluateOutputJSON inspects a non-streaming JSON LLM response before returning to the bot.
func (e *BotEnforcer) EvaluateOutputJSON(ctx context.Context, provider, model, routePath string, body []byte) (llmproxy.GuardrailOutputDecision, error) {
	started := e.svc.now()
	compiled, err := e.svc.loadCompiledForBot(ctx, e.workspaceID, e.botID)
	if err != nil {
		if e.cachedFailureMode() == FailureOpen {
			return llmproxy.GuardrailOutputDecision{Action: ActionAllowed}, nil
		}
		return llmproxy.GuardrailOutputDecision{
			Action:  ActionBlocked,
			Code:    "guardrail_unavailable",
			Message: "LLM guardrail evaluation failed closed",
		}, nil
	}
	if compiled == nil || !compiled.Policy.Enabled {
		return llmproxy.GuardrailOutputDecision{Action: ActionAllowed}, nil
	}

	res, nextBody, evalErr := compiled.EvaluateJSON(DirectionOutput, body)
	if evalErr != nil {
		if compiled.Policy.FailureMode == FailureOpen {
			return llmproxy.GuardrailOutputDecision{Action: ActionAllowed}, nil
		}
		return llmproxy.GuardrailOutputDecision{
			Action:  ActionBlocked,
			Code:    "guardrail_unavailable",
			Message: "LLM guardrail evaluation failed closed",
		}, nil
	}

	latencyMs := int(e.svc.now().Sub(started).Milliseconds())
	if latencyMs < 0 {
		latencyMs = 0
	}

	switch res.Action {
	case ActionBlocked:
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, res, http.StatusForbidden, latencyMs)
		return llmproxy.GuardrailOutputDecision{
			Action:  ActionBlocked,
			Code:    "guardrail_blocked",
			Message: "LLM response blocked by workspace guardrail",
		}, nil
	case ActionRedacted:
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, res, http.StatusOK, latencyMs)
		return llmproxy.GuardrailOutputDecision{
			Action:  ActionRedacted,
			Body:    nextBody,
			Message: res.Summary,
		}, nil
	case ActionAudited:
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, res, http.StatusOK, latencyMs)
		return llmproxy.GuardrailOutputDecision{Action: ActionAudited}, nil
	default:
		e.svc.recordAllowed(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, latencyMs)
		return llmproxy.GuardrailOutputDecision{Action: ActionAllowed}, nil
	}
}

// WrapSSEOutput wraps dst with a sliding-window SSE guardrail filter.
func (e *BotEnforcer) WrapSSEOutput(ctx context.Context, provider, model, routePath string, dst io.Writer) io.WriteCloser {
	started := e.svc.now()
	compiled, err := e.svc.loadCompiledForBot(ctx, e.workspaceID, e.botID)
	if err != nil {
		if e.cachedFailureMode() == FailureOpen {
			return NewSSEFilter(nil, provider, dst, nil)
		}
		return &failingSSECloser{dst: dst, provider: provider}
	}
	if compiled == nil || !compiled.Policy.Enabled {
		return NewSSEFilter(nil, provider, dst, nil)
	}
	return NewSSEFilter(compiled, provider, dst, func(outcome SSEOutcome) {
		latencyMs := int(e.svc.now().Sub(started).Milliseconds())
		if latencyMs < 0 {
			latencyMs = 0
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		switch outcome.Result.Action {
		case ActionBlocked:
			e.svc.recordIntervention(recordCtx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, outcome.Result, http.StatusForbidden, latencyMs)
		case ActionRedacted, ActionAudited:
			e.svc.recordIntervention(recordCtx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, outcome.Result, http.StatusOK, latencyMs)
		default:
			e.svc.recordAllowed(recordCtx, e.workspaceID, e.botID, provider, model, routePath, DirectionOutput, latencyMs)
		}
	})
}

type failingSSECloser struct {
	dst      io.Writer
	provider string
}

func (f *failingSSECloser) Write(_ []byte) (int, error) {
	switch f.provider {
	case "anthropic":
		_, _ = io.WriteString(f.dst, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"guardrail_unavailable\",\"message\":\"LLM guardrail unavailable\"}}\n\n")
	case "google":
		_, _ = io.WriteString(f.dst, geminiErrorFrame("LLM guardrail unavailable"))
	default:
		_, _ = io.WriteString(f.dst, "data: {\"error\":{\"code\":\"guardrail_unavailable\",\"message\":\"LLM guardrail unavailable\"}}\n\ndata: [DONE]\n\n")
	}
	return 0, ErrStreamBlocked
}

func (f *failingSSECloser) Close() error { return nil }

func (e *BotEnforcer) cachedFailureMode() string {
	failureMode := FailureClosed
	if e == nil || e.svc == nil {
		return failureMode
	}
	e.svc.mu.RLock()
	if cached, ok := e.svc.cache[e.workspaceID]; ok && cached.policy.FailureMode != "" {
		failureMode = cached.policy.FailureMode
	}
	e.svc.mu.RUnlock()
	return failureMode
}

func (e *BotEnforcer) handleFailureInput(ctx context.Context, provider, model, routePath string, started time.Time, err error) (llmproxy.GuardrailInputDecision, error) {
	if e.svc.Logger != nil {
		e.svc.Logger.Warn("llm guardrail evaluation failed", slog.String("workspace_id", e.workspaceID.String()), slog.Any("error", err))
	}
	failureMode := e.cachedFailureMode()

	latencyMs := int(e.svc.now().Sub(started).Milliseconds())
	if latencyMs < 0 {
		latencyMs = 0
	}

	if failureMode == FailureOpen {
		e.svc.recordIntervention(ctx, e.workspaceID, e.botID, provider, model, routePath, DirectionInput, EvalResult{
			Action:      ActionAudited,
			RuleSlugs:   []string{"guardrail-evaluator"},
			MatchLabels: []string{"fail_open"},
			Summary:     "Guardrail evaluator unavailable · allowed (fail-open)",
		}, http.StatusOK, latencyMs)
		return llmproxy.GuardrailInputDecision{Action: ActionAudited}, nil
	}

	return llmproxy.GuardrailInputDecision{
		Action:  ActionBlocked,
		Code:    "guardrail_unavailable",
		Message: "LLM guardrail evaluation failed closed",
	}, nil
}

func (s *Service) loadCompiledForBot(ctx context.Context, workspaceID, botID uuid.UUID) (*CompiledPolicy, error) {
	if s == nil || s.Pool == nil || workspaceID == uuid.Nil {
		return nil, errors.New("guardrail service unconfigured")
	}
	now := s.now()
	s.mu.RLock()
	entry, ok := s.cache[workspaceID]
	s.mu.RUnlock()

	if !ok || now.Sub(entry.loadedAt) > s.cacheTTL {
		q := db.New(s.Pool)
		wsPG := pgID(workspaceID)
		policyRow, err := q.GetWorkspaceGuardrailPolicy(ctx, wsPG)
		var policy Policy
		if errors.Is(err, pgx.ErrNoRows) {
			policy = DefaultPolicy(workspaceID)
		} else if err != nil {
			return nil, err
		} else {
			policy = PolicyFromDB(policyRow)
		}

		ruleRows, err := q.ListWorkspaceGuardrailRules(ctx, wsPG)
		if err != nil {
			return nil, err
		}
		rules := make([]CustomRule, 0, len(ruleRows))
		for _, r := range ruleRows {
			rules = append(rules, RuleFromDB(r))
		}
		var storedSecrets []string
		if policy.Enabled && policy.Secrets.Enabled && s.Ring != nil {
			storedSecrets = s.collectWorkspaceSecrets(ctx, q, workspaceID)
		}
		compiled := CompilePolicy(policy, rules, storedSecrets)
		entry = cachedWorkspacePolicy{
			policy:        policy,
			rules:         rules,
			compiled:      compiled,
			loadedAt:      now,
			secretsLoaded: policy.Enabled && policy.Secrets.Enabled,
		}
		s.mu.Lock()
		s.cache[workspaceID] = entry
		s.mu.Unlock()
	}

	if !entry.policy.Enabled {
		return entry.compiled, nil
	}

	// Add bot-specific stored secrets dynamically on top of cached workspace policy.
	if entry.policy.Secrets.Enabled && s.Ring != nil && botID != uuid.Nil {
		botSecrets := s.collectBotSecrets(ctx, db.New(s.Pool), workspaceID, botID)
		if len(botSecrets) > 0 {
			combined := make([]string, 0, len(entry.compiled.StoredSecrets)+len(botSecrets))
			combined = append(combined, entry.compiled.StoredSecrets...)
			combined = append(combined, botSecrets...)
			return CompilePolicy(entry.policy, entry.rules, combined), nil
		}
	}
	return entry.compiled, nil
}

func (s *Service) collectWorkspaceSecrets(ctx context.Context, q *db.Queries, workspaceID uuid.UUID) []string {
	wsPG := pgID(workspaceID)
	var out []string
	if wsSecrets, err := q.ListWorkspaceSecretsByWorkspace(ctx, wsPG); err == nil {
		for _, row := range wsSecrets {
			plain, decErr := s.Ring.Decrypt(secrets.EnvelopeCiphertext{
				KeyID:      row.KekID,
				WrappedDEK: row.WrappedDek,
				Ciphertext: row.EncryptedValue,
			}, secrets.SecretAAD(workspaceID, "workspace", uuid.UUID(row.ID.Bytes)))
			if decErr == nil {
				for _, val := range secrets.ExtractSecretMatchValues(row.AuthKind, plain) {
					if len(val) >= minStoredSecretMatchLen {
						out = append(out, val)
					}
				}
			}
		}
	}
	if mcpServers, err := q.ListMCPServers(ctx, wsPG); err == nil {
		for _, srv := range mcpServers {
			if len(srv.EncryptedValue) == 0 || srv.KekID == "" {
				continue
			}
			plain, decErr := s.Ring.Decrypt(secrets.EnvelopeCiphertext{
				KeyID:      srv.KekID,
				WrappedDEK: srv.WrappedDek,
				Ciphertext: srv.EncryptedValue,
			}, secrets.SecretAAD(workspaceID, "mcp_server", uuid.UUID(srv.ID.Bytes)))
			if decErr == nil && len(plain) >= minStoredSecretMatchLen {
				out = append(out, string(plain))
			}
		}
	}
	return out
}

func (s *Service) collectBotSecrets(ctx context.Context, q *db.Queries, workspaceID, botID uuid.UUID) []string {
	rows, err := q.ListBotSecretsByWorkspaceAndBot(ctx, db.ListBotSecretsByWorkspaceAndBotParams{
		WorkspaceID: pgID(workspaceID),
		BotID:       pgID(botID),
	})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		plain, decErr := s.Ring.Decrypt(secrets.EnvelopeCiphertext{
			KeyID:      row.KekID,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedValue,
		}, secrets.SecretAAD(workspaceID, "bot", uuid.UUID(row.ID.Bytes)))
		if decErr == nil {
			for _, val := range secrets.ExtractSecretMatchValues(row.AuthKind, plain) {
				if len(val) >= minStoredSecretMatchLen {
					out = append(out, val)
				}
			}
		}
	}
	return out
}

func (s *Service) recordAllowed(ctx context.Context, workspaceID, botID uuid.UUID, provider, model, routePath, direction string, latencyMs int) {
	if s == nil || s.Pool == nil {
		return
	}
	q := db.New(s.Pool)
	wsPG, botPG := pgID(workspaceID), pgID(botID)
	var convPG, turnPG pgtype.UUID
	if turn, err := q.GetRunningBotTurn(ctx, db.GetRunningBotTurnParams{WorkspaceID: wsPG, BotID: botPG}); err == nil {
		convPG = turn.ConversationID
		turnPG = turn.ID
	}
	_, _ = q.InsertLLMGuardrailEvent(ctx, db.InsertLLMGuardrailEventParams{
		ID:             pgID(uuid.New()),
		WorkspaceID:    wsPG,
		BotID:          botPG,
		ConversationID: convPG,
		TurnID:         turnPG,
		Provider:       provider,
		Model:          model,
		RoutePath:      routePath,
		Direction:      direction,
		Action:         ActionAllowed,
		StatusCode:     http.StatusOK,
		LatencyMs:      safeInt32(latencyMs),
		RuleSlugs:      []string{},
		MatchLabels:    []string{},
		MatchCount:     0,
		Summary:        "",
		CreatedAt:      pgtype.Timestamptz{Time: s.now(), Valid: true},
	})
}

func (s *Service) recordIntervention(
	ctx context.Context,
	workspaceID, botID uuid.UUID,
	provider, model, routePath, direction string,
	res EvalResult,
	statusCode, latencyMs int,
) {
	if s == nil || s.Pool == nil {
		return
	}
	q := db.New(s.Pool)
	wsPG, botPG := pgID(workspaceID), pgID(botID)
	var convPG, turnPG pgtype.UUID
	var convID, turnID uuid.UUID
	if turn, err := q.GetRunningBotTurn(ctx, db.GetRunningBotTurnParams{WorkspaceID: wsPG, BotID: botPG}); err == nil {
		convPG = turn.ConversationID
		turnPG = turn.ID
		convID = uuid.UUID(turn.ConversationID.Bytes)
		turnID = uuid.UUID(turn.ID.Bytes)
	}

	slugs := res.RuleSlugs
	if slugs == nil {
		slugs = []string{}
	}
	labels := res.MatchLabels
	if labels == nil {
		labels = []string{}
	}
	summary := res.Summary
	if len(summary) > 240 {
		summary = summary[:240]
	}

	eventID := uuid.New()
	_, err := q.InsertLLMGuardrailEvent(ctx, db.InsertLLMGuardrailEventParams{
		ID:             pgID(eventID),
		WorkspaceID:    wsPG,
		BotID:          botPG,
		ConversationID: convPG,
		TurnID:         turnPG,
		Provider:       provider,
		Model:          model,
		RoutePath:      routePath,
		Direction:      direction,
		Action:         res.Action,
		StatusCode:     safeInt32(statusCode),
		LatencyMs:      safeInt32(latencyMs),
		RuleSlugs:      slugs,
		MatchLabels:    labels,
		MatchCount:     safeInt32(res.MatchCount),
		Summary:        summary,
		CreatedAt:      pgtype.Timestamptz{Time: s.now(), Valid: true},
	})
	if err != nil && s.Logger != nil {
		s.Logger.Warn("insert llm guardrail event failed", slog.Any("error", err))
	}

	// Emit inline chat card and Tool Calls trace entry for active turns on redact/block.
	if convID == uuid.Nil || (res.Action != ActionRedacted && res.Action != ActionBlocked) {
		return
	}

	meta := ChatGuardrailMetadata{
		EventID:     eventID,
		TurnID:      turnID.String(),
		Direction:   direction,
		Action:      res.Action,
		Provider:    provider,
		Model:       model,
		RoutePath:   routePath,
		StatusCode:  statusCode,
		LatencyMs:   latencyMs,
		RuleSlugs:   slugs,
		MatchLabels: labels,
		MatchCount:  res.MatchCount,
		Summary:     summary,
	}
	cardJSON, err := json.Marshal(meta)
	if err != nil {
		return
	}

	if _, msgErr := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             pgID(uuid.New()),
		WorkspaceID:    wsPG,
		ConversationID: convPG,
		SenderType:     "bot",
		SenderBotID:    botPG,
		Kind:           "guardrail",
		Content:        summary,
		Metadata:       cardJSON,
	}); msgErr != nil && s.Logger != nil {
		s.Logger.Warn("insert chat guardrail card failed", slog.Any("error", msgErr))
	}

	errorCode := ""
	if res.Action == ActionBlocked {
		errorCode = "guardrail_blocked"
	}
	toolMeta := ToolGuardrailMetadata{
		Kind:      "guardrail_event",
		TurnID:    turnID.String(),
		ToolID:    "guardrail-" + eventID.String(),
		IsError:   res.Action == ActionBlocked,
		ErrorCode: errorCode,
		Guardrail: meta,
	}
	if toolJSON, tMarshalErr := json.Marshal(toolMeta); tMarshalErr == nil {
		if _, toolErr := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID:             pgID(uuid.New()),
			WorkspaceID:    wsPG,
			ConversationID: convPG,
			SenderType:     "bot",
			SenderBotID:    botPG,
			Kind:           "tool",
			Content:        summary,
			Metadata:       toolJSON,
		}); toolErr != nil && s.Logger != nil {
			s.Logger.Warn("insert tool guardrail trace failed", slog.Any("error", toolErr))
		}
	}

	if s.Signal != nil {
		s.Signal(ctx, workspaceID, convID, nil)
	}
}

// PolicyFromDB converts a database row into a domain Policy.
func PolicyFromDB(row db.WorkspaceGuardrailPolicy) Policy {
	entities := row.PiiEntities
	if entities == nil {
		entities = []string{}
	}
	return Policy{
		WorkspaceID: uuid.UUID(row.WorkspaceID.Bytes),
		Enabled:     row.Enabled,
		FailureMode: row.FailureMode,
		Secrets: DetectorConfig{
			Enabled:   row.SecretsEnabled,
			Direction: row.SecretsDirection,
			Action:    row.SecretsAction,
		},
		PII: PIIDetectorConfig{
			Enabled:   row.PiiEnabled,
			Direction: row.PiiDirection,
			Action:    row.PiiAction,
			Entities:  entities,
		},
		PromptInjection: DetectorConfig{
			Enabled:   row.PromptInjectionEnabled,
			Direction: row.PromptInjectionDirection,
			Action:    row.PromptInjectionAction,
		},
		Revision:  row.Revision,
		UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
}

// RuleFromDB converts a database row into a domain CustomRule.
func RuleFromDB(row db.WorkspaceGuardrailRule) CustomRule {
	return CustomRule{
		ID:               uuid.UUID(row.ID.Bytes),
		WorkspaceID:      uuid.UUID(row.WorkspaceID.Bytes),
		Slug:             row.Slug,
		Name:             row.Name,
		MatchMode:        row.MatchMode,
		Pattern:          row.Pattern,
		Direction:        row.Direction,
		Action:           row.Action,
		ReplacementToken: row.ReplacementToken,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt.Time.UTC(),
		UpdatedAt:        row.UpdatedAt.Time.UTC(),
	}
}

func pgID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func safeInt32(v int) int32 {
	if v <= 0 {
		return 0
	}
	const maxI32 = int(^uint32(0) >> 1)
	if v > maxI32 {
		return int32(maxI32)
	}
	return int32(v) // #nosec G115 -- clamped to int32 max above
}
