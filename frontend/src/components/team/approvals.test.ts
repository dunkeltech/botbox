import { describe, expect, it } from 'vitest';
import {
  applyDecision,
  approvalForMessage,
  canDecideApproval,
  payloadField,
} from '@/components/team/approvals';
import type { components } from '@/types/openapi.gen';

type Approval = components['schemas']['Approval'];

function row(overrides: Partial<Approval> = {}): Approval {
  return {
    id: 'approval-1',
    botId: 'bot-1',
    kind: 'new_host',
    status: 'pending',
    createdAt: '2026-11-16T00:00:00Z',
    ...overrides,
  };
}

describe('approval card rules', () => {
  it('shows one public payload field and skips blanks', () => {
    expect(payloadField(row({ host: ' a.com ', branch: 'main' }))).toBe(
      'a.com',
    );
    expect(payloadField(row({ host: ' ', branch: ' main ' }))).toBe('main');
    expect(payloadField(row({ label: ' ship ' }))).toBe('ship');
    expect(payloadField(row({ host: ' ', branch: ' ', label: ' ' }))).toBe('');
    expect(payloadField(row())).toBe('');
  });

  it('lets only an allowed role decide a pending row', () => {
    const group = row({ conversationId: 'group-1' });
    const budget = row({ kind: 'raise_budget' });
    expect(canDecideApproval('owner', budget)).toBe(true);
    expect(canDecideApproval('admin', budget)).toBe(true);
    expect(canDecideApproval('member', group)).toBe(true);
    expect(canDecideApproval('member', budget)).toBe(false);
    expect(canDecideApproval('member', row({ conversationId: '' }))).toBe(
      false,
    );
    expect(canDecideApproval('viewer', group)).toBe(false);
    expect(canDecideApproval('owner', row({ status: 'approved' }))).toBe(false);
    expect(
      canDecideApproval(
        'admin',
        row({ status: 'denied', conversationId: 'group-1' }),
      ),
    ).toBe(false);
  });

  it('matches a card to its group message only', () => {
    const mine = row({ messageId: 'message-1', conversationId: 'group-1' });
    const other = row({
      id: 'approval-2',
      messageId: 'message-1',
      conversationId: 'group-2',
    });
    expect(approvalForMessage([other, mine], 'message-1', 'group-1')).toBe(
      mine,
    );
    expect(approvalForMessage([other], 'message-1', 'group-1')).toBeUndefined();
    expect(approvalForMessage([mine], 'message-2', 'group-1')).toBeUndefined();
    expect(
      approvalForMessage(
        [row({ messageId: 'message-1' })],
        'message-1',
        'group-1',
      )?.id,
    ).toBe('approval-1');
  });

  it('writes a decision into the shared list and leaves other rows', () => {
    const list = {
      approvals: [row(), row({ id: 'approval-2', status: 'pending' })],
    };
    expect(applyDecision(undefined, 'approval-1', 'approved')).toBeUndefined();
    const next = applyDecision(list, 'approval-1', 'denied');
    expect(next?.approvals.map((item) => item.status)).toEqual([
      'denied',
      'pending',
    ]);
    expect(
      applyDecision(list, 'missing', 'approved')?.approvals[0]?.status,
    ).toBe('pending');
  });
});
