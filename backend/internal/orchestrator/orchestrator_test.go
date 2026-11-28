// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBroadcastMentions(t *testing.T) {
	a, b, human := uuid.New(), uuid.New(), uuid.New()
	members := []Member{{Kind: MemberBot, ID: a, Name: "Yoda"}, {Kind: MemberBot, ID: b, Name: "Mace Windu"}, {Kind: MemberUser, ID: human, Name: "Jan"}}
	for _, content := range []string{"@all discuss", "(@ALL), please", "@Yoda @all @Mace Windu @all"} {
		bots, humans := ParseMentions(content, members)
		if !slices.Equal(bots, []uuid.UUID{a, b}) || !slices.Equal(humans, []uuid.UUID{human}) {
			t.Fatalf("%q: bots=%v humans=%v", content, bots, humans)
		}
	}
	for _, content := range []string{"", "all", "@alliance", "@all2", "@all_name", "@allé", "mail@all.com", "é@all"} {
		bots, humans := ParseMentions(content, members)
		if len(bots)+len(humans) != 0 {
			t.Fatalf("%q unexpectedly broadcasts: %v %v", content, bots, humans)
		}
	}
	for _, policy := range []AnswerPolicy{PolicyMentionedElseLead, PolicyLeadFirst, PolicyRoundRobin} {
		t.Run(string(policy), func(t *testing.T) {
			req := Request{SenderKind: MemberUser, SenderID: human, Content: "@all", BotTalk: true, Policy: policy, LeadBotID: a, TurnLimit: 20, Members: members}
			if plan := Route(req); !slices.Equal(plan.Bots, []uuid.UUID{a, b}) {
				t.Fatalf("human broadcast=%+v", plan)
			}
			req.SenderKind, req.SenderID = MemberBot, a
			req.Content = "Please give your perspective.\nRequest input: [\"@all\"]"
			if plan := Route(req); !slices.Equal(plan.Bots, []uuid.UUID{b}) {
				t.Fatalf("lead broadcast=%+v", plan)
			}
			req.BotTalk = false
			if plan := Route(req); len(plan.Bots) != 0 {
				t.Fatalf("bot talk off=%+v", plan)
			}
			req.BotTalk = true
			for _, reason := range []PauseReason{PauseApproval, PauseBudget, PauseLoop, PauseTurnLimit} {
				req.Paused, req.PauseReason = true, reason
				if plan := Route(req); len(plan.Bots) != 0 || !plan.Pause || plan.Reason != reason {
					t.Fatalf("paused broadcast=%+v", plan)
				}
			}
			req.Paused, req.TurnsSinceHuman = false, 20
			if plan := Route(req); len(plan.Bots) != 0 || plan.Reason != PauseTurnLimit {
				t.Fatalf("limited broadcast=%+v", plan)
			}
			req.TurnsSinceHuman = 0
			req.RecentBotSpeakers = []uuid.UUID{b, a, b, a}
			if plan := Route(req); len(plan.Bots) != 0 || plan.Reason != PauseLoop {
				t.Fatalf("loop broadcast=%+v", plan)
			}
			req.RecentBotSpeakers, req.Members = nil, members[:1]
			if plan := Route(req); len(plan.Bots) != 0 {
				t.Fatalf("single bot broadcast=%+v", plan)
			}
			req.Members = nil
			if plan := Route(req); len(plan.Bots) != 0 {
				t.Fatalf("empty group broadcast=%+v", plan)
			}
		})
	}
}

func TestContextIdentifiesBotAndLead(t *testing.T) {
	self, lead := uuid.New(), uuid.New()
	text := BuildContext(ContextInput{BotID: self, LeadBotID: lead, Members: []Member{
		{Kind: MemberBot, ID: self, Name: "Mace Windu", Instructions: "Keep the system architecture understandable."}, {Kind: MemberBot, ID: lead, Name: "Yoda", Instructions: "Check GDPR compliance."},
	}})
	for _, want := range []string{"You are Mace Windu", "Group lead: Yoda", "Request input:", "@all", "Keep the system architecture understandable.", "Check GDPR compliance."} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestContextIncludesHumanRolesAndHandoffGuidance(t *testing.T) {
	botID, mayaID, liamID := uuid.New(), uuid.New(), uuid.New()
	text := BuildContext(ContextInput{
		BotID:     botID,
		LeadBotID: botID,
		Members: []Member{
			{Kind: MemberBot, ID: botID, Name: "Atlas", Instructions: "Draft release notes."},
			{Kind: MemberUser, ID: mayaID, Name: "Maya", Instructions: "Release Reviewer — Reviews draft release notes."},
			{Kind: MemberUser, ID: liamID, Name: "Liam", Instructions: "   "},
		},
	})
	for _, want := range []string{
		"- Maya (user, id=" + mayaID.String() + ")",
		"  Role and responsibilities: Release Reviewer — Reviews draft release notes.",
		"- Liam (user, id=" + liamID.String() + ")",
		"Handoff to human:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "- Liam (user, id="+liamID.String()+") \n  Role and responsibilities:") {
		t.Errorf("unexpected role line for empty human role in %s", text)
	}
}

func TestParseMentionsSameNameBotAndHuman(t *testing.T) {
	botAlex, humanAlex1, humanAlex2 := uuid.New(), uuid.New(), uuid.New()
	members := []Member{
		{Kind: MemberBot, ID: botAlex, Name: "Alex"},
		{Kind: MemberUser, ID: humanAlex1, Name: "Alex"},
		{Kind: MemberUser, ID: humanAlex2, Name: "alex"},
	}
	bots, humans := ParseMentions("@Alex please review", members)
	if !slices.Equal(bots, []uuid.UUID{botAlex}) {
		t.Fatalf("bots=%v want [%s]", bots, botAlex)
	}
	if !slices.Equal(humans, []uuid.UUID{humanAlex1, humanAlex2}) {
		t.Fatalf("humans=%v want [%s %s]", humans, humanAlex1, humanAlex2)
	}
}

func TestSplitHumanHandoffAndControlDirectives(t *testing.T) {
	u1, u2 := uuid.New(), uuid.New()

	body, ids := SplitHumanHandoffDirective("Please review this.\nHandoff to human: [\"" + u1.String() + "\", \"" + u1.String() + "\", \"" + u2.String() + "\"]")
	if body != "Please review this." || !slices.Equal(ids, []uuid.UUID{u1, u2}) {
		t.Fatalf("valid handoff: body=%q ids=%v", body, ids)
	}

	for _, bad := range []string{
		"Handoff to human: [\"" + u1.String() + "\"]",
		"Body\nHandoff to human: []",
		"Body\nHandoff to human: [\"not-a-uuid\"]",
		"Body\nHandoff to human: [\"" + uuid.Nil.String() + "\"]",
		"Body\nHandoff to human: [\"a\",\"b\",\"c\",\"d\",\"e\",\"f\",\"g\",\"h\",\"i\"]",
	} {
		if gotBody, gotIDs := SplitHumanHandoffDirective(bad); gotBody != bad || len(gotIDs) != 0 {
			t.Fatalf("expected invalid handoff %q to stay untouched, got body=%q ids=%v", bad, gotBody, gotIDs)
		}
	}

	combined := "Draft ready.\nRequest input: [\"Nova\"]\nHandoff to human: [\"" + u1.String() + "\"]"
	gotBody, names, required, handoffIDs := ExtractControlDirectives(combined)
	if gotBody != "Draft ready." || !slices.Equal(names, []string{"Nova"}) || required || !slices.Equal(handoffIDs, []uuid.UUID{u1}) {
		t.Fatalf("combined=%q names=%v req=%v ids=%v", gotBody, names, required, handoffIDs)
	}

	reversed := "Draft ready.\nHandoff to human: [\"" + u1.String() + "\"]\nRequest required input: [\"Nova\"]"
	gotBody, names, required, handoffIDs = ExtractControlDirectives(reversed)
	if gotBody != "Draft ready." || !slices.Equal(names, []string{"Nova"}) || !required || !slices.Equal(handoffIDs, []uuid.UUID{u1}) {
		t.Fatalf("reversed=%q names=%v req=%v ids=%v", gotBody, names, required, handoffIDs)
	}
}

func TestBuildContextKeepsFullRosterWhenTrimmingDescriptions(t *testing.T) {
	botID := uuid.New()
	members := []Member{
		{Kind: MemberBot, ID: botID, Name: "Atlas", Instructions: strings.Repeat("x", 4000)},
	}
	for i := range 6 {
		members = append(members, Member{
			Kind:         MemberUser,
			ID:           uuid.New(),
			Name:         "Member" + string(rune('A'+i)),
			Instructions: strings.Repeat("y", 4000),
		})
	}
	mayaID := uuid.New()
	members = append(members, Member{
		Kind:         MemberUser,
		ID:           mayaID,
		Name:         "Maya",
		Instructions: "Release Reviewer — Checks release notes.",
	})

	text := BuildContext(ContextInput{BotID: botID, LeadBotID: botID, Members: members})
	if len(text) > groupContextBytes {
		t.Fatalf("context length %d exceeds limit %d", len(text), groupContextBytes)
	}
	for _, want := range []string{
		"- Maya (user, id=" + mayaID.String() + ")",
		"Release Reviewer — Checks release notes.",
		contextOmission,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in context (len=%d)", want, len(text))
		}
	}
}

func TestBuildContextPreservesRosterIDsWithLargeGoalAndRoleLabels(t *testing.T) {
	botID := uuid.New()
	mayaID := uuid.New()
	var members []Member
	members = append(members, Member{
		Kind:         MemberBot,
		ID:           botID,
		Name:         "Atlas",
		Role:         strings.Repeat("🚀", 100),
		Instructions: strings.Repeat("b", 4000),
	})
	userIDs := make([]uuid.UUID, 0, 49)
	for i := range 48 {
		id := uuid.New()
		userIDs = append(userIDs, id)
		members = append(members, Member{
			Kind:         MemberUser,
			ID:           id,
			Name:         "User" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Role:         strings.Repeat("ä", 100),
			Instructions: strings.Repeat("d", 400),
		})
	}
	userIDs = append(userIDs, mayaID)
	members = append(members, Member{
		Kind:         MemberUser,
		ID:           mayaID,
		Name:         "Maya",
		Role:         "reviewer",
		Instructions: "Release Reviewer — Checks release notes.",
	})

	text := BuildContext(ContextInput{
		BotID:     botID,
		LeadBotID: botID,
		Goal:      strings.Repeat("g", 8192),
		Members:   members,
	})
	if len(text) > groupContextBytes {
		t.Fatalf("context length %d exceeds limit %d", len(text), groupContextBytes)
	}
	for _, id := range userIDs {
		if !strings.Contains(text, "id="+id.String()) {
			t.Fatalf("missing member id %s in context", id)
		}
	}
	for _, want := range []string{
		"- Maya (user, id=" + mayaID.String() + ")",
		"Release Reviewer — Checks release notes.",
		contextOmission,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in context (len=%d)", want, len(text))
		}
	}
}

func TestBuildContextPreservesRosterIDsWithLargeAskBeforeAndInputProgress(t *testing.T) {
	botID := uuid.New()
	mayaID := uuid.New()
	members := []Member{
		{
			Kind:         MemberBot,
			ID:           botID,
			Name:         "Atlas",
			Role:         "lead",
			Instructions: "Coordinate release checks.",
		},
	}
	userIDs := make([]uuid.UUID, 0, 49)
	for i := range 48 {
		id := uuid.New()
		userIDs = append(userIDs, id)
		members = append(members, Member{
			Kind:         MemberUser,
			ID:           id,
			Name:         "Member" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Role:         "engineer",
			Instructions: strings.Repeat("r", 200),
		})
	}
	userIDs = append(userIDs, mayaID)
	members = append(members, Member{
		Kind:         MemberUser,
		ID:           mayaID,
		Name:         "Maya",
		Role:         "reviewer",
		Instructions: "Release Reviewer — Checks release notes.",
	})

	askBefore := make([]AskRule, 20)
	for i := range askBefore {
		askBefore[i] = AskRule{
			Kind:  "custom",
			Label: strings.Repeat("🚀", 200),
		}
	}

	var progress strings.Builder
	progress.WriteString("You have an open group input request. It will resume automatically when collection finishes.\n")
	for i := range 8 {
		progress.WriteString("- SpecialistBot")
		progress.WriteByte(byte('1' + i))
		progress.WriteString(": waiting\n")
	}

	text := BuildContext(ContextInput{
		BotID:         botID,
		LeadBotID:     botID,
		Goal:          strings.Repeat("g", 4096),
		InputProgress: progress.String(),
		AskBefore:     askBefore,
		Members:       members,
	})
	if len(text) > groupContextBytes {
		t.Fatalf("context length %d exceeds limit %d", len(text), groupContextBytes)
	}
	for _, id := range userIDs {
		if !strings.Contains(text, "id="+id.String()) {
			t.Fatalf("missing member id %s in context", id)
		}
	}
	for _, want := range []string{
		"- Maya (user, id=" + mayaID.String() + ")",
		"Release Reviewer — Checks release notes.",
		"Ask before:",
		"You have an open group input request.",
		contextOmission,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in context (len=%d)", want, len(text))
		}
	}
}

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
		req.Content = "What do you think?\nRequest input: [\"Lead\", \"Other\"]"
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
		req.Content = "Thoughts?\nRequest input: [\"Lead\"]"
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
	for _, want := range []string{"ship the migration", "Lead", "bot_talk=true", "deleting data", "older notes", "Ada: go", "Request input:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestInputRequestLeavesRoundRobinPositionUnchanged(t *testing.T) {
	lead := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	other := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	req := Request{
		SenderKind: MemberBot,
		SenderID:   lead,
		Content:    "What do you think?\nRequest input: [\"Lead\", \"Other\"]",
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
	if len(plan.Bots) != 1 || plan.Bots[0] != other || plan.NextRoundIndex != 0 {
		t.Fatalf("plan=%+v", plan)
	}
}
