// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"testing"

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
