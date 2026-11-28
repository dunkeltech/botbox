// SPDX-License-Identifier: AGPL-3.0-only

package workspaces

import "testing"

func TestValidRole(t *testing.T) {
	cases := []struct {
		name string
		role string
		want bool
	}{
		{name: "owner", role: "owner", want: true},
		{name: "admin", role: "admin", want: true},
		{name: "member", role: "member", want: true},
		{name: "viewer", role: "viewer", want: true},
		{name: "empty", role: "", want: false},
		{name: "uppercase", role: "OWNER", want: false},
		{name: "unknown", role: "superuser", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidRole(tc.role); got != tc.want {
				t.Fatalf("ValidRole(%q) = %v, want %v", tc.role, got, tc.want)
			}
			parsed, ok := ParseRole(tc.role)
			if ok != tc.want {
				t.Fatalf("ParseRole(%q) ok = %v, want %v", tc.role, ok, tc.want)
			}
			if tc.want && string(parsed) != tc.role {
				t.Fatalf("ParseRole(%q) = %q, want %q", tc.role, parsed, tc.role)
			}
		})
	}
}

func TestCan(t *testing.T) {
	cases := []struct {
		name       string
		role       Role
		action     Action
		isBotOwner bool
		want       bool
	}{
		// View workspace
		{name: "owner views workspace", role: RoleOwner, action: ActionViewWorkspace, want: true},
		{name: "admin views workspace", role: RoleAdmin, action: ActionViewWorkspace, want: true},
		{name: "member views workspace", role: RoleMember, action: ActionViewWorkspace, want: true},
		{name: "viewer views workspace", role: RoleViewer, action: ActionViewWorkspace, want: true},

		// Manage workspace & security policies
		{name: "owner manages guardrails", role: RoleOwner, action: ActionManageGuardrails, want: true},
		{name: "admin manages guardrails", role: RoleAdmin, action: ActionManageGuardrails, want: true},
		{name: "member cannot manage guardrails", role: RoleMember, action: ActionManageGuardrails, want: false},
		{name: "viewer cannot manage guardrails", role: RoleViewer, action: ActionManageGuardrails, want: false},
		{name: "owner views audit logs", role: RoleOwner, action: ActionViewAuditLog, want: true},
		{name: "admin views audit logs", role: RoleAdmin, action: ActionViewAuditLog, want: true},
		{name: "member cannot view audit logs", role: RoleMember, action: ActionViewAuditLog, want: false},
		{name: "viewer cannot view audit logs", role: RoleViewer, action: ActionViewAuditLog, want: false},
		{name: "owner manages GitLab instances", role: RoleOwner, action: ActionManageGitLabInstances, want: true},
		{name: "admin manages GitLab instances", role: RoleAdmin, action: ActionManageGitLabInstances, want: true},
		{name: "member cannot manage GitLab instances", role: RoleMember, action: ActionManageGitLabInstances, want: false},
		{name: "viewer cannot manage GitLab instances", role: RoleViewer, action: ActionManageGitLabInstances, want: false},
		{name: "owner manages workspace", role: RoleOwner, action: ActionManageWorkspace, want: true},
		{name: "admin cannot manage workspace", role: RoleAdmin, action: ActionManageWorkspace, want: false},
		{name: "member cannot manage workspace", role: RoleMember, action: ActionManageWorkspace, want: false},
		{name: "viewer cannot manage workspace", role: RoleViewer, action: ActionManageWorkspace, want: false},

		// Manage members and invitations
		{name: "owner manages members", role: RoleOwner, action: ActionManageMembers, want: true},
		{name: "admin manages members", role: RoleAdmin, action: ActionManageMembers, want: true},
		{name: "member cannot manage members", role: RoleMember, action: ActionManageMembers, want: false},
		{name: "viewer cannot manage members", role: RoleViewer, action: ActionManageMembers, want: false},
		{name: "owner manages invitations", role: RoleOwner, action: ActionManageInvitations, want: true},
		{name: "admin manages invitations", role: RoleAdmin, action: ActionManageInvitations, want: true},
		{name: "member cannot manage invitations", role: RoleMember, action: ActionManageInvitations, want: false},
		{name: "viewer cannot manage invitations", role: RoleViewer, action: ActionManageInvitations, want: false},

		// Bot creation
		{name: "owner creates bot", role: RoleOwner, action: ActionCreateBot, want: true},
		{name: "admin creates bot", role: RoleAdmin, action: ActionCreateBot, want: true},
		{name: "member creates bot", role: RoleMember, action: ActionCreateBot, want: true},
		{name: "viewer cannot create bot", role: RoleViewer, action: ActionCreateBot, want: false},

		// Bot management and secrets (ownership aware for members)
		{name: "owner manages any bot", role: RoleOwner, action: ActionManageBot, isBotOwner: false, want: true},
		{name: "admin manages any bot", role: RoleAdmin, action: ActionManageBot, isBotOwner: false, want: true},
		{name: "member manages own bot", role: RoleMember, action: ActionManageBot, isBotOwner: true, want: true},
		{name: "member cannot manage others bot", role: RoleMember, action: ActionManageBot, isBotOwner: false, want: false},
		{name: "viewer cannot manage even flagged owner", role: RoleViewer, action: ActionManageBot, isBotOwner: true, want: false},

		{name: "owner manages any bot secrets", role: RoleOwner, action: ActionManageBotSecrets, isBotOwner: false, want: true},
		{name: "admin manages any bot secrets", role: RoleAdmin, action: ActionManageBotSecrets, isBotOwner: false, want: true},
		{name: "member manages own bot secrets", role: RoleMember, action: ActionManageBotSecrets, isBotOwner: true, want: true},
		{name: "member cannot manage others bot secrets", role: RoleMember, action: ActionManageBotSecrets, isBotOwner: false, want: false},
		{name: "viewer cannot manage bot secrets", role: RoleViewer, action: ActionManageBotSecrets, isBotOwner: true, want: false},

		// Unknown role or action fails closed
		{name: "invalid role denied", role: Role("ghost"), action: ActionViewWorkspace, isBotOwner: true, want: false},
		{name: "unknown action denied", role: RoleOwner, action: Action("unknown"), isBotOwner: true, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Can(tc.role, tc.action, tc.isBotOwner); got != tc.want {
				t.Fatalf("Can(%q, %q, %v) = %v, want %v", tc.role, tc.action, tc.isBotOwner, got, tc.want)
			}
		})
	}
}

func TestCanAssignAndModifyMember(t *testing.T) {
	assignCases := []struct {
		actor  Role
		target Role
		want   bool
	}{
		{actor: RoleOwner, target: RoleOwner, want: true},
		{actor: RoleOwner, target: RoleAdmin, want: true},
		{actor: RoleOwner, target: RoleMember, want: true},
		{actor: RoleOwner, target: RoleViewer, want: true},
		{actor: RoleAdmin, target: RoleOwner, want: false},
		{actor: RoleAdmin, target: RoleAdmin, want: false},
		{actor: RoleAdmin, target: RoleMember, want: true},
		{actor: RoleAdmin, target: RoleViewer, want: true},
		{actor: RoleMember, target: RoleViewer, want: false},
		{actor: RoleViewer, target: RoleViewer, want: false},
		{actor: Role("bad"), target: RoleViewer, want: false},
	}
	for _, tc := range assignCases {
		if got := CanAssignRole(tc.actor, tc.target); got != tc.want {
			t.Fatalf("CanAssignRole(%q, %q) = %v, want %v", tc.actor, tc.target, got, tc.want)
		}
		if got := CanModifyMember(tc.actor, tc.target); got != tc.want {
			t.Fatalf("CanModifyMember(%q, %q) = %v, want %v", tc.actor, tc.target, got, tc.want)
		}
	}
}

func TestCanEditMemberRoleDescription(t *testing.T) {
	cases := []struct {
		name   string
		actor  Role
		target Role
		isSelf bool
		want   bool
	}{
		// Owner can edit anyone including self.
		{name: "owner edits self", actor: RoleOwner, target: RoleOwner, isSelf: true, want: true},
		{name: "owner edits other owner", actor: RoleOwner, target: RoleOwner, isSelf: false, want: true},
		{name: "owner edits admin", actor: RoleOwner, target: RoleAdmin, isSelf: false, want: true},
		{name: "owner edits member", actor: RoleOwner, target: RoleMember, isSelf: false, want: true},
		{name: "owner edits viewer", actor: RoleOwner, target: RoleViewer, isSelf: false, want: true},

		// Admin can edit self, members, and viewers; not owners or other admins.
		{name: "admin edits self", actor: RoleAdmin, target: RoleAdmin, isSelf: true, want: true},
		{name: "admin cannot edit other admin", actor: RoleAdmin, target: RoleAdmin, isSelf: false, want: false},
		{name: "admin cannot edit owner", actor: RoleAdmin, target: RoleOwner, isSelf: false, want: false},
		{name: "admin edits member", actor: RoleAdmin, target: RoleMember, isSelf: false, want: true},
		{name: "admin edits viewer", actor: RoleAdmin, target: RoleViewer, isSelf: false, want: true},

		// Member can edit only self.
		{name: "member edits self", actor: RoleMember, target: RoleMember, isSelf: true, want: true},
		{name: "member cannot edit other member", actor: RoleMember, target: RoleMember, isSelf: false, want: false},
		{name: "member cannot edit owner", actor: RoleMember, target: RoleOwner, isSelf: false, want: false},
		{name: "member cannot edit admin", actor: RoleMember, target: RoleAdmin, isSelf: false, want: false},
		{name: "member cannot edit viewer", actor: RoleMember, target: RoleViewer, isSelf: false, want: false},

		// Viewer is strictly read-only, even for self.
		{name: "viewer cannot edit self", actor: RoleViewer, target: RoleViewer, isSelf: true, want: false},
		{name: "viewer cannot edit other", actor: RoleViewer, target: RoleMember, isSelf: false, want: false},

		// Invalid roles fail closed.
		{name: "invalid actor denied", actor: Role("ghost"), target: RoleMember, isSelf: true, want: false},
		{name: "invalid target denied", actor: RoleOwner, target: Role("ghost"), isSelf: false, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanEditMemberRoleDescription(tc.actor, tc.target, tc.isSelf); got != tc.want {
				t.Fatalf("CanEditMemberRoleDescription(%q, %q, %v) = %v, want %v", tc.actor, tc.target, tc.isSelf, got, tc.want)
			}
		})
	}
}
