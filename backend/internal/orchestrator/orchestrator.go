// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MemberKind is a conversation participant.
type MemberKind string

const (
	// MemberUser is a human workspace member.
	MemberUser MemberKind = "user"
	// MemberBot is a bot in the same workspace.
	MemberBot MemberKind = "bot"
)

// AnswerPolicy chooses which bots speak after a message.
type AnswerPolicy string

const (
	// PolicyMentionedElseLead queues mentioned bots, or the lead when nobody was mentioned.
	PolicyMentionedElseLead AnswerPolicy = "mentioned_else_lead"
	// PolicyLeadFirst queues the lead before any other mentioned bot.
	PolicyLeadFirst AnswerPolicy = "lead_first"
	// PolicyRoundRobin queues one bot in a stable rotation. Mentions do not add bots.
	PolicyRoundRobin AnswerPolicy = "round_robin"
)

// PauseReason is why a group stopped scheduling bots.
type PauseReason string

const (
	// PauseNone means the group is not paused.
	PauseNone PauseReason = ""
	// PauseTurnLimit means the human-to-human turn budget is spent.
	PauseTurnLimit PauseReason = "turn_limit"
	// PauseLoop means bots are handing the same pair back and forth.
	PauseLoop PauseReason = "loop"
	// PauseApproval means an ask-before card is waiting.
	PauseApproval PauseReason = "approval"
	// PauseBudget means the conversation cap is spent.
	PauseBudget PauseReason = "budget"
)

// Member is one roster entry. Name is the display name used for @mentions.
type Member struct {
	Kind         MemberKind
	ID           uuid.UUID
	Name         string
	Role         string
	Instructions string
}

// Request is the pure input to Route. RecentBotSpeakers is oldest first and
// includes the bot that just spoke, when the sender is a bot.
type Request struct {
	SenderKind        MemberKind
	SenderID          uuid.UUID
	Content           string
	BotTalk           bool
	Policy            AnswerPolicy
	LeadBotID         uuid.UUID
	TurnLimit         int
	TurnsSinceHuman   int
	RoundIndex        int
	Paused            bool
	PauseReason       PauseReason
	Members           []Member
	RecentBotSpeakers []uuid.UUID
}

// Plan is who to queue and whether the group must stop.
type Plan struct {
	Bots                []uuid.UUID
	Humans              []uuid.UUID
	RequiredInput       bool
	NextTurnsSinceHuman int
	NextRoundIndex      int
	Pause               bool
	Reason              PauseReason
}

// AskRule is one ask-before entry copied into the prompt.
type AskRule struct {
	Kind  string
	Label string
}

// ContextMessage is one recent line in the prompt.
type ContextMessage struct {
	Speaker string
	Body    string
}

// ContextInput is everything BuildContext may see. It has no secret field.
type ContextInput struct {
	InputProgress string
	BotID         uuid.UUID
	LeadBotID     uuid.UUID
	Goal          string
	Members       []Member
	BotTalk       bool
	TurnLimit     int
	Policy        AnswerPolicy
	AskBefore     []AskRule
	Summary       string
	Recent        []ContextMessage
}

// ParseMentions returns member ids named with @Name, in order of appearance.
// The reserved @all token expands to the roster once. Matching is case-insensitive.
func ParseMentions(content string, members []Member) (bots, humans []uuid.UUID) {
	bots, humans, _ = parseMentions(content, members)
	return bots, humans
}

// ParseMentionsWithBroadcast behaves like ParseMentions and also reports whether
// the reserved @all broadcast token appeared in content.
func ParseMentionsWithBroadcast(content string, members []Member) (bots, humans []uuid.UUID, broadcast bool) {
	return parseMentions(content, members)
}

func parseMentions(content string, members []Member) (bots, humans []uuid.UUID, broadcast bool) {
	ordered := append([]Member(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return utf8.RuneCountInString(ordered[i].Name) > utf8.RuneCountInString(ordered[j].Name)
	})
	seenBot := map[uuid.UUID]bool{}
	seenHuman := map[uuid.UUID]bool{}
	add := func(member Member) {
		switch member.Kind {
		case MemberBot:
			if !seenBot[member.ID] {
				bots = append(bots, member.ID)
				seenBot[member.ID] = true
			}
		case MemberUser:
			if !seenHuman[member.ID] {
				humans = append(humans, member.ID)
				seenHuman[member.ID] = true
			}
		}
	}
	for i := 0; i < len(content); {
		if content[i] != '@' {
			i++
			continue
		}
		rest := content[i+1:]
		if hasPrefixFold(rest, "all") && mentionBoundary(content[:i], true) && mentionBoundary(rest[3:], false) {
			broadcast = true
			for _, member := range members {
				add(member)
			}
			i += 4
			continue
		}
		var matchedName string
		for _, member := range ordered {
			name := strings.TrimSpace(member.Name)
			if name == "" {
				continue
			}
			if matchedName != "" {
				if strings.EqualFold(name, matchedName) {
					add(member)
				}
				continue
			}
			if !hasPrefixFold(rest, name) {
				continue
			}
			after := len(name)
			if after < len(rest) {
				r, _ := utf8.DecodeRuneInString(rest[after:])
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					continue
				}
			}
			matchedName = name
			add(member)
		}
		if matchedName != "" {
			i += 1 + len(matchedName)
		} else {
			i++
		}
	}
	return bots, humans, broadcast
}

func mentionBoundary(text string, before bool) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text)
	if before {
		r, _ = utf8.DecodeLastRuneInString(text)
	}
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && !unicode.IsMark(r)
}

// Route decides the next bot turns. A human sender clears a pause and the
// turn counter. A bot sender does not, so a limit or an approval keeps holding.
func Route(req Request) Plan {
	content := req.Content
	var inputNames []string
	var required bool
	if req.SenderKind == MemberBot {
		content, inputNames, required, _ = ExtractControlDirectives(content)
	}
	mentioned, humans, broadcast := parseMentions(content, req.Members)
	plan := Plan{
		Humans:              humans,
		NextTurnsSinceHuman: req.TurnsSinceHuman,
		NextRoundIndex:      req.RoundIndex,
		Reason:              req.PauseReason,
	}
	if req.SenderKind == MemberUser {
		plan.NextTurnsSinceHuman = 0
		plan.Reason = PauseNone
		req.Paused = false
		req.TurnsSinceHuman = 0
	} else if req.Paused {
		plan.Pause = true
		return plan
	}
	if req.SenderKind == MemberBot && req.TurnLimit > 0 && req.TurnsSinceHuman >= req.TurnLimit {
		plan.Pause = true
		plan.Reason = PauseTurnLimit
		return plan
	}
	if req.SenderKind == MemberBot {
		if !req.BotTalk {
			return plan
		}
		selected := inputRecipients(req, inputNames)
		if pingPong(req.RecentBotSpeakers, selected) {
			plan.Pause = true
			plan.Reason = PauseLoop
			return plan
		}
		plan.Bots = selected
		plan.RequiredInput = len(selected) > 0 && required
		return plan
	}
	selected, nextRR := selectBots(req, mentioned)
	// An explicit broadcast must reach the whole group even under round robin.
	if broadcast {
		selected, nextRR = withoutSelf(req, mentioned), req.RoundIndex
		if req.Policy == PolicyLeadFirst {
			selected = leadFirst(req, mentioned)
		}
	}
	plan.NextRoundIndex = nextRR
	plan.Bots = selected
	return plan
}

// BuildContext renders the group brief prepended to one bot turn.
func BuildContext(in ContextInput) string {
	var identity strings.Builder
	for _, member := range in.Members {
		if member.Kind != MemberBot {
			continue
		}
		if member.ID == in.BotID {
			fmt.Fprintf(&identity, "You are %s, the bot responding in this turn.\n", member.Name)
		}
		if member.ID == in.LeadBotID {
			fmt.Fprintf(&identity, "Group lead: %s.\n", member.Name)
		}
	}
	rulesLine := fmt.Sprintf("Rules: bot_talk=%t turn_limit=%d answer_policy=%s\n", in.BotTalk, in.TurnLimit, in.Policy)
	header, headerTrimmed := buildRosterHeader(identity.String(), rulesLine, in)
	return boundedContext(header, headerTrimmed, in.Summary, in.Recent)
}

type budgetItem struct {
	prefix string
	text   string
	suffix string
}

type rosterMemberLayout struct {
	basePrefix string
	roleIdx    int
	descIdx    int
}

func buildRosterHeader(identity, rulesLine string, in ContextInput) (string, bool) {
	const (
		goalPrefix = "Group goal: "
		goalSuffix = "\n"
		askPrefix  = "Ask before:"
		askSuffix  = "\n"
		rosterHead = "Roster:\n"
	)

	layouts := make([]rosterMemberLayout, len(in.Members))
	var items []budgetItem
	fixedBytes := len(identity) + len(goalPrefix) + len(goalSuffix) + len(rulesLine) + len(rosterHead)
	if len(in.AskBefore) > 0 {
		fixedBytes += len(askPrefix) + len(askSuffix)
	}

	goal := strings.TrimSpace(in.Goal)

	progressIdx := -1
	if progress := strings.TrimRight(in.InputProgress, "\r\n"); progress != "" {
		progressIdx = len(items)
		items = append(items, budgetItem{text: progress, suffix: "\n"})
	}

	askIndices := make([]int, 0, len(in.AskBefore))
	for _, rule := range in.AskBefore {
		label := rule.Label
		if label == "" {
			label = rule.Kind
		}
		if label == "" {
			continue
		}
		askIndices = append(askIndices, len(items))
		items = append(items, budgetItem{prefix: " ", text: label})
	}

	for i, member := range in.Members {
		var basePrefix string
		if member.Kind == MemberUser {
			basePrefix = fmt.Sprintf("- %s (user, id=%s)", member.Name, member.ID)
		} else {
			basePrefix = fmt.Sprintf("- %s (%s)", member.Name, member.Kind)
		}
		fixedBytes += len(basePrefix) + len(" \n")

		roleIdx := -1
		if role := member.Role; role != "" {
			roleIdx = len(items)
			items = append(items, budgetItem{text: role})
		}

		descIdx := -1
		if desc := strings.TrimSpace(member.Instructions); desc != "" {
			label := "Role and responsibilities"
			if member.Kind == MemberBot {
				label = "Global role and responsibilities"
			}
			descIdx = len(items)
			items = append(items, budgetItem{
				prefix: fmt.Sprintf("  %s: ", label),
				text:   desc,
				suffix: "\n",
			})
		}
		layouts[i] = rosterMemberLayout{
			basePrefix: basePrefix,
			roleIdx:    roleIdx,
			descIdx:    descIdx,
		}
	}

	// Reserve all member names, IDs, and fixed structural lines first, then
	// prioritize the group goal before fitting remaining variable sections
	// (open input progress, ask-before labels, group role labels, descriptions).
	remaining := groupContextBytes/2 - fixedBytes
	fittedGoal := ""
	goalTrimmed := false
	if goal != "" {
		if remaining <= 0 {
			goalTrimmed = true
		} else {
			fittedGoal = fitClippedText(goal, remaining)
			goalTrimmed = len(fittedGoal) < len(goal)
			remaining -= len(fittedGoal)
		}
	}
	renderedItems, itemsTrimmed := fitBudgetedItems(items, remaining)
	trimmed := goalTrimmed || itemsTrimmed

	var out strings.Builder
	out.Grow(min(groupContextBytes/2, fixedBytes+len(fittedGoal)+max(remaining, 0)))
	out.WriteString(identity)
	out.WriteString(goalPrefix)
	out.WriteString(fittedGoal)
	out.WriteString(goalSuffix)
	out.WriteString(rulesLine)
	if progressIdx >= 0 {
		out.WriteString(renderedItems[progressIdx])
	}
	if len(in.AskBefore) > 0 {
		out.WriteString(askPrefix)
		for _, idx := range askIndices {
			out.WriteString(renderedItems[idx])
		}
		out.WriteString(askSuffix)
	}
	out.WriteString(rosterHead)
	for _, layout := range layouts {
		out.WriteString(layout.basePrefix)
		out.WriteByte(' ')
		if layout.roleIdx >= 0 {
			out.WriteString(renderedItems[layout.roleIdx])
		}
		out.WriteByte('\n')
		if layout.descIdx >= 0 {
			out.WriteString(renderedItems[layout.descIdx])
		}
	}
	return out.String(), trimmed
}

func fitClippedText(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	const ellipsis = "…"
	if budget <= len(ellipsis) {
		return ""
	}
	clipped := contextPrefix(text, budget-len(ellipsis))
	if clipped == "" {
		return ""
	}
	return clipped + ellipsis
}

func fitBudgetedItems(items []budgetItem, remaining int) ([]string, bool) {
	rendered := make([]string, len(items))
	if len(items) == 0 {
		return rendered, false
	}
	if remaining <= 0 {
		return rendered, true
	}

	fullTotal := 0
	for _, item := range items {
		fullTotal += len(item.prefix) + len(item.text) + len(item.suffix)
	}
	if fullTotal <= remaining {
		for i, item := range items {
			rendered[i] = item.prefix + item.text + item.suffix
		}
		return rendered, false
	}

	// Water-fill remaining header space so short roles stay intact while
	// oversized group goals, role labels, or descriptions are trimmed without
	// evicting later roster members or their IDs.
	pending := make([]int, len(items))
	for i := range items {
		pending[i] = i
	}
	avail := remaining
	for len(pending) > 0 {
		share := avail / len(pending)
		var next []int
		progressed := false
		for _, idx := range pending {
			item := items[idx]
			lineLen := len(item.prefix) + len(item.text) + len(item.suffix)
			if lineLen <= share {
				rendered[idx] = item.prefix + item.text + item.suffix
				avail -= lineLen
				progressed = true
			} else {
				next = append(next, idx)
			}
		}
		if !progressed {
			pending = next
			break
		}
		pending = next
	}

	if len(pending) > 0 {
		share := avail / len(pending)
		extra := avail % len(pending)
		for pos, idx := range pending {
			budget := share
			if pos < extra {
				budget++
			}
			item := items[idx]
			maxText := budget - len(item.prefix) - len(item.suffix)
			clipped := fitClippedText(item.text, maxText)
			if clipped == "" {
				continue
			}
			rendered[idx] = item.prefix + clipped + item.suffix
		}
	}
	return rendered, true
}

func selectBots(req Request, mentioned []uuid.UUID) ([]uuid.UUID, int) {
	switch req.Policy {
	case PolicyRoundRobin:
		return roundRobin(req)
	case PolicyLeadFirst:
		return leadFirst(req, mentioned), req.RoundIndex
	default:
		return mentionedElseLead(req, mentioned), req.RoundIndex
	}
}

func mentionedElseLead(req Request, mentioned []uuid.UUID) []uuid.UUID {
	out := withoutSelf(req, mentioned)
	if len(out) > 0 {
		return out
	}
	if botMember(req.Members, req.LeadBotID) && !isSelf(req, req.LeadBotID) {
		return []uuid.UUID{req.LeadBotID}
	}
	return nil
}

func leadFirst(req Request, mentioned []uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	if botMember(req.Members, req.LeadBotID) && !isSelf(req, req.LeadBotID) {
		out = append(out, req.LeadBotID)
	}
	for _, id := range withoutSelf(req, mentioned) {
		if id == req.LeadBotID || contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

func roundRobin(req Request) ([]uuid.UUID, int) {
	ids := botMemberIDs(req.Members)
	if len(ids) == 0 {
		return nil, req.RoundIndex
	}
	start := req.RoundIndex % len(ids)
	if start < 0 {
		start = 0
	}
	for step := 0; step < len(ids); step++ {
		pos := (start + step) % len(ids)
		id := ids[pos]
		if isSelf(req, id) {
			continue
		}
		return []uuid.UUID{id}, pos + 1
	}
	return nil, req.RoundIndex
}

func withoutSelf(req Request, ids []uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	for _, id := range ids {
		if isSelf(req, id) || !botMember(req.Members, id) || contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

func isSelf(req Request, id uuid.UUID) bool {
	return req.SenderKind == MemberBot && id == req.SenderID
}

func botMember(members []Member, id uuid.UUID) bool {
	if id == uuid.Nil {
		return false
	}
	for _, member := range members {
		if member.Kind == MemberBot && member.ID == id {
			return true
		}
	}
	return false
}

func botMemberIDs(members []Member) []uuid.UUID {
	var ids []uuid.UUID
	for _, member := range members {
		if member.Kind == MemberBot {
			ids = append(ids, member.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

func contains(ids []uuid.UUID, id uuid.UUID) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

func pingPong(recent, next []uuid.UUID) bool {
	if len(next) == 0 || len(recent) < 4 {
		return false
	}
	n := len(recent)
	a, b := recent[n-4], recent[n-3]
	if a == b || recent[n-2] != a || recent[n-1] != b {
		return false
	}
	return next[0] == a
}

func hasPrefixFold(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}
