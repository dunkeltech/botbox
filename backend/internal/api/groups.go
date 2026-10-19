package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maxGroupMembers   = 50
	maxAskBeforeRules = 20
	maxAskLabelRunes  = 200
	maxGroupBudget    = 1_000_000_000_000
)

// storedAskRule is the canonical ask-before object. Label is always present so
// two encodings of the same rule compare equal, including rules with no label.
type storedAskRule struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type groupMemberSpec struct {
	kind string
	id   uuid.UUID
	role string
}

type groupSpec struct {
	name      string
	goal      string
	lead      uuid.UUID
	botTalk   bool
	turnLimit int32
	policy    string
	askBefore []byte
	budget    *int64
	members   []groupMemberSpec
}

func (s *server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	rows, err := s.queries.ListGroupsForMember(r.Context(), db.ListGroupsForMemberParams{
		WorkspaceID: ws.ID,
		MemberID:    toPGUUID(claims.Subject),
	})
	if err != nil {
		s.logger.Error("list groups", "err", err)
		s.chatFailure(w)
		return
	}
	out := make([]apigen.Group, 0, len(rows))
	for _, row := range rows {
		group, err := s.presentGroup(r.Context(), groupHeadFromList(row))
		if err != nil {
			s.logger.Error("present group", "err", err)
			s.chatFailure(w)
			return
		}
		out = append(out, group)
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionCreateBot)
	if !ok {
		return
	}
	var req apigen.CreateGroupRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.Id == uuid.Nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "group id is required")
		return
	}
	spec, ok := s.parseGroupSpec(w, r, ws.ID, claims.Subject, groupBody{
		name: req.Name, goal: req.Goal, lead: req.LeadBotId, botTalk: req.BotTalk,
		turnLimit: req.TurnLimit, policy: string(req.AnswerPolicy), ask: req.AskBefore,
		budget: req.BudgetLimitMicros, members: req.Members,
	})
	if !ok {
		return
	}
	if s.replayGroup(w, r, ws.ID, toPGUUID(req.Id), toPGUUID(claims.Subject), spec, false) {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin create group", "err", err)
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	if _, err = q.InsertGroup(r.Context(), db.InsertGroupParams{
		ID:                toPGUUID(req.Id),
		WorkspaceID:       ws.ID,
		Name:              pgtype.Text{String: spec.name, Valid: true},
		Goal:              spec.goal,
		LeadBotID:         toPGUUID(spec.lead),
		BotTalk:           spec.botTalk,
		TurnLimit:         spec.turnLimit,
		AnswerPolicy:      spec.policy,
		AskBefore:         spec.askBefore,
		BudgetLimitMicros: budgetParam(spec.budget),
	}); err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback(r.Context())
			if !s.replayGroup(w, r, ws.ID, toPGUUID(req.Id), toPGUUID(claims.Subject), spec, true) {
				writeError(w, s.logger, http.StatusConflict, "group.id_conflict", "group id already used")
			}
			return
		}
		s.writeGroupStoreError(w, err)
		return
	}
	if err = syncGroupMembers(r.Context(), q, ws.ID, toPGUUID(req.Id), spec.members); err != nil {
		s.writeGroupStoreError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit create group", "err", err)
		s.chatFailure(w)
		return
	}
	s.writeMemberGroup(w, r, ws.ID, toPGUUID(req.Id), toPGUUID(claims.Subject), http.StatusCreated)
}

func (s *server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	row, ok := s.groupForMember(w, r, false)
	if !ok {
		return
	}
	group, err := s.presentGroup(r.Context(), groupHeadFromGet(row))
	if err != nil {
		s.logger.Error("present group", "err", err)
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, group)
}

func (s *server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	row, ok := s.groupForMember(w, r, true)
	if !ok {
		return
	}
	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return
	}
	var req apigen.UpdateGroupRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	spec, ok := s.parseGroupSpec(w, r, row.WorkspaceID, claims.Subject, groupBody{
		name: req.Name, goal: req.Goal, lead: req.LeadBotId, botTalk: req.BotTalk,
		turnLimit: req.TurnLimit, policy: string(req.AnswerPolicy), ask: req.AskBefore,
		budget: req.BudgetLimitMicros, members: req.Members,
	})
	if !ok {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update group", "err", err)
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	n, err := q.UpdateGroupSettings(r.Context(), db.UpdateGroupSettingsParams{
		WorkspaceID:       row.WorkspaceID,
		ID:                row.ID,
		Name:              pgtype.Text{String: spec.name, Valid: true},
		Goal:              spec.goal,
		LeadBotID:         toPGUUID(spec.lead),
		BotTalk:           spec.botTalk,
		TurnLimit:         spec.turnLimit,
		AnswerPolicy:      spec.policy,
		AskBefore:         spec.askBefore,
		BudgetLimitMicros: budgetParam(spec.budget),
	})
	if err != nil {
		s.writeGroupStoreError(w, err)
		return
	}
	if n == 0 {
		writeError(w, s.logger, http.StatusNotFound, "group.not_found", "group not found")
		return
	}
	if err = syncGroupMembers(r.Context(), q, row.WorkspaceID, row.ID, spec.members); err != nil {
		s.writeGroupStoreError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit update group", "err", err)
		s.chatFailure(w)
		return
	}
	s.writeMemberGroup(w, r, row.WorkspaceID, row.ID, toPGUUID(claims.Subject), http.StatusOK)
}

func (s *server) handleListGroupMessages(w http.ResponseWriter, r *http.Request) {
	row, ok := s.groupForMember(w, r, false)
	if !ok {
		return
	}
	s.writeConversationHistory(w, r, row.WorkspaceID, row.ID)
}

func (s *server) handleSendGroupMessage(w http.ResponseWriter, r *http.Request) {
	// Publish before commit so a failed queue write rolls the human message back
	// instead of leaving a group post that never reaches a bot (ADR-0015).
	row, ok := s.groupForMember(w, r, true)
	if !ok {
		return
	}
	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return
	}
	var req apigen.SendChatMessageRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.Id == uuid.Nil || strings.TrimSpace(req.Content) == "" || len(req.Content) > 8192 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "message id and 1..8192 bytes of content are required")
		return
	}
	sender := toPGUUID(claims.Subject)
	if previous, found, ok := s.existingChatMessage(w, r, row.WorkspaceID, toPGUUID(req.Id)); found {
		if !ok {
			return
		}
		if previous.ConversationID != row.ID || previous.SenderUserID != sender || previous.Content != req.Content {
			writeError(w, s.logger, http.StatusConflict, "chat.id_conflict", "message id already used")
			return
		}
		writeJSON(w, s.logger, http.StatusAccepted, chatMessage(previous))
		return
	}
	if !s.chatRL.Allow(fromPGUUID(row.WorkspaceID).String() + "/" + claims.Subject.String()) {
		writeError(w, s.logger, http.StatusTooManyRequests, "chat.rate_limited", "message rate limit exceeded")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin group message", "err", err)
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	msg, err := q.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ID:             toPGUUID(req.Id),
		WorkspaceID:    row.WorkspaceID,
		ConversationID: row.ID,
		SenderType:     "user",
		SenderUserID:   sender,
		Kind:           "text",
		Content:        req.Content,
		Metadata:       []byte("{}"),
	})
	if isUniqueViolation(err) {
		_ = tx.Rollback(r.Context())
		previous, found, ok := s.existingChatMessage(w, r, row.WorkspaceID, toPGUUID(req.Id))
		if !ok || !found {
			if ok {
				writeError(w, s.logger, http.StatusConflict, "chat.id_conflict", "message id already used")
			}
			return
		}
		if previous.ConversationID != row.ID || previous.SenderUserID != sender || previous.Content != req.Content {
			writeError(w, s.logger, http.StatusConflict, "chat.id_conflict", "message id already used")
			return
		}
		writeJSON(w, s.logger, http.StatusAccepted, chatMessage(previous))
		return
	}
	if err != nil {
		s.logger.Error("store group message", "err", err)
		s.chatFailure(w)
		return
	}
	turns, err := groups.PlanHuman(r.Context(), q, row.WorkspaceID, row.ID, claims.Subject, msg)
	if errors.Is(err, groups.ErrQueueFull) {
		writeError(w, s.logger, http.StatusTooManyRequests, "chat.queue_full", "bot queue is full")
		return
	}
	if err != nil {
		s.logger.Error("route group message", "err", err)
		s.chatFailure(w)
		return
	}
	if len(turns) > 0 && s.turnQueue == nil {
		s.chatFailure(w)
		return
	}
	for _, turn := range turns {
		if err = s.turnQueue.Enqueue(r.Context(), turn.WorkspaceID, turn.BotID, turn.TurnID); err != nil {
			s.logger.Error("enqueue group turn", "err", err)
			s.chatFailure(w)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit group message", "err", err)
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, http.StatusAccepted, chatMessage(msg))
}

func (s *server) handleMarkGroupRead(w http.ResponseWriter, r *http.Request) {
	row, ok := s.groupForMember(w, r, false)
	if !ok {
		return
	}
	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return
	}
	n, err := s.queries.MarkGroupRead(r.Context(), db.MarkGroupReadParams{
		WorkspaceID:    row.WorkspaceID,
		ConversationID: row.ID,
		MemberID:       toPGUUID(claims.Subject),
	})
	if err != nil {
		s.logger.Error("mark group read", "err", err)
		s.chatFailure(w)
		return
	}
	if n == 0 {
		writeError(w, s.logger, http.StatusNotFound, "group.not_found", "group not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// groupForMember checks workspace membership, then group membership. A viewer
// who is not in the group gets 404. A viewer who is a member gets 403 on a
// mutation, and can still read.
func (s *server) groupForMember(w http.ResponseWriter, r *http.Request, mutate bool) (db.GetGroupForMemberRow, bool) {
	var zero db.GetGroupForMemberRow
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return zero, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "groupID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid group identifier")
		return zero, false
	}
	row, err := s.queries.GetGroupForMember(r.Context(), db.GetGroupForMemberParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(id),
		MemberID:    toPGUUID(claims.Subject),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "group.not_found", "group not found")
		return zero, false
	}
	if err != nil {
		s.logger.Error("load group", "err", err)
		s.chatFailure(w)
		return zero, false
	}
	if mutate && role == workspaces.RoleViewer {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot change a group")
		return zero, false
	}
	return row, true
}

type groupBody struct {
	name      string
	goal      *string
	lead      uuid.UUID
	botTalk   bool
	turnLimit int
	policy    string
	ask       []apigen.AskBeforeRule
	budget    *int64
	members   []apigen.GroupMember
}

func (s *server) parseGroupSpec(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, caller uuid.UUID, body groupBody) (groupSpec, bool) {
	var zero groupSpec
	name := strings.TrimSpace(body.name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "group name must be 1..100 characters")
		return zero, false
	}
	goal := ""
	if body.goal != nil {
		goal = *body.goal
	}
	if len(goal) > 8192 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "group goal must be at most 8192 bytes")
		return zero, false
	}
	if body.turnLimit < 1 || body.turnLimit > 100 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "turn limit must be 1..100")
		return zero, false
	}
	switch body.policy {
	case "mentioned_else_lead", "lead_first", "round_robin":
	default:
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "unknown answer policy")
		return zero, false
	}
	if body.budget != nil && (*body.budget < 1 || *body.budget > maxGroupBudget) {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "budget limit must be 1..1000000000000 microdollars")
		return zero, false
	}
	ask, err := canonicalAskBefore(body.ask)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return zero, false
	}
	members, err := normalizeGroupMembers(caller, body.members)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return zero, false
	}
	leadIsBot := false
	botCount := 0
	for _, member := range members {
		switch member.kind {
		case "user":
			_, err = s.queries.GetWorkspaceMember(r.Context(), db.GetWorkspaceMemberParams{WorkspaceID: workspaceID, UserID: toPGUUID(member.id)})
		case "bot":
			botCount++
			if member.id == body.lead {
				leadIsBot = true
			}
			_, err = s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{WorkspaceID: workspaceID, ID: toPGUUID(member.id)})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "group member is not in this workspace")
			return zero, false
		}
		if err != nil {
			s.logger.Error("validate group member", "err", err)
			s.chatFailure(w)
			return zero, false
		}
	}
	if botCount < 1 || body.lead == uuid.Nil || !leadIsBot {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "the lead must be a bot member")
		return zero, false
	}
	return groupSpec{
		name: name, goal: goal, lead: body.lead, botTalk: body.botTalk,
		turnLimit: int32(body.turnLimit), policy: body.policy, askBefore: ask,
		budget: body.budget, members: members,
	}, true
}

func canonicalAskBefore(rules []apigen.AskBeforeRule) ([]byte, error) {
	if len(rules) > maxAskBeforeRules {
		return nil, errors.New("at most 20 ask-before rules")
	}
	stored := make([]storedAskRule, 0, len(rules))
	for _, rule := range rules {
		label := ""
		if rule.Label != nil {
			label = strings.TrimSpace(*rule.Label)
		}
		if utf8.RuneCountInString(label) > maxAskLabelRunes {
			return nil, errors.New("ask-before label must be at most 200 characters")
		}
		switch string(rule.Kind) {
		case "new_host", "raise_budget", "push_protected":
		case "custom":
			if label == "" {
				return nil, errors.New("a custom ask-before rule needs a label")
			}
		default:
			return nil, errors.New("unknown ask-before kind")
		}
		stored = append(stored, storedAskRule{Kind: string(rule.Kind), Label: label})
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func normalizeGroupMembers(caller uuid.UUID, in []apigen.GroupMember) ([]groupMemberSpec, error) {
	if len(in) > maxGroupMembers {
		return nil, errors.New("a group has at most 50 members")
	}
	seen := make(map[string]struct{}, len(in)+1)
	out := make([]groupMemberSpec, 0, len(in)+1)
	callerListed := false
	for _, member := range in {
		kind := string(member.Type)
		if kind != "user" && kind != "bot" {
			return nil, errors.New("member type must be user or bot")
		}
		if member.Id == uuid.Nil {
			return nil, errors.New("member id is required")
		}
		if utf8.RuneCountInString(member.RoleText) > 100 {
			return nil, errors.New("member role must be at most 100 characters")
		}
		key := kind + ":" + member.Id.String()
		if _, ok := seen[key]; ok {
			return nil, errors.New("duplicate group member")
		}
		seen[key] = struct{}{}
		if kind == "user" && member.Id == caller {
			callerListed = true
		}
		out = append(out, groupMemberSpec{kind: kind, id: member.Id, role: member.RoleText})
	}
	if !callerListed {
		out = append(out, groupMemberSpec{kind: "user", id: caller, role: ""})
	}
	if len(out) > maxGroupMembers {
		return nil, errors.New("a group has at most 50 members")
	}
	return out, nil
}

func syncGroupMembers(ctx context.Context, q *db.Queries, workspaceID, conversationID pgtype.UUID, members []groupMemberSpec) error {
	existing, err := q.ListGroupMemberRows(ctx, db.ListGroupMemberRowsParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return err
	}
	wanted := make(map[string]struct{}, len(members))
	for _, member := range members {
		wanted[member.kind+":"+member.id.String()] = struct{}{}
		if err = q.UpsertGroupMember(ctx, db.UpsertGroupMemberParams{
			WorkspaceID:    workspaceID,
			ConversationID: conversationID,
			MemberType:     member.kind,
			MemberID:       toPGUUID(member.id),
			RoleText:       member.role,
		}); err != nil {
			return err
		}
	}
	for _, row := range existing {
		key := row.MemberType + ":" + fromPGUUID(row.MemberID).String()
		if _, ok := wanted[key]; ok {
			continue
		}
		if err = q.DeleteGroupMember(ctx, db.DeleteGroupMemberParams{
			WorkspaceID:    workspaceID,
			ConversationID: conversationID,
			MemberType:     row.MemberType,
			MemberID:       row.MemberID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// replayGroup writes 200 when this caller already stored the same create body.
// forceConflict makes a missing or different group a handled conflict instead
// of "not found", which is what a primary-key collision means.
func (s *server) replayGroup(w http.ResponseWriter, r *http.Request, workspaceID, groupID, memberID pgtype.UUID, spec groupSpec, forceConflict bool) bool {
	row, err := s.queries.GetGroupForMember(r.Context(), db.GetGroupForMemberParams{WorkspaceID: workspaceID, ID: groupID, MemberID: memberID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		s.logger.Error("load group replay", "err", err)
		s.chatFailure(w)
		return true
	}
	members, err := s.queries.ListGroupMemberRows(r.Context(), db.ListGroupMemberRowsParams{WorkspaceID: workspaceID, ConversationID: groupID})
	if err != nil {
		s.logger.Error("load group members", "err", err)
		s.chatFailure(w)
		return true
	}
	if !sameGroupSpec(row, members, spec) {
		if forceConflict {
			return false
		}
		writeError(w, s.logger, http.StatusConflict, "group.id_conflict", "group id already used")
		return true
	}
	group, err := s.presentGroup(r.Context(), groupHeadFromGet(row))
	if err != nil {
		s.logger.Error("present group", "err", err)
		s.chatFailure(w)
		return true
	}
	writeJSON(w, s.logger, http.StatusOK, group)
	return true
}

func sameGroupSpec(row db.GetGroupForMemberRow, stored []db.ListGroupMemberRowsRow, spec groupSpec) bool {
	if row.Name.String != spec.name || row.Goal != spec.goal || fromPGUUID(row.LeadBotID) != spec.lead {
		return false
	}
	if row.BotTalk != spec.botTalk || row.TurnLimit != spec.turnLimit || row.AnswerPolicy != spec.policy {
		return false
	}
	if !budgetEqual(row.BudgetLimitMicros, spec.budget) || !askEqual(row.AskBefore, spec.askBefore) {
		return false
	}
	if len(stored) != len(spec.members) {
		return false
	}
	want := make(map[string]string, len(spec.members))
	for _, member := range spec.members {
		want[member.kind+":"+member.id.String()] = member.role
	}
	for _, member := range stored {
		role, ok := want[member.MemberType+":"+fromPGUUID(member.MemberID).String()]
		if !ok || role != member.RoleText {
			return false
		}
	}
	return true
}

func askEqual(stored, canonical []byte) bool {
	left, err := normalizeAsk(stored)
	if err != nil {
		return false
	}
	right, err := normalizeAsk(canonical)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func normalizeAsk(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("[]")
	}
	var rules []storedAskRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []storedAskRule{}
	}
	return json.Marshal(rules)
}

func budgetEqual(stored pgtype.Int8, want *int64) bool {
	if !stored.Valid {
		return want == nil
	}
	return want != nil && stored.Int64 == *want
}

func budgetParam(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func (s *server) existingChatMessage(w http.ResponseWriter, r *http.Request, workspaceID, messageID pgtype.UUID) (db.Message, bool, bool) {
	msg, err := s.queries.GetTurnMessage(r.Context(), db.GetTurnMessageParams{WorkspaceID: workspaceID, ID: messageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Message{}, false, true
	}
	if err != nil {
		s.logger.Error("load message", "err", err)
		s.chatFailure(w)
		return db.Message{}, false, false
	}
	return msg, true, true
}

func (s *server) writeMemberGroup(w http.ResponseWriter, r *http.Request, workspaceID, groupID, memberID pgtype.UUID, status int) {
	row, err := s.queries.GetGroupForMember(r.Context(), db.GetGroupForMemberParams{WorkspaceID: workspaceID, ID: groupID, MemberID: memberID})
	if err != nil {
		s.logger.Error("reload group", "err", err)
		s.chatFailure(w)
		return
	}
	group, err := s.presentGroup(r.Context(), groupHeadFromGet(row))
	if err != nil {
		s.logger.Error("present group", "err", err)
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, status, group)
}

func (s *server) writeGroupStoreError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "23503" || pgErr.Code == "23505" || pgErr.Code == "P0001") {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "group settings were rejected")
		return
	}
	s.logger.Error("store group", "err", err)
	s.chatFailure(w)
}

type groupHead struct {
	id        pgtype.UUID
	workspace pgtype.UUID
	name      pgtype.Text
	goal      string
	lead      pgtype.UUID
	botTalk   bool
	turnLimit int32
	policy    string
	askBefore []byte
	budget    pgtype.Int8
	turns     int32
	paused    bool
	pause     string
	unread    int64
}

func groupHeadFromGet(row db.GetGroupForMemberRow) groupHead {
	return groupHead{
		id: row.ID, workspace: row.WorkspaceID, name: row.Name, goal: row.Goal, lead: row.LeadBotID,
		botTalk: row.BotTalk, turnLimit: row.TurnLimit, policy: row.AnswerPolicy, askBefore: row.AskBefore,
		budget: row.BudgetLimitMicros, turns: row.TurnsSinceHuman, paused: row.Paused, pause: row.PauseReason, unread: row.Unread,
	}
}

func groupHeadFromList(row db.ListGroupsForMemberRow) groupHead {
	return groupHead{
		id: row.ID, workspace: row.WorkspaceID, name: row.Name, goal: row.Goal, lead: row.LeadBotID,
		botTalk: row.BotTalk, turnLimit: row.TurnLimit, policy: row.AnswerPolicy, askBefore: row.AskBefore,
		budget: row.BudgetLimitMicros, turns: row.TurnsSinceHuman, paused: row.Paused, pause: row.PauseReason, unread: row.Unread,
	}
}

func (s *server) presentGroup(ctx context.Context, head groupHead) (apigen.Group, error) {
	rules, err := presentAskBefore(head.askBefore)
	if err != nil {
		return apigen.Group{}, err
	}
	memberRows, err := s.queries.ListGroupMemberRows(ctx, db.ListGroupMemberRowsParams{WorkspaceID: head.workspace, ConversationID: head.id})
	if err != nil {
		return apigen.Group{}, err
	}
	members := make([]apigen.GroupMember, 0, len(memberRows))
	for _, row := range memberRows {
		name := row.DisplayName
		members = append(members, apigen.GroupMember{
			Type:     apigen.GroupMemberType(row.MemberType),
			Id:       fromPGUUID(row.MemberID),
			RoleText: row.RoleText,
			Name:     &name,
		})
	}
	var budget *int64
	if head.budget.Valid {
		value := head.budget.Int64
		budget = &value
	}
	return apigen.Group{
		Id:                fromPGUUID(head.id),
		Name:              head.name.String,
		Goal:              head.goal,
		LeadBotId:         fromPGUUID(head.lead),
		BotTalk:           head.botTalk,
		TurnLimit:         int(head.turnLimit),
		AnswerPolicy:      apigen.GroupAnswerPolicy(head.policy),
		AskBefore:         rules,
		BudgetLimitMicros: budget,
		Paused:            head.paused,
		PauseReason:       head.pause,
		TurnsSinceHuman:   int(head.turns),
		Unread:            head.unread,
		Members:           members,
	}, nil
}

func presentAskBefore(raw []byte) ([]apigen.AskBeforeRule, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []apigen.AskBeforeRule{}, nil
	}
	var stored []storedAskRule
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	out := make([]apigen.AskBeforeRule, 0, len(stored))
	for _, rule := range stored {
		item := apigen.AskBeforeRule{Kind: apigen.AskBeforeRuleKind(rule.Kind)}
		if rule.Label != "" {
			label := rule.Label
			item.Label = &label
		}
		out = append(out, item)
	}
	return out, nil
}
