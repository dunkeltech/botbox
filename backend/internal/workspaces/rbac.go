package workspaces

// Role represents a member's authorization level inside a workspace.
type Role string

const (
	// RoleOwner grants full control over a workspace, including membership,
	// billing, and destructive operations.
	RoleOwner Role = "owner"
	// RoleAdmin grants operational control over members, invitations, and all bots.
	RoleAdmin Role = "admin"
	// RoleMember allows participating in chat and creating or managing own bots.
	RoleMember Role = "member"
	// RoleViewer grants read-only visibility inside a workspace.
	RoleViewer Role = "viewer"
)

// Action identifies an operation protected by workspace RBAC.
type Action string

const (
	// ActionViewWorkspace allows reading workspace metadata, members, and bots.
	ActionViewWorkspace Action = "view_workspace"
	// ActionManageWorkspace allows owner-level workspace lifecycle changes.
	ActionManageWorkspace Action = "manage_workspace"
	// ActionManageGitLabInstances allows registering and removing trusted GitLab origins.
	ActionManageGitLabInstances Action = "manage_gitlab_instances"
	// ActionManageMCPServers allows registering, syncing, and removing workspace MCP servers.
	ActionManageMCPServers Action = "manage_mcp_servers"
	// ActionManageMembers allows changing or removing workspace memberships.
	ActionManageMembers Action = "manage_members"
	// ActionManageInvitations allows viewing, creating, and revoking invitations.
	ActionManageInvitations Action = "manage_invitations"
	// ActionCreateBot allows registering a new bot in the workspace.
	ActionCreateBot Action = "create_bot"
	// ActionManageBot allows updating or deleting an existing bot.
	ActionManageBot Action = "manage_bot"
	// ActionManageBotSecrets allows writing or rotating bot credentials and secrets.
	// #nosec G101 -- RBAC action identifier constant, not a credential.
	ActionManageBotSecrets Action = "manage_bot_secrets"
	// ActionFulfillSecretRequest allows fulfilling or denying an in-chat secret request.
	// #nosec G101 -- RBAC action identifier constant, not a credential.
	ActionFulfillSecretRequest Action = "fulfill_secret_request"
)

// ValidRole reports whether role is one of the supported workspace roles.
func ValidRole(role string) bool {
	switch Role(role) {
	case RoleOwner, RoleAdmin, RoleMember, RoleViewer:
		return true
	default:
		return false
	}
}

// ParseRole converts a string into a typed Role if valid.
func ParseRole(role string) (Role, bool) {
	if !ValidRole(role) {
		return "", false
	}
	return Role(role), true
}

// Can evaluates whether a member with role may perform action on a resource.
// For bot-scoped mutations, isBotOwner indicates whether the caller created the bot.
func Can(role Role, action Action, isBotOwner bool) bool {
	if !ValidRole(string(role)) {
		return false
	}

	switch action {
	case ActionViewWorkspace:
		return true
	case ActionManageWorkspace:
		return role == RoleOwner
	case ActionManageMembers, ActionManageInvitations, ActionManageGitLabInstances, ActionManageMCPServers:
		return role == RoleOwner || role == RoleAdmin
	case ActionCreateBot, ActionFulfillSecretRequest:
		return role == RoleOwner || role == RoleAdmin || role == RoleMember
	case ActionManageBot, ActionManageBotSecrets:
		if role == RoleOwner || role == RoleAdmin {
			return true
		}
		return role == RoleMember && isBotOwner
	default:
		return false
	}
}

// CanAssignRole reports whether an actor with actorRole may assign targetRole
// to a member or invitation without escalating beyond their own privilege tier.
func CanAssignRole(actorRole, targetRole Role) bool {
	if !ValidRole(string(actorRole)) || !ValidRole(string(targetRole)) {
		return false
	}
	switch actorRole {
	case RoleOwner:
		return true
	case RoleAdmin:
		return targetRole == RoleMember || targetRole == RoleViewer
	default:
		return false
	}
}

// CanModifyMember reports whether an actor may change or remove a member who
// currently holds currentTargetRole.
func CanModifyMember(actorRole, currentTargetRole Role) bool {
	if !ValidRole(string(actorRole)) || !ValidRole(string(currentTargetRole)) {
		return false
	}
	switch actorRole {
	case RoleOwner:
		return true
	case RoleAdmin:
		return currentTargetRole == RoleMember || currentTargetRole == RoleViewer
	default:
		return false
	}
}
