// SPDX-License-Identifier: AGPL-3.0-only

import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type Approval = components['schemas']['Approval'];
type ApprovalList = components['schemas']['ApprovalList'];

/** A decision the approvals endpoint accepts. */
export type ApprovalDecision = 'approved' | 'denied';

/**
 * The one public payload field a card may show. Host wins, then branch, then
 * label. Blank strings are skipped so the card does not invent a line.
 */
export function payloadField(
  approval: Pick<Approval, 'host' | 'branch' | 'label'>,
): string {
  const host = approval.host?.trim() ?? '';
  if (host !== '') return host;
  const branch = approval.branch?.trim() ?? '';
  if (branch !== '') return branch;
  return approval.label?.trim() ?? '';
}

/**
 * Owners and admins decide every visible row. A member decides a group card
 * only. A workspace budget row has no conversation, and the API returns 404
 * for a member, so the button is not offered. Viewers are read-only.
 * A decided row never offers the buttons again.
 */
export function canDecideApproval(
  role: WorkspaceRole,
  approval: Pick<Approval, 'status' | 'conversationId'>,
): boolean {
  if (approval.status !== 'pending') return false;
  if (role === 'owner' || role === 'admin') return true;
  if (role === 'member') return Boolean(approval.conversationId);
  return false;
}

/**
 * The transcript card for this message. A row from another group is ignored
 * so a shared message id cannot paint the wrong decision.
 */
export function approvalForMessage(
  approvals: readonly Approval[],
  messageId: string,
  groupId: string,
): Approval | undefined {
  return approvals.find(
    (row) =>
      row.messageId === messageId &&
      (row.conversationId === undefined || row.conversationId === groupId),
  );
}

/**
 * Writes the decision into the shared list. The inbox and the open transcript
 * both read this cache, so the pending buttons disappear before the refetch.
 */
export function applyDecision(
  list: ApprovalList | undefined,
  id: string,
  decision: ApprovalDecision,
): ApprovalList | undefined {
  if (!list) return list;
  return {
    approvals: list.approvals.map((row) =>
      row.id === id ? { ...row, status: decision } : row,
    ),
  };
}
