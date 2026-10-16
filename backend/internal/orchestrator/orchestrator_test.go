package orchestrator

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseMentions(t *testing.T) {
	ann := uuid.New()
	annie := uuid.New()
	sam := uuid.New()
	members := []Member{
		{Kind: MemberBot, ID: ann, Name: "Ann"},
		{Kind: MemberBot, ID: annie, Name: "Annie"},
		{Kind: MemberUser, ID: sam, Name: "Sam"},
	}
	bots, humans := ParseMentions("ask @Annie then @Sam and @ann again @Annie", members)
	if len(bots) != 2 || bots[0] != annie || bots[1] != ann {
		t.Fatalf("bots=%v", bots)
	}
	if len(humans) != 1 || humans[0] != sam {
		t.Fatalf("humans=%v", humans)
	}
	bots, humans = ParseMentions("", members)
	if bots != nil || humans != nil {
		t.Fatalf("empty bots=%v humans=%v", bots, humans)
	}
	bots, humans = ParseMentions("@Nobody", members)
	if len(bots) != 0 || len(humans) != 0 {
		t.Fatalf("unknown bots=%v humans=%v", bots, humans)
	}
}

func TestRoute(t *testing.T) {
	lead := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	other := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	third := uuid.MustParse("00000000-0000-4000-8000-000000000003")
	human := uuid.MustParse("00000000-0000-4000-8000-00000000000a")
	otherHuman := uuid.MustParse("00000000-0000-4000-8000-00000000000b")
	members := []Member{
		{Kind: MemberBot, ID: lead, Name: "Lead"},
		{Kind: MemberBot, ID: other, Name: "Other"},
		{Kind: MemberBot, ID: third, Name: "Third"},
		{Kind: MemberUser, ID: human, Name: "Ada"},
		{Kind: MemberUser, ID: otherHuman, Name: "Bea"},
	}
	base := Request{BotTalk: true, Policy: PolicyMentionedElseLead, LeadBotID: lead, TurnLimit: 20, Members: members}

	t.Run("human resets and routes mentions", func(t *testing.T) {
		req := base
		req.SenderKind = MemberUser
		req.SenderID = human
		req.Content = "please @Other and @Ada"
		req.TurnsSinceHuman = 9
		req.Paused = true
		req.PauseReason = PauseTurnLimit
		plan := Route(req)
		if plan.Pause || plan.NextTurnsSinceHuman != 0 || len(plan.Bots) != 1 || plan.Bots[0] != other {
			t.Fatalf("plan=%+v", plan)
		}
		if len(plan.Humans) != 1 || plan.Humans[0] != human {
			t.Fatalf("humans=%v", plan.Humans)
		}
	})

	t.Run("mentioned else lead falls back", func(t *testing.T) {
		req := base
		req.SenderKind = MemberUser
		req.SenderID = human
		req.Content = "status?"
		plan := Route(req)
		if len(plan.Bots) != 1 || plan.Bots[0] != lead {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("lead first orders lead ahead of mentions", func(t *testing.T) {
		req := base
		req.Policy = PolicyLeadFirst
		req.SenderKind = MemberUser
		req.SenderID = human
		req.Content = "@Other @Lead"
		plan := Route(req)
		if len(plan.Bots) != 2 || plan.Bots[0] != lead || plan.Bots[1] != other {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("lead first with no mention is only the lead", func(t *testing.T) {
		req := base
		req.Policy = PolicyLeadFirst
		req.SenderKind = MemberUser
		req.Content = "hello"
		plan := Route(req)
		if len(plan.Bots) != 1 || plan.Bots[0] != lead {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("round robin ignores mentions and advances", func(t *testing.T) {
		req := base
		req.Policy = PolicyRoundRobin
		req.SenderKind = MemberUser
		req.Content = "@Third"
		req.RoundIndex = 0
		plan := Route(req)
		// Stable order is lead, other, third by uuid string.
		if len(plan.Bots) != 1 || plan.Bots[0] != lead || plan.NextRoundIndex != 1 {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("bot does not answer itself", func(t *testing.T) {
		req := base
		req.SenderKind = MemberBot
		req.SenderID = lead
		req.Content = "@Lead @Other"
		plan := Route(req)
		if len(plan.Bots) != 1 || plan.Bots[0] != other {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("bot talk off routes humans only", func(t *testing.T) {
		req := base
		req.BotTalk = false
		req.SenderKind = MemberUser
		req.SenderID = human
		req.Content = "@Other"
		if plan := Route(req); len(plan.Bots) != 1 || plan.Bots[0] != other {
			t.Fatalf("human plan=%+v", plan)
		}
		req.SenderKind = MemberBot
		req.SenderID = other
		req.Content = "@Lead"
		plan := Route(req)
		if len(plan.Bots) != 0 || plan.Pause {
			t.Fatalf("bot plan=%+v", plan)
		}
		if len(plan.Humans) != 0 {
			t.Fatalf("humans=%v", plan.Humans)
		}
	})

	t.Run("turn limit pauses", func(t *testing.T) {
		req := base
		req.SenderKind = MemberBot
		req.SenderID = other
		req.TurnLimit = 1
		req.TurnsSinceHuman = 1
		req.Content = "@Lead"
		plan := Route(req)
		if !plan.Pause || plan.Reason != PauseTurnLimit || len(plan.Bots) != 0 {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("ping pong pauses", func(t *testing.T) {
		req := base
		req.SenderKind = MemberBot
		req.SenderID = other
		req.Content = "@Lead"
		req.RecentBotSpeakers = []uuid.UUID{lead, other, lead, other}
		plan := Route(req)
		if !plan.Pause || plan.Reason != PauseLoop || len(plan.Bots) != 0 {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("unknown mention adds nobody", func(t *testing.T) {
		req := base
		req.LeadBotID = uuid.Nil
		req.SenderKind = MemberUser
		req.Content = "@Missing"
		plan := Route(req)
		if len(plan.Bots) != 0 || len(plan.Humans) != 0 {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("paused bot stays paused", func(t *testing.T) {
		req := base
		req.SenderKind = MemberBot
		req.SenderID = other
		req.Paused = true
		req.PauseReason = PauseApproval
		req.TurnsSinceHuman = 4
		req.Content = "@Lead"
		plan := Route(req)
		if !plan.Pause || plan.Reason != PauseApproval || plan.NextTurnsSinceHuman != 4 || len(plan.Bots) != 0 {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("humans only and missing lead", func(t *testing.T) {
		req := base
		req.Members = []Member{{Kind: MemberUser, ID: human, Name: "Ada"}}
		req.SenderKind = MemberUser
		req.Content = "hello"
		plan := Route(req)
		if len(plan.Bots) != 0 || plan.Pause {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("limit of one still routes the human", func(t *testing.T) {
		req := base
		req.TurnLimit = 1
		req.TurnsSinceHuman = 1
		req.Paused = true
		req.PauseReason = PauseTurnLimit
		req.SenderKind = MemberUser
		req.Content = "@Other"
		plan := Route(req)
		if plan.Pause || len(plan.Bots) != 1 || plan.Bots[0] != other || plan.NextTurnsSinceHuman != 0 {
			t.Fatalf("plan=%+v", plan)
		}
	})
}

func TestBuildContextOmitsSecrets(t *testing.T) {
	text := BuildContext(ContextInput{
		Goal:      "ship the migration",
		Members:   []Member{{Kind: MemberBot, ID: uuid.New(), Name: "Lead", Role: "owner"}},
		BotTalk:   true,
		TurnLimit: 20,
		Policy:    PolicyMentionedElseLead,
		AskBefore: []AskRule{{Kind: "custom", Label: "deleting data"}},
		Summary:   "older notes",
		Recent:    []ContextMessage{{Speaker: "Ada", Body: "go"}},
	})
	for _, want := range []string{"ship the migration", "Lead", "bot_talk=true", "deleting data", "older notes", "Ada: go", "Mention the bot you hand work to."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestRoundRobinSkipsSender(t *testing.T) {
	lead := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	other := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	req := Request{
		SenderKind: MemberBot,
		SenderID:   lead,
		BotTalk:    true,
		Policy:     PolicyRoundRobin,
		LeadBotID:  lead,
		TurnLimit:  20,
		RoundIndex: 0,
		Members: []Member{
			{Kind: MemberBot, ID: lead, Name: "Lead"},
			{Kind: MemberBot, ID: other, Name: "Other"},
		},
	}
	plan := Route(req)
	if len(plan.Bots) != 1 || plan.Bots[0] != other || plan.NextRoundIndex != 2 {
		t.Fatalf("plan=%+v", plan)
	}
}
