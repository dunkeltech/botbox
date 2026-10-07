import type { Bot, WorkspaceRole } from '@/types/api';

/**
 * Returns true when the workspace role can create bots.
 */
export function canCreateBot(role: WorkspaceRole | undefined): boolean {
  return role === 'owner' || role === 'admin' || role === 'member';
}

/**
 * Mirrors backend workspaces.Can(role, ActionManageBot, isBotOwner):
 * owners and admins manage any bot; members manage bots they own; viewers are read-only.
 */
export function canManageBot(
  role: WorkspaceRole | undefined,
  userId: string | undefined,
  bot: Pick<Bot, 'ownerUserId'> | undefined,
): boolean {
  if (role === 'owner' || role === 'admin') {
    return true;
  }
  if (role === 'member' && userId && bot?.ownerUserId) {
    return bot.ownerUserId === userId;
  }
  return false;
}

/**
 * Returns true when the role can invite teammates, change member roles, or revoke invitations.
 */
export function canManageMembers(role: WorkspaceRole | undefined): boolean {
  return role === 'owner' || role === 'admin';
}
