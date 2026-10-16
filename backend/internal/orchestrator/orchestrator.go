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
	Kind MemberKind
	ID   uuid.UUID
	Name string
	Role string
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
	Goal      string
	Members   []Member
	BotTalk   bool
	TurnLimit int
	Policy    AnswerPolicy
	AskBefore []AskRule
	Summary   string
	Recent    []ContextMessage
}

// ParseMentions returns member ids named with @Name, in order of appearance.
// Matching is case-insensitive and prefers the longest display name.
func ParseMentions(content string, members []Member) (bots, humans []uuid.UUID) {
	ordered := append([]Member(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return utf8.RuneCountInString(ordered[i].Name) > utf8.RuneCountInString(ordered[j].Name)
	})
	seenBot := map[uuid.UUID]bool{}
	seenHuman := map[uuid.UUID]bool{}
	for i := 0; i < len(content); {
		if content[i] != '@' {
			i++
			continue
		}
		rest := content[i+1:]
		matched := false
		for _, member := range ordered {
			name := strings.TrimSpace(member.Name)
			if name == "" || !hasPrefixFold(rest, name) {
				continue
			}
			after := len(name)
			if after < len(rest) {
				r, _ := utf8.DecodeRuneInString(rest[after:])
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					continue
				}
			}
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
			i += 1 + len(name)
			matched = true
			break
		}
		if !matched {
			i++
		}
	}
	return bots, humans
}

// Route decides the next bot turns. A human sender clears a pause and the
// turn counter. A bot sender does not, so a limit or an approval keeps holding.
func Route(req Request) Plan {
	mentioned, humans := ParseMentions(req.Content, req.Members)
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
	if req.SenderKind == MemberBot && !req.BotTalk {
		return plan
	}
	selected, nextRR := selectBots(req, mentioned)
	plan.NextRoundIndex = nextRR
	if req.SenderKind == MemberBot && pingPong(req.RecentBotSpeakers, selected) {
		plan.Pause = true
		plan.Reason = PauseLoop
		return plan
	}
	plan.Bots = selected
	return plan
}

// BuildContext renders the group brief prepended to one bot turn.
func BuildContext(in ContextInput) string {
	var b strings.Builder
	b.WriteString("Group goal: ")
	b.WriteString(strings.TrimSpace(in.Goal))
	b.WriteString("\nRoster:\n")
	for _, member := range in.Members {
		fmt.Fprintf(&b, "- %s (%s) %s\n", member.Name, member.Kind, member.Role)
	}
	fmt.Fprintf(&b, "Rules: bot_talk=%t turn_limit=%d answer_policy=%s\n", in.BotTalk, in.TurnLimit, in.Policy)
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
	if strings.TrimSpace(in.Summary) != "" {
		b.WriteString("Earlier summary:\n")
		b.WriteString(in.Summary)
		b.WriteString("\n")
	}
	if len(in.Recent) > 0 {
		b.WriteString("Recent messages:\n")
		for _, message := range in.Recent {
			fmt.Fprintf(&b, "%s: %s\n", message.Speaker, message.Body)
		}
	}
	b.WriteString("Mention the bot you hand work to.\n")
	return b.String()
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
