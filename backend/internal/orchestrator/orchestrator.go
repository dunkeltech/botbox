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
	var b strings.Builder
	for _, member := range in.Members {
		if member.Kind != MemberBot {
			continue
		}
		if member.ID == in.BotID {
			fmt.Fprintf(&b, "You are %s, the bot responding in this turn.\n", member.Name)
		}
		if member.ID == in.LeadBotID {
			fmt.Fprintf(&b, "Group lead: %s.\n", member.Name)
		}
	}
	b.WriteString("Group goal: ")
	b.WriteString(strings.TrimSpace(in.Goal))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Rules: bot_talk=%t turn_limit=%d answer_policy=%s\n", in.BotTalk, in.TurnLimit, in.Policy)
	b.WriteString(in.InputProgress)
	if len(in.AskBefore) > 0 {
		b.WriteString("Ask before:")
		for _, rule := range in.AskBefore {
			label := rule.Label
			if label == "" {
				label = rule.Kind
			}
			fmt.Fprintf(&b, " %s", label)
		}
		b.WriteString("\n")
	}
	b.WriteString("Roster:\n")
	header, headerTrimmed := buildRosterHeader(b.String(), in.Members)
	return boundedContext(header, headerTrimmed, in.Summary, in.Recent)
}

type rosterEntry struct {
	baseLine string
	prefix   string
	desc     string
}

func buildRosterHeader(preamble string, members []Member) (string, bool) {
	entries := make([]rosterEntry, len(members))
	baseBytes := 0
	var withDesc []int
	for i, member := range members {
		var baseLine string
		if member.Kind == MemberUser {
			baseLine = fmt.Sprintf("- %s (user, id=%s) %s\n", member.Name, member.ID, member.Role)
		} else {
			baseLine = fmt.Sprintf("- %s (%s) %s\n", member.Name, member.Kind, member.Role)
		}
		baseBytes += len(baseLine)
		desc := strings.TrimSpace(member.Instructions)
		var prefix string
		if desc != "" {
			label := "Role and responsibilities"
			if member.Kind == MemberBot {
				label = "Global role and responsibilities"
			}
			prefix = fmt.Sprintf("  %s: ", label)
			withDesc = append(withDesc, i)
		}
		entries[i] = rosterEntry{baseLine: baseLine, prefix: prefix, desc: desc}
	}

	remaining := groupContextBytes/2 - len(preamble) - baseBytes
	renderedDesc, trimmed := fitRosterDescriptions(entries, withDesc, remaining)

	var out strings.Builder
	out.Grow(len(preamble) + baseBytes + max(remaining, 0))
	out.WriteString(preamble)
	for i, entry := range entries {
		out.WriteString(entry.baseLine)
		out.WriteString(renderedDesc[i])
	}
	return out.String(), trimmed
}

func fitRosterDescriptions(entries []rosterEntry, indices []int, remaining int) ([]string, bool) {
	rendered := make([]string, len(entries))
	if len(indices) == 0 {
		return rendered, false
	}
	if remaining <= 0 {
		return rendered, true
	}

	fullTotal := 0
	for _, idx := range indices {
		fullTotal += len(entries[idx].prefix) + len(entries[idx].desc) + 1
	}
	if fullTotal <= remaining {
		for _, idx := range indices {
			rendered[idx] = entries[idx].prefix + entries[idx].desc + "\n"
		}
		return rendered, false
	}

	// Water-fill remaining header space so short descriptions stay intact while
	// oversized descriptions are trimmed without evicting later roster members.
	pending := append([]int(nil), indices...)
	avail := remaining
	for len(pending) > 0 {
		share := avail / len(pending)
		var next []int
		progressed := false
		for _, idx := range pending {
			lineLen := len(entries[idx].prefix) + len(entries[idx].desc) + 1
			if lineLen <= share {
				rendered[idx] = entries[idx].prefix + entries[idx].desc + "\n"
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
		const ellipsis = "…"
		for pos, idx := range pending {
			budget := share
			if pos < extra {
				budget++
			}
			maxDesc := budget - len(entries[idx].prefix) - len(ellipsis) - 1
			if maxDesc <= 0 {
				continue
			}
			clipped := contextPrefix(entries[idx].desc, maxDesc)
			if clipped == "" {
				continue
			}
			rendered[idx] = entries[idx].prefix + clipped + ellipsis + "\n"
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
