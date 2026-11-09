package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Summary describes a bot's knowledge base usage and policy state.
type Summary struct {
	AutoSave         bool
	TotalCount       int64
	AlwaysCount      int64
	OnDemandCount    int64
	AlwaysTokens     int64
	AlwaysTokenLimit int64
	MaxEntries       int64
}

// ListFilter filters bot knowledge base entries.
type ListFilter struct {
	Query      string
	Mode       string
	SourceKind string
	Tag        string
	Limit      int32
	Offset     int32
}

// ListResult returns paginated entries alongside overall summary counters and available tags.
type ListResult struct {
	Summary       Summary
	Entries       []db.BotMemory
	FilteredCount int64
	Tags          []string
}

// WriteInput carries validated fields for creating a memory entry.
type WriteInput struct {
	Title   string
	Content string
	Mode    string
	Tags    []string
}

// UpdateInput carries optional fields for updating a memory entry.
type UpdateInput struct {
	Title   *string
	Content *string
	Mode    *string
	Tags    *[]string
}

// ChatMemoryMetadata is stored in messages.metadata for kind='memory' rows
// and returned cleanly by the chat history API.
type ChatMemoryMetadata struct {
	MemoryID      uuid.UUID `json:"memoryId"`
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	Mode          string    `json:"mode"`
	Tags          []string  `json:"tags"`
	TokenEstimate int32     `json:"tokenEstimate"`
	SourceLabel   string    `json:"sourceLabel"`
	Deleted       bool      `json:"deleted"`
}

// Service owns workspace- and bot-scoped knowledge base persistence.
type Service struct {
	Pool   *pgxpool.Pool
	Ring   *secrets.KeyRing
	Signal notify.Signal
	Now    func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func pgID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func pgNullableUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

func pgTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t.UTC(), Valid: true} }

// List returns filtered memories, summary counters, and distinct tags for a bot.
func (s Service) List(ctx context.Context, workspaceID, botID uuid.UUID, filter ListFilter) (ListResult, error) {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
		return ListResult{}, ErrNotFound
	}
	q := db.New(s.Pool)
	wsPG, botPG := pgID(workspaceID), pgID(botID)

	sumRow, err := q.GetBotMemorySummary(ctx, db.GetBotMemorySummaryParams{WorkspaceID: wsPG, ID: botPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return ListResult{}, ErrNotFound
	}
	if err != nil {
		return ListResult{}, fmt.Errorf("load memory summary: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	search := strings.TrimSpace(filter.Query)
	like := EscapeLike(search)

	rows, err := q.ListBotMemories(ctx, db.ListBotMemoriesParams{
		WorkspaceID:  wsPG,
		BotID:        botPG,
		ModeFilter:   filter.Mode,
		SourceFilter: filter.SourceKind,
		TagFilter:    filter.Tag,
		SearchQuery:  search,
		LikeQuery:    like,
		PageLimit:    limit,
		PageOffset:   offset,
	})
	if err != nil {
		return ListResult{}, fmt.Errorf("list memories: %w", err)
	}

	filteredCount, err := q.CountFilteredBotMemories(ctx, db.CountFilteredBotMemoriesParams{
		WorkspaceID:  wsPG,
		BotID:        botPG,
		ModeFilter:   filter.Mode,
		SourceFilter: filter.SourceKind,
		TagFilter:    filter.Tag,
		SearchQuery:  search,
		LikeQuery:    like,
	})
	if err != nil {
		return ListResult{}, fmt.Errorf("count filtered memories: %w", err)
	}

	tags, err := q.ListBotMemoryTags(ctx, db.ListBotMemoryTagsParams{WorkspaceID: wsPG, BotID: botPG})
	if err != nil {
		return ListResult{}, fmt.Errorf("list memory tags: %w", err)
	}
	if rows == nil {
		rows = []db.BotMemory{}
	}
	if tags == nil {
		tags = []string{}
	}

	return ListResult{
		Summary: Summary{
			AutoSave:         sumRow.MemoryAutoSave,
			TotalCount:       sumRow.TotalCount,
			AlwaysCount:      sumRow.AlwaysCount,
			OnDemandCount:    sumRow.OnDemandCount,
			AlwaysTokens:     sumRow.AlwaysTokens,
			AlwaysTokenLimit: AlwaysTokenLimit,
			MaxEntries:       MaxEntriesPerBot,
		},
		Entries:       rows,
		FilteredCount: filteredCount,
		Tags:          tags,
	}, nil
}

// CreateManual creates a user-authored memory entry from Bot Settings.
func (s Service) CreateManual(ctx context.Context, workspaceID, botID, actorUserID uuid.UUID, actorName string, in WriteInput) (db.BotMemory, error) {
	sourceLabel := "Manual"
	if trimmed := strings.TrimSpace(actorName); trimmed != "" {
		sourceLabel = "Manual · " + trimmed
	}
	return s.createEntry(ctx, workspaceID, botID, SourceManual, sourceLabel, uuid.Nil, uuid.Nil, actorUserID, in)
}

func (s Service) createEntry(
	ctx context.Context,
	workspaceID, botID uuid.UUID,
	sourceKind, sourceLabel string,
	conversationID, turnID, actorUserID uuid.UUID,
	in WriteInput,
) (db.BotMemory, error) {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
		return db.BotMemory{}, ErrNotFound
	}
	title, err := ValidateTitle(in.Title)
	if err != nil {
		return db.BotMemory{}, err
	}
	content, err := ValidateContent(in.Content)
	if err != nil {
		return db.BotMemory{}, err
	}
	mode, err := ValidateMode(in.Mode)
	if err != nil {
		return db.BotMemory{}, err
	}
	tags, err := NormalizeTags(in.Tags)
	if err != nil {
		return db.BotMemory{}, err
	}
	tokens := EstimateTokens(title, content, tags)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("begin create memory: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	wsPG, botPG := pgID(workspaceID), pgID(botID)

	if err := q.LockBotMemory(ctx, db.LockBotMemoryParams{Column1: wsPG, Column2: botPG}); err != nil {
		return db.BotMemory{}, fmt.Errorf("lock bot memory: %w", err)
	}
	sumRow, err := q.GetBotMemorySummary(ctx, db.GetBotMemorySummaryParams{WorkspaceID: wsPG, ID: botPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.BotMemory{}, ErrNotFound
	}
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("load memory summary: %w", err)
	}
	if sourceKind == SourceChat && !sumRow.MemoryAutoSave {
		return db.BotMemory{}, ErrAutoSaveDisabled
	}
	if sumRow.TotalCount >= MaxEntriesPerBot {
		return db.BotMemory{}, ErrLimitReached
	}
	if mode == ModeAlways && sumRow.AlwaysTokens+int64(tokens) > AlwaysTokenLimit {
		return db.BotMemory{}, ErrAlwaysBudgetExceeded
	}
	checkFields := append([]string{title, content}, tags...)
	if err := CheckZeroSecret(ctx, q, s.Ring, workspaceID, botID, checkFields...); err != nil {
		return db.BotMemory{}, err
	}

	now := s.now()
	row, err := q.CreateBotMemory(ctx, db.CreateBotMemoryParams{
		ID:                   pgID(uuid.New()),
		WorkspaceID:          wsPG,
		BotID:                botPG,
		Title:                title,
		Content:              content,
		Mode:                 mode,
		Tags:                 tags,
		SourceKind:           sourceKind,
		SourceLabel:          sourceLabel,
		SourceConversationID: pgNullableUUID(conversationID),
		SourceTurnID:         pgNullableUUID(turnID),
		CreatedByUserID:      pgNullableUUID(actorUserID),
		TokenEstimate:        tokens,
		CreatedAt:            pgTime(now),
		UpdatedAt:            pgTime(now),
	})
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("insert bot memory: %w", err)
	}

	if sourceKind == SourceChat && conversationID != uuid.Nil {
		metaBytes, marshalErr := json.Marshal(ChatMemoryMetadata{
			MemoryID:      uuid.UUID(row.ID.Bytes),
			Title:         row.Title,
			Content:       row.Content,
			Mode:          row.Mode,
			Tags:          row.Tags,
			TokenEstimate: row.TokenEstimate,
			SourceLabel:   row.SourceLabel,
			Deleted:       false,
		})
		if marshalErr != nil {
			return db.BotMemory{}, fmt.Errorf("marshal chat memory card: %w", marshalErr)
		}
		if _, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID:             pgID(uuid.New()),
			WorkspaceID:    wsPG,
			ConversationID: pgID(conversationID),
			SenderType:     "bot",
			SenderBotID:    botPG,
			Kind:           "memory",
			Content:        row.Title,
			Metadata:       metaBytes,
		}); err != nil {
			return db.BotMemory{}, fmt.Errorf("create chat memory card: %w", err)
		}
	}

	if err := recordMemoryAudit(ctx, q, workspaceID, actorUserID, "bot_memory.created", uuid.UUID(row.ID.Bytes), map[string]any{
		"bot_id":      botID.String(),
		"mode":        row.Mode,
		"source_kind": row.SourceKind,
		"tokens":      row.TokenEstimate,
	}); err != nil {
		return db.BotMemory{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return db.BotMemory{}, fmt.Errorf("commit create memory: %w", err)
	}
	if sourceKind == SourceChat && conversationID != uuid.Nil && s.Signal != nil {
		s.Signal(ctx, workspaceID, conversationID, nil)
	}
	return row, nil
}

// Update modifies an existing bot memory entry and keeps any linked chat memory cards in sync.
func (s Service) Update(ctx context.Context, workspaceID, botID, memoryID, actorUserID uuid.UUID, fromChat bool, in UpdateInput) (db.BotMemory, error) {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil || memoryID == uuid.Nil {
		return db.BotMemory{}, ErrNotFound
	}
	if in.Title == nil && in.Content == nil && in.Mode == nil && in.Tags == nil {
		return db.BotMemory{}, fmt.Errorf("%w: at least one field must be updated", ErrValidation)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("begin update memory: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	wsPG, botPG, memPG := pgID(workspaceID), pgID(botID), pgID(memoryID)

	if err := q.LockBotMemory(ctx, db.LockBotMemoryParams{Column1: wsPG, Column2: botPG}); err != nil {
		return db.BotMemory{}, fmt.Errorf("lock bot memory: %w", err)
	}
	sumRow, err := q.GetBotMemorySummary(ctx, db.GetBotMemorySummaryParams{WorkspaceID: wsPG, ID: botPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.BotMemory{}, ErrNotFound
	}
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("load memory summary: %w", err)
	}
	if fromChat && !sumRow.MemoryAutoSave {
		return db.BotMemory{}, ErrAutoSaveDisabled
	}

	existing, err := q.GetBotMemory(ctx, db.GetBotMemoryParams{WorkspaceID: wsPG, BotID: botPG, ID: memPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.BotMemory{}, ErrNotFound
	}
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("get existing memory: %w", err)
	}

	title := existing.Title
	if in.Title != nil {
		title, err = ValidateTitle(*in.Title)
		if err != nil {
			return db.BotMemory{}, err
		}
	}
	content := existing.Content
	if in.Content != nil {
		content, err = ValidateContent(*in.Content)
		if err != nil {
			return db.BotMemory{}, err
		}
	}
	mode := existing.Mode
	if in.Mode != nil {
		mode, err = ValidateMode(*in.Mode)
		if err != nil {
			return db.BotMemory{}, err
		}
	}
	tags := existing.Tags
	if in.Tags != nil {
		tags, err = NormalizeTags(*in.Tags)
		if err != nil {
			return db.BotMemory{}, err
		}
	}
	if tags == nil {
		tags = []string{}
	}

	newTokens := EstimateTokens(title, content, tags)
	alwaysDelta := int64(0)
	if existing.Mode == ModeAlways {
		alwaysDelta -= int64(existing.TokenEstimate)
	}
	if mode == ModeAlways {
		alwaysDelta += int64(newTokens)
	}
	if alwaysDelta > 0 && sumRow.AlwaysTokens+alwaysDelta > AlwaysTokenLimit {
		return db.BotMemory{}, ErrAlwaysBudgetExceeded
	}

	checkFields := append([]string{title, content}, tags...)
	if err := CheckZeroSecret(ctx, q, s.Ring, workspaceID, botID, checkFields...); err != nil {
		return db.BotMemory{}, err
	}

	updated, err := q.UpdateBotMemory(ctx, db.UpdateBotMemoryParams{
		WorkspaceID:   wsPG,
		BotID:         botPG,
		ID:            memPG,
		Title:         title,
		Content:       content,
		Mode:          mode,
		Tags:          tags,
		TokenEstimate: newTokens,
		UpdatedAt:     pgTime(s.now()),
	})
	if err != nil {
		return db.BotMemory{}, fmt.Errorf("update bot memory: %w", err)
	}

	if err := q.UpdateMemoryMessagesSnapshot(ctx, db.UpdateMemoryMessagesSnapshotParams{
		WorkspaceID:   wsPG,
		MemoryID:      memoryID.String(),
		Title:         updated.Title,
		Content:       updated.Content,
		Mode:          updated.Mode,
		Tags:          updated.Tags,
		TokenEstimate: updated.TokenEstimate,
	}); err != nil {
		return db.BotMemory{}, fmt.Errorf("sync memory chat cards: %w", err)
	}

	if err := recordMemoryAudit(ctx, q, workspaceID, actorUserID, "bot_memory.updated", memoryID, map[string]any{
		"bot_id": botID.String(),
		"mode":   updated.Mode,
		"tokens": updated.TokenEstimate,
	}); err != nil {
		return db.BotMemory{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return db.BotMemory{}, fmt.Errorf("commit update memory: %w", err)
	}
	if updated.SourceConversationID.Valid && s.Signal != nil {
		s.Signal(ctx, workspaceID, uuid.UUID(updated.SourceConversationID.Bytes), nil)
	}
	return updated, nil
}

// Delete removes one memory entry and marks any inline chat memory cards as deleted.
func (s Service) Delete(ctx context.Context, workspaceID, botID, memoryID, actorUserID uuid.UUID, fromChat bool) error {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil || memoryID == uuid.Nil {
		return ErrNotFound
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete memory: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	wsPG, botPG, memPG := pgID(workspaceID), pgID(botID), pgID(memoryID)

	if err := q.LockBotMemory(ctx, db.LockBotMemoryParams{Column1: wsPG, Column2: botPG}); err != nil {
		return fmt.Errorf("lock bot memory: %w", err)
	}
	if fromChat {
		sumRow, err := q.GetBotMemorySummary(ctx, db.GetBotMemorySummaryParams{WorkspaceID: wsPG, ID: botPG})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load memory summary: %w", err)
		}
		if !sumRow.MemoryAutoSave {
			return ErrAutoSaveDisabled
		}
	}
	existing, err := q.GetBotMemory(ctx, db.GetBotMemoryParams{WorkspaceID: wsPG, BotID: botPG, ID: memPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load memory before delete: %w", err)
	}
	deleted, err := q.DeleteBotMemory(ctx, db.DeleteBotMemoryParams{WorkspaceID: wsPG, BotID: botPG, ID: memPG})
	if err != nil {
		return fmt.Errorf("delete bot memory: %w", err)
	}
	if deleted == 0 {
		return ErrNotFound
	}
	if err := q.MarkMemoryMessagesDeleted(ctx, db.MarkMemoryMessagesDeletedParams{
		WorkspaceID: wsPG,
		MemoryID:    memoryID.String(),
	}); err != nil {
		return fmt.Errorf("mark memory chat cards deleted: %w", err)
	}
	if err := recordMemoryAudit(ctx, q, workspaceID, actorUserID, "bot_memory.deleted", memoryID, map[string]any{
		"bot_id": botID.String(),
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete memory: %w", err)
	}
	if existing.SourceConversationID.Valid && s.Signal != nil {
		s.Signal(ctx, workspaceID, uuid.UUID(existing.SourceConversationID.Bytes), nil)
	}
	return nil
}

// Clear removes every entry from a bot's knowledge base without affecting chats or secrets.
func (s Service) Clear(ctx context.Context, workspaceID, botID, actorUserID uuid.UUID) (int64, error) {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
		return 0, ErrNotFound
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin clear memory: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	wsPG, botPG := pgID(workspaceID), pgID(botID)

	if err := q.LockBotMemory(ctx, db.LockBotMemoryParams{Column1: wsPG, Column2: botPG}); err != nil {
		return 0, fmt.Errorf("lock bot memory: %w", err)
	}
	if _, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: wsPG, ID: botPG}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("get bot: %w", err)
	}
	count, err := q.ClearBotMemories(ctx, db.ClearBotMemoriesParams{WorkspaceID: wsPG, BotID: botPG})
	if err != nil {
		return 0, fmt.Errorf("clear bot memories: %w", err)
	}
	if err := q.MarkBotMemoryMessagesDeleted(ctx, db.MarkBotMemoryMessagesDeletedParams{
		WorkspaceID: wsPG,
		SenderBotID: botPG,
	}); err != nil {
		return 0, fmt.Errorf("mark bot memory cards deleted: %w", err)
	}
	if err := recordMemoryAudit(ctx, q, workspaceID, actorUserID, "bot_memory.cleared", botID, map[string]any{
		"deleted_count": count,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit clear memory: %w", err)
	}
	return count, nil
}

// UpdatePolicy updates memory_auto_save for a bot and returns the refreshed summary.
func (s Service) UpdatePolicy(ctx context.Context, workspaceID, botID, actorUserID uuid.UUID, autoSave bool) (Summary, error) {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
		return Summary{}, ErrNotFound
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("begin update memory policy: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	wsPG, botPG := pgID(workspaceID), pgID(botID)

	if _, err := q.UpdateBotMemoryPolicy(ctx, db.UpdateBotMemoryPolicyParams{
		WorkspaceID:    wsPG,
		ID:             botPG,
		MemoryAutoSave: autoSave,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Summary{}, ErrNotFound
		}
		return Summary{}, fmt.Errorf("update memory policy: %w", err)
	}
	sumRow, err := q.GetBotMemorySummary(ctx, db.GetBotMemorySummaryParams{WorkspaceID: wsPG, ID: botPG})
	if err != nil {
		return Summary{}, fmt.Errorf("load memory summary: %w", err)
	}
	if err := recordMemoryAudit(ctx, q, workspaceID, actorUserID, "bot_memory.policy_updated", botID, map[string]any{
		"auto_save": autoSave,
	}); err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Summary{}, fmt.Errorf("commit update memory policy: %w", err)
	}
	return Summary{
		AutoSave:         sumRow.MemoryAutoSave,
		TotalCount:       sumRow.TotalCount,
		AlwaysCount:      sumRow.AlwaysCount,
		OnDemandCount:    sumRow.OnDemandCount,
		AlwaysTokens:     sumRow.AlwaysTokens,
		AlwaysTokenLimit: AlwaysTokenLimit,
		MaxEntries:       MaxEntriesPerBot,
	}, nil
}

// FormatPromptContext builds the bounded prompt block of always-in-context memories
// and tool instructions for a bot turn.
func (s Service) FormatPromptContext(ctx context.Context, workspaceID, botID uuid.UUID, turnID uuid.UUID, cli bool) (string, error) {
	if s.Pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
		return "", nil
	}
	q := db.New(s.Pool)
	wsPG, botPG := pgID(workspaceID), pgID(botID)
	sumRow, err := q.GetBotMemorySummary(ctx, db.GetBotMemorySummaryParams{WorkspaceID: wsPG, ID: botPG})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	entries, err := q.ListAlwaysBotMemories(ctx, db.ListAlwaysBotMemoriesParams{WorkspaceID: wsPG, BotID: botPG})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Knowledge base & memory:\n")
	if cli {
		fmt.Fprintf(&sb, "You have persistent knowledge base tools (botbox-memory: memory_save, memory_search, memory_update, memory_delete) — pass turn_id %s on every call.\n", turnID.String())
	} else {
		sb.WriteString("You have persistent knowledge base tools (memory_save, memory_search, memory_update, memory_delete).\n")
	}
	if sumRow.MemoryAutoSave {
		sb.WriteString("When the user asks you to remember a preference, convention, or fact (for example \"remember that...\" or \"merke dir...\"), store it with memory_save (never store credentials, tokens, or passwords). Use memory_search to look up on-demand memories when relevant.")
	} else {
		sb.WriteString("Auto-saving memories from chat is currently disabled by a workspace manager; you can still query existing entries with memory_search.")
	}

	if len(entries) > 0 {
		sb.WriteString("\nAlways-in-context knowledge base entries:\n")
		for _, entry := range entries {
			var line strings.Builder
			fmt.Fprintf(&line, "- [%s] %s", uuid.UUID(entry.ID.Bytes).String(), entry.Title)
			if len(entry.Tags) > 0 {
				fmt.Fprintf(&line, " (tags: %s)", strings.Join(entry.Tags, ", "))
			}
			fmt.Fprintf(&line, ": %s\n", entry.Content)
			if sb.Len()+line.Len() > MaxPromptBytes {
				sb.WriteString("- [additional always-in-context entries omitted due to prompt limit; use memory_search]\n")
				break
			}
			sb.WriteString(line.String())
		}
	}
	return sb.String(), nil
}

func recordMemoryAudit(ctx context.Context, q *db.Queries, workspaceID, actorUserID uuid.UUID, action string, targetID uuid.UUID, meta map[string]any) error {
	metaBytes := []byte("{}")
	if len(meta) > 0 {
		encoded, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		metaBytes = encoded
	}
	_, err := q.CreateAuditLog(ctx, db.CreateAuditLogParams{
		WorkspaceID: pgID(workspaceID),
		ActorUserID: pgNullableUUID(actorUserID),
		Action:      action,
		TargetType:  pgtype.Text{String: "bot_memory", Valid: true},
		TargetID:    pgNullableUUID(targetID),
		Metadata:    metaBytes,
	})
	return err
}
