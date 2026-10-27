// Package egresspolicy provides shared validation, evaluation, and serialized
// transactional persistence for per-bot egress allowlists, blocklists, and revisions.
package egresspolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// MaxHostsPerList caps both allowedHosts and blockedHosts per bot.
const MaxHostsPerList = 200

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

var (
	// ErrConflict indicates an optimistic-concurrency expectedRevision mismatch.
	ErrConflict = errors.New("egress policy revision conflict")
	// ErrForbiddenBlockRemoval indicates an approval tried to remove an explicit block without bot-management rights.
	ErrForbiddenBlockRemoval = errors.New("bot management permission required to remove explicit block")
	// ErrAlreadyAllowed indicates the host is already allowed under the current policy.
	ErrAlreadyAllowed = errors.New("host is already allowed")
)

// ValidationError carries a user-safe validation message for 400 responses.
type ValidationError struct {
	Message string
}

func (e ValidationError) Error() string { return e.Message }

// WildcardBlockedError reports that a host is blocked by a wildcard rule that
// cannot be silently edited or removed by one-click allow or an approval.
type WildcardBlockedError struct {
	Pattern string
}

func (e WildcardBlockedError) Error() string {
	return fmt.Sprintf("host is blocked by wildcard rule %q", e.Pattern)
}

// NormalizeAndValidateHost validates an exact DNS hostname without scheme, port, wildcard, or path.
func NormalizeAndValidateHost(raw string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || len(host) > 253 {
		return "", false
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@:* \t\r\n") || strings.HasSuffix(host, ".") {
		return "", false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if !dnsLabelPattern.MatchString(label) {
			return "", false
		}
	}
	return host, true
}

// NormalizeAndValidateHostPattern validates an exact hostname or "*.domain" wildcard pattern.
func NormalizeAndValidateHostPattern(raw string) (string, bool) {
	pattern := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(pattern, "*.") {
		rest := strings.TrimPrefix(pattern, "*.")
		host, ok := NormalizeAndValidateHost(rest)
		if !ok {
			return "", false
		}
		return "*." + host, true
	}
	return NormalizeAndValidateHost(pattern)
}

// CleanHostList validates, normalizes, and deduplicates a list of host patterns.
func CleanHostList(raw []string, listLabel string) ([]string, error) {
	if len(raw) > MaxHostsPerList {
		return nil, ValidationError{Message: fmt.Sprintf("at most %d %s are supported", MaxHostsPerList, listLabel)}
	}
	cleaned := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, entry := range raw {
		norm, ok := NormalizeAndValidateHostPattern(entry)
		if !ok {
			return nil, ValidationError{Message: fmt.Sprintf("invalid host pattern %q", entry)}
		}
		if !seen[norm] {
			seen[norm] = true
			cleaned = append(cleaned, norm)
		}
	}
	return cleaned, nil
}

// ValidateDisjointExactPatterns rejects identical normalized patterns present in both lists.
// Broader overlaps (e.g., "*.example.com" allowed and "api.example.com" blocked) remain valid.
func ValidateDisjointExactPatterns(allowed, blocked []string) error {
	if len(allowed) == 0 || len(blocked) == 0 {
		return nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, h := range allowed {
		allowedSet[mitmproxy.NormalizeHostPattern(h)] = true
	}
	for _, b := range blocked {
		norm := mitmproxy.NormalizeHostPattern(b)
		if allowedSet[norm] {
			return ValidationError{Message: fmt.Sprintf("host pattern %q cannot be in both allowedHosts and blockedHosts", norm)}
		}
	}
	return nil
}

// UpdateInput describes a serialized policy mutation for one bot.
type UpdateInput struct {
	WorkspaceID      uuid.UUID
	BotID            uuid.UUID
	ActorID          uuid.UUID
	Policy           mitmproxy.EgressPolicy
	AllowedHosts     []string
	HasBlockedHosts  bool
	BlockedHosts     []string
	ExpectedRevision int64
}

// UpdateResult is returned by transactional policy writers.
type UpdateResult struct {
	Rule    db.EgressRule
	Changed bool
}

// ApplyUpdateTx atomically validates, checks expectedRevision under a per-bot lock,
// persists the updated egress policy, audit row, and durable invalidation intent.
func ApplyUpdateTx(ctx context.Context, q *db.Queries, in UpdateInput) (UpdateResult, error) {
	if in.Policy != mitmproxy.PolicyAllowlist && in.Policy != mitmproxy.PolicyOpen {
		return UpdateResult{}, ValidationError{Message: "policy must be allowlist or open"}
	}
	if in.ExpectedRevision < 0 {
		return UpdateResult{}, ValidationError{Message: "expectedRevision must be non-negative"}
	}

	cleanedAllowed, err := CleanHostList(in.AllowedHosts, "allowed hosts")
	if err != nil {
		return UpdateResult{}, err
	}

	wsPG := toPGUUID(in.WorkspaceID)
	botPG := toPGUUID(in.BotID)

	if err := q.LockBotEgressRule(ctx, db.LockBotEgressRuleParams{
		Column1: wsPG,
		Column2: botPG,
	}); err != nil {
		return UpdateResult{}, err
	}

	cur, err := loadCurrentRule(ctx, q, wsPG, botPG)
	if err != nil {
		return UpdateResult{}, err
	}
	if cur.Revision != in.ExpectedRevision {
		return UpdateResult{}, ErrConflict
	}

	cleanedBlocked := append([]string(nil), cur.BlockedHosts...)
	if in.HasBlockedHosts {
		cleanedBlocked, err = CleanHostList(in.BlockedHosts, "blocked hosts")
		if err != nil {
			return UpdateResult{}, err
		}
	}
	if err := ValidateDisjointExactPatterns(cleanedAllowed, cleanedBlocked); err != nil {
		return UpdateResult{}, err
	}

	if cur.Found && cur.Policy == string(in.Policy) &&
		slices.Equal(cur.AllowedHosts, cleanedAllowed) &&
		slices.Equal(cur.BlockedHosts, cleanedBlocked) {
		return UpdateResult{Rule: cur.Row, Changed: false}, nil
	}

	nextRevision := cur.Revision + 1
	saved, err := persistRuleAndIntent(ctx, q, wsPG, botPG, in.ActorID, string(in.Policy), cleanedAllowed, cleanedBlocked, nextRevision, "")
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Rule: saved, Changed: true}, nil
}

// ApproveNewHostTx atomically applies an approved new_host card under the shared
// per-bot policy lock. Exact explicit blocks require bot-management rights to remove;
// wildcard blocks never silently narrow or delete and return WildcardBlockedError.
func ApproveNewHostTx(
	ctx context.Context,
	q *db.Queries,
	workspaceID, botID, actorID uuid.UUID,
	rawHost string,
	canManageBot bool,
) (UpdateResult, error) {
	host := mitmproxy.NormalizeHostPattern(rawHost)
	if host == "" {
		return UpdateResult{}, ValidationError{Message: "host is required"}
	}

	wsPG := toPGUUID(workspaceID)
	botPG := toPGUUID(botID)

	if err := q.LockBotEgressRule(ctx, db.LockBotEgressRuleParams{
		Column1: wsPG,
		Column2: botPG,
	}); err != nil {
		return UpdateResult{}, err
	}

	cur, err := loadCurrentRule(ctx, q, wsPG, botPG)
	if err != nil {
		return UpdateResult{}, err
	}

	allowed, matchedBlock := mitmproxy.EvaluateHostAccess(
		mitmproxy.EgressPolicy(cur.Policy),
		cur.AllowedHosts,
		cur.BlockedHosts,
		host,
	)

	nextAllowed := append([]string(nil), cur.AllowedHosts...)
	nextBlocked := append([]string(nil), cur.BlockedHosts...)

	if matchedBlock != "" {
		if strings.HasPrefix(matchedBlock, "*.") {
			return UpdateResult{}, WildcardBlockedError{Pattern: matchedBlock}
		}
		if !canManageBot {
			return UpdateResult{}, ErrForbiddenBlockRemoval
		}
		filtered := make([]string, 0, len(nextBlocked))
		for _, item := range nextBlocked {
			if mitmproxy.NormalizeHostPattern(item) != matchedBlock {
				filtered = append(filtered, item)
			}
		}
		nextBlocked = filtered
		if cur.Policy == string(mitmproxy.PolicyAllowlist) && !slices.Contains(nextAllowed, host) {
			nextAllowed = append(nextAllowed, host)
		}
	} else {
		if allowed {
			return UpdateResult{}, ErrAlreadyAllowed
		}
		if !slices.Contains(nextAllowed, host) {
			nextAllowed = append(nextAllowed, host)
		}
	}

	if len(nextAllowed) > MaxHostsPerList {
		return UpdateResult{}, ValidationError{Message: fmt.Sprintf("at most %d allowed hosts are supported", MaxHostsPerList)}
	}
	if err := ValidateDisjointExactPatterns(nextAllowed, nextBlocked); err != nil {
		return UpdateResult{}, err
	}

	nextRevision := cur.Revision + 1
	saved, err := persistRuleAndIntent(ctx, q, wsPG, botPG, actorID, cur.Policy, nextAllowed, nextBlocked, nextRevision, "approval")
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Rule: saved, Changed: true}, nil
}

type currentRuleState struct {
	Found        bool
	Row          db.EgressRule
	Policy       string
	AllowedHosts []string
	BlockedHosts []string
	Revision     int64
}

func loadCurrentRule(ctx context.Context, q *db.Queries, wsPG, botPG pgtype.UUID) (currentRuleState, error) {
	row, err := q.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return currentRuleState{
			Found:        false,
			Policy:       string(mitmproxy.PolicyAllowlist),
			AllowedHosts: []string{},
			BlockedHosts: []string{},
			Revision:     0,
			Row: db.EgressRule{
				BotID:        botPG,
				WorkspaceID:  wsPG,
				Policy:       string(mitmproxy.PolicyAllowlist),
				AllowedHosts: []string{},
				BlockedHosts: []string{},
				Revision:     0,
			},
		}, nil
	}
	if err != nil {
		return currentRuleState{}, err
	}
	allowed := []string{}
	if row.AllowedHosts != nil {
		allowed = row.AllowedHosts
	}
	blocked := []string{}
	if row.BlockedHosts != nil {
		blocked = row.BlockedHosts
	}
	return currentRuleState{
		Found:        true,
		Row:          row,
		Policy:       row.Policy,
		AllowedHosts: allowed,
		BlockedHosts: blocked,
		Revision:     row.Revision,
	}, nil
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func persistRuleAndIntent(
	ctx context.Context,
	q *db.Queries,
	wsPG, botPG pgtype.UUID,
	actorID uuid.UUID,
	policy string,
	allowedHosts, blockedHosts []string,
	revision int64,
	source string,
) (db.EgressRule, error) {
	allowedHosts = nonNilSlice(allowedHosts)
	blockedHosts = nonNilSlice(blockedHosts)
	saved, err := q.UpsertEgressRule(ctx, db.UpsertEgressRuleParams{
		BotID:        botPG,
		WorkspaceID:  wsPG,
		Policy:       policy,
		AllowedHosts: allowedHosts,
		BlockedHosts: blockedHosts,
		Revision:     revision,
	})
	if err != nil {
		return db.EgressRule{}, err
	}

	if err := q.UpsertEgressInvalidationIntent(ctx, db.UpsertEgressInvalidationIntentParams{
		BotID:          botPG,
		WorkspaceID:    wsPG,
		TargetRevision: revision,
	}); err != nil {
		return db.EgressRule{}, err
	}

	metaMap := map[string]any{
		"policy":        saved.Policy,
		"allowed_count": len(allowedHosts),
		"blocked_count": len(blockedHosts),
		"revision":      saved.Revision,
	}
	if source != "" {
		metaMap["source"] = source
	}
	metaJSON, err := json.Marshal(metaMap)
	if err != nil {
		return db.EgressRule{}, err
	}

	var actorPG pgtype.UUID
	if actorID != uuid.Nil {
		actorPG = toPGUUID(actorID)
	}
	if _, err := q.CreateAuditLog(ctx, db.CreateAuditLogParams{
		WorkspaceID: wsPG,
		ActorUserID: actorPG,
		Action:      "bot.egress_updated",
		TargetType:  pgtype.Text{String: "bot", Valid: true},
		TargetID:    botPG,
		Metadata:    metaJSON,
	}); err != nil {
		return db.EgressRule{}, err
	}
	return saved, nil
}

func toPGUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil}
}
