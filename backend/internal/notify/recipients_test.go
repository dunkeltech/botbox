// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"slices"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/orchestrator"
	"github.com/google/uuid"
)

func TestMentionRecipients(t *testing.T) {
	sam := uuid.New()
	quiet := uuid.New()
	owner := uuid.New()
	members := []person{
		{ID: sam, Notify: notifyMentions, Role: "member"},
		{ID: quiet, Notify: notifyMute, Role: "member"},
		{ID: owner, Notify: notifyAll, Role: "owner"},
	}
	got := mentionIDs([]uuid.UUID{sam, sam, quiet, owner, uuid.Nil, uuid.New()}, members)
	if len(got) != 2 || got[0] != sam || got[1] != owner {
		t.Fatalf("mentions=%v", got)
	}
}

func TestApprovalAndBudgetRecipients(t *testing.T) {
	owner := uuid.New()
	admin := uuid.New()
	sam := uuid.New()
	viewer := uuid.New()
	quiet := uuid.New()
	members := []person{
		{ID: owner, Notify: notifyMentions, Role: "owner"},
		{ID: sam, Notify: notifyMentions, Role: "member"},
		{ID: viewer, Notify: notifyAll, Role: roleViewer},
		{ID: quiet, Notify: notifyMute, Role: "member"},
	}
	deciders := []person{{ID: owner, Role: "owner"}, {ID: admin, Role: "admin"}}
	approvals := approvalIDs(members, deciders)
	if len(approvals) != 1 || approvals[0] != admin {
		t.Fatalf("approvals=%v", approvals)
	}
	budgets := budgetIDs(members)
	if len(budgets) != 1 || budgets[0] != viewer {
		t.Fatalf("budgets=%v", budgets)
	}
}

func TestResolveHumanMentionTargetsUnicodeAndBroadcast(t *testing.T) {
	sigmaUpper := uuid.New()
	sigmaFinal := uuid.New()
	alex1 := uuid.New()
	alex2 := uuid.New()
	maya := uuid.New()
	muted := uuid.New()
	botAlex := uuid.New()

	rosterMembers := []orchestrator.Member{
		{Kind: orchestrator.MemberBot, ID: botAlex, Name: "Alex"},
		{Kind: orchestrator.MemberUser, ID: sigmaUpper, Name: "Σ"},
		{Kind: orchestrator.MemberUser, ID: sigmaFinal, Name: "ς"},
		{Kind: orchestrator.MemberUser, ID: alex1, Name: "Alex"},
		{Kind: orchestrator.MemberUser, ID: alex2, Name: "Alex"},
		{Kind: orchestrator.MemberUser, ID: maya, Name: "Maya"},
		{Kind: orchestrator.MemberUser, ID: muted, Name: "Muted"},
	}
	peopleList := []person{
		{ID: sigmaUpper, Notify: notifyMentions, Role: "member"},
		{ID: sigmaFinal, Notify: notifyMentions, Role: "member"},
		{ID: alex1, Notify: notifyMentions, Role: "member"},
		{ID: alex2, Notify: notifyMentions, Role: "member"},
		{ID: maya, Notify: notifyMentions, Role: "member"},
		{ID: muted, Notify: notifyMute, Role: "member"},
	}

	// Greek sigma: Σ and ς fold equal under strings.EqualFold; handoff to Σ must not notify ς.
	sigmaTargets := resolveHumanMentionTargets("@Σ please check this", rosterMembers, []uuid.UUID{sigmaUpper})
	if got := mentionIDs(sigmaTargets, peopleList); !slices.Equal(got, []uuid.UUID{sigmaUpper}) {
		t.Fatalf("sigma targets=%v want [%s]", got, sigmaUpper)
	}

	// Individual mention + handoff: @Alex and @Maya with handoff to alex1 notifies alex1 and Maya, not alex2.
	directTargets := resolveHumanMentionTargets("@Alex and @Maya please review", rosterMembers, []uuid.UUID{alex1})
	if got := mentionIDs(directTargets, peopleList); !slices.Equal(got, []uuid.UUID{alex1, maya}) {
		t.Fatalf("direct targets=%v want [%s %s]", got, alex1, maya)
	}

	// Broadcast + handoff: @all reaches alex2 too while still honoring mute preferences.
	broadcastTargets := resolveHumanMentionTargets("@all status update, @Alex please review", rosterMembers, []uuid.UUID{alex1})
	gotBroadcast := mentionIDs(broadcastTargets, peopleList)
	if !slices.Contains(gotBroadcast, alex1) || !slices.Contains(gotBroadcast, alex2) || !slices.Contains(gotBroadcast, maya) || slices.Contains(gotBroadcast, muted) {
		t.Fatalf("broadcast targets=%v", gotBroadcast)
	}
}
