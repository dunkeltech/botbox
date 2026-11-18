// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBotInputRequestsAreExplicit(t *testing.T) {
	owner, lead, peer, human := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	members := []Member{{ID: owner, Kind: MemberBot, Name: "Yoda"}, {ID: lead, Kind: MemberBot, Name: "Mace Windu"}, {ID: peer, Kind: MemberBot, Name: "Frik, Babu"}, {ID: human, Kind: MemberUser, Name: "Jan"}}
	for _, tc := range []struct {
		name, text string
		want       []uuid.UUID
		required   bool
	}{
		{name: "references only", text: "I included @Mace Windu's feedback"},
		{name: "plain reply", text: "The draft is ready"},
		{name: "quoted directive", text: "Example:\n> Request input: [\"Mace Windu\"]"},
		{name: "wrong type", text: "Thoughts?\nRequest input: [1]"},
		{name: "empty list", text: "Thoughts?\nRequest input: []"},
		{name: "missing question", text: "Request input: [\"Mace Windu\"]"},
		{name: "unknown name", text: "Thoughts?\nRequest input: [\"Mace Windu\",\"Outsider\"]"},
		{name: "human target", text: "Thoughts?\nRequest input: [\"Jan\"]"},
		{name: "too many names", text: "Thoughts?\nRequest input: [" + strings.Repeat("\"Mace Windu\",", MaxInputRecipients) + "\"Mace Windu\"]"},
		{name: "self only", text: "Thoughts?\nRequest input: [\"Yoda\"]"},
		{name: "duplicate target", text: "Thoughts?\nRequest input: [\"Frik, Babu\",\"Mace Windu\",\"mace windu\",\"Yoda\"]", want: []uuid.UUID{peer, lead}},
		{name: "broadcast", text: "Thoughts?\nRequest input: [\"@all\"]", want: []uuid.UUID{lead, peer}},
		{name: "required", text: "Thoughts?\nRequest required input: [\"Mace Windu\"]", want: []uuid.UUID{lead}, required: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, policy := range []AnswerPolicy{PolicyMentionedElseLead, PolicyLeadFirst, PolicyRoundRobin} {
				plan := Route(Request{SenderKind: MemberBot, SenderID: owner, Content: tc.text, BotTalk: true, Policy: policy, LeadBotID: lead, RoundIndex: 5, Members: members})
				if !slices.Equal(plan.Bots, tc.want) || plan.RequiredInput != tc.required || len(plan.Humans) != 0 || plan.NextRoundIndex != 5 {
					t.Fatalf("policy %s: got %+v, want bots %v and required %t", policy, plan, tc.want, tc.required)
				}
			}
		})
	}
}
