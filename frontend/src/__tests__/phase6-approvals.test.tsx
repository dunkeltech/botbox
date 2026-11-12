import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApprovalsInbox } from '@/components/team/ApprovalsInbox';
import { TeamScreen } from '@/components/team/TeamScreen';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { ApiClientError, requestJson } from '@/lib/api-client';
import type { Bot, WorkspaceMember, WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return { ...actual, requestJson: vi.fn(), getAccessToken: vi.fn(() => null) };
});

const request = vi.mocked(requestJson);
type Group = components['schemas']['Group'];
type ChatMessage = components['schemas']['ChatMessage'];
type Approval = components['schemas']['Approval'];

const group: Group = {
  id: 'group-1',
  name: 'Launch',
  goal: 'Ship the fix',
  leadBotId: 'bot-1',
  botTalk: true,
  turnLimit: 20,
  answerPolicy: 'mentioned_else_lead',
  askBefore: [{ kind: 'new_host' }],
  budgetLimitMicros: 100,
  paused: true,
  pauseReason: 'approval',
  turnsSinceHuman: 2,
  unread: 1,
  members: [
    { type: 'bot', id: 'bot-1', roleText: 'lead', name: 'Atlas' },
    { type: 'user', id: 'user-1', roleText: '', name: 'Jan' },
  ],
};

const bots: Bot[] = [
  {
    id: 'bot-1',
    workspaceId: 'ws-1',
    name: 'Atlas',
    description: 'Writes the fix',
    avatarSeed: 'atlas',
    llmMode: 'cli',
    llmProvider: 'claude',
    llmModel: 'opus',
    driverType: 'docker',
    replyLanguage: 'en',
    turnTimeoutMinutes: 10,
    status: 'idle',
    mood: 'idle',
    createdAt: '2026-11-15T00:00:00Z',
    updatedAt: '2026-11-15T00:00:00Z',
  },
];

const humans: WorkspaceMember[] = [
  {
    workspaceId: 'ws-1',
    userId: 'user-1',
    email: 'jan@example.com',
    displayName: 'Jan',
    locale: 'en',
    role: 'member',
    joinedAt: '2026-11-15T00:00:00Z',
  },
];

const hostMessage: ChatMessage = {
  id: 'message-host',
  position: 1,
  senderType: 'system',
  kind: 'approval',
  content: 'Allow host paste.example.net',
  createdAt: '2026-11-16T00:00:01Z',
};

function hostApproval(status: Approval['status'] = 'pending'): Approval {
  return {
    id: 'approval-host',
    botId: 'bot-1',
    conversationId: 'group-1',
    messageId: 'message-host',
    kind: 'new_host',
    status,
    host: 'paste.example.net',
    createdAt: '2026-11-16T00:00:00Z',
  };
}

let approvals: Approval[] = [];
let transcript: ChatMessage[] = [];
let failList = false;
let decisionFailure: { status: number; code: string } | null = null;

function handler(path: string, options?: { method?: string; body?: unknown }) {
  const method = options?.method ?? 'GET';
  if (method === 'POST' && path.includes('/decision')) {
    if (decisionFailure) {
      return Promise.reject(
        new ApiClientError(
          decisionFailure.status,
          decisionFailure.code,
          'secret detail',
        ),
      );
    }
    const id = path.split('/approvals/')[1]?.split('/')[0];
    const decision = (options?.body as { decision?: string } | undefined)
      ?.decision;
    if (decision === 'approved' || decision === 'denied') {
      approvals = approvals.map((row) =>
        row.id === id ? { ...row, status: decision } : row,
      );
      if (decision === 'denied') {
        transcript = [
          ...transcript,
          {
            id: 'message-denied',
            position: transcript.length + 1,
            senderType: 'system',
            kind: 'system',
            content: 'Denied · this request stays blocked',
            createdAt: '2026-11-16T00:00:02Z',
          },
        ];
      }
    }
    return Promise.resolve(undefined);
  }
  if (method === 'POST' && path.endsWith('/read'))
    return Promise.resolve(undefined);
  if (path.endsWith('/groups')) return Promise.resolve([group]);
  if (path.endsWith('/bots')) return Promise.resolve({ bots });
  if (path.endsWith('/members')) return Promise.resolve({ members: humans });
  if (path.includes('/approvals')) {
    if (failList) {
      return Promise.reject(
        new ApiClientError(503, 'chat.unavailable', 'secret detail'),
      );
    }
    return Promise.resolve({ approvals });
  }
  if (path.includes('/notifications')) {
    return Promise.resolve({ notifications: [] });
  }
  if (path.includes('/messages')) {
    return Promise.resolve({ messages: transcript, turns: [] });
  }
  return Promise.resolve({});
}

function show(
  role: WorkspaceRole = 'member',
  surfaces: 'both' | 'inbox' = 'both',
) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryProvider>
        {surfaces === 'both' ? (
          <TeamScreen
            workspaceId="ws-1"
            userId="user-1"
            displayName="Jan"
            role={role}
            workspaceName="dunkel.Tech"
          />
        ) : null}
        <ApprovalsInbox workspaceId="ws-1" role={role} />
      </QueryProvider>
    </NextIntlClientProvider>,
  );
}

async function openSurfaces() {
  const log = await screen.findByRole('log', { name: messages.team.messages });
  const inbox = screen.getByRole('region', { name: messages.team.inboxTitle });
  return { log, inbox };
}

beforeEach(() => {
  approvals = [hostApproval()];
  transcript = [{ ...hostMessage }];
  failList = false;
  decisionFailure = null;
  request.mockImplementation(handler);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('Phase 6 approval cards and inbox', () => {
  it('approves a pending host card from the transcript and the inbox follows', async () => {
    show('member');
    const { log, inbox } = await openSurfaces();
    expect(await screen.findByText('1 pending')).toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: messages.team.backToTeam }),
    ).toHaveAttribute('href', '/team');
    fireEvent.click(
      await within(log).findByRole('button', {
        name: messages.team.approveHost,
      }),
    );
    await waitFor(() => {
      expect(
        within(log).getByText(messages.team.statusApproved),
      ).toBeInTheDocument();
    });
    expect(
      within(log).queryByRole('button', { name: messages.team.approveHost }),
    ).toBeNull();
    expect(within(log).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    expect(screen.getByText('0 pending')).toBeInTheDocument();
    fireEvent.click(within(inbox).getByRole('button', { name: /^Approved/ }));
    expect(within(inbox).getByText('paste.example.net')).toBeInTheDocument();
    expect(
      within(inbox).getByText(messages.team.statusApproved),
    ).toBeInTheDocument();
    expect(within(inbox).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    const sent = request.mock.calls.find(([path]) =>
      String(path).includes('/decision'),
    );
    expect(String(sent?.[0])).toContain('/approvals/approval-host/decision');
    expect(sent?.[1]).toMatchObject({
      method: 'POST',
      body: { decision: 'approved' },
    });
  });

  it('denies a card from the inbox and the transcript follows', async () => {
    show('owner');
    const { log, inbox } = await openSurfaces();
    fireEvent.click(
      await within(inbox).findByRole('button', { name: /^Deny$/ }),
    );
    await waitFor(() => {
      expect(log).toHaveTextContent('Denied · this request stays blocked');
    });
    expect(
      within(log).getByText(messages.team.statusDenied),
    ).toBeInTheDocument();
    expect(within(log).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    fireEvent.click(within(inbox).getByRole('button', { name: /^Denied/ }));
    expect(
      within(inbox).getByText(messages.team.statusDenied),
    ).toBeInTheDocument();
    expect(within(inbox).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    const sent = request.mock.calls.find(([path]) =>
      String(path).includes('/decision'),
    );
    expect(sent?.[1]).toMatchObject({ body: { decision: 'denied' } });
  });

  it('shows an approved card without decision buttons', async () => {
    approvals = [hostApproval('approved')];
    show('admin');
    const { log, inbox } = await openSurfaces();
    expect(
      await within(log).findByText(messages.team.statusApproved),
    ).toBeInTheDocument();
    expect(within(log).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    expect(
      within(log).queryByRole('button', { name: messages.team.approveHost }),
    ).toBeNull();
    fireEvent.click(within(inbox).getByRole('button', { name: /^Approved/ }));
    expect(
      within(inbox).getByText(messages.team.statusApproved),
    ).toBeInTheDocument();
    expect(within(inbox).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    expect(
      request.mock.calls.some(([path]) => String(path).includes('/decision')),
    ).toBe(false);
  });

  it('shows a denied card without decision buttons', async () => {
    approvals = [hostApproval('denied')];
    show('member');
    const { log, inbox } = await openSurfaces();
    expect(
      await within(log).findByText(messages.team.statusDenied),
    ).toBeInTheDocument();
    expect(within(log).queryByRole('button', { name: /^Deny$/ })).toBeNull();
    expect(
      within(log).queryByRole('button', { name: messages.team.approveHost }),
    ).toBeNull();
    fireEvent.click(within(inbox).getByRole('button', { name: /^Denied/ }));
    expect(
      within(inbox).getByText(messages.team.statusDenied),
    ).toBeInTheDocument();
    expect(
      within(inbox).queryByRole('button', { name: messages.team.approveHost }),
    ).toBeNull();
  });

  it('hides decision buttons from a viewer', async () => {
    show('viewer');
    const { log, inbox } = await openSurfaces();
    expect(
      await within(log).findByText('paste.example.net'),
    ).toBeInTheDocument();
    expect(within(inbox).getByText('paste.example.net')).toBeInTheDocument();
    expect(
      within(log).getByText(messages.team.statusPending),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Deny$/ })).toBeNull();
    expect(
      screen.queryByRole('button', { name: messages.team.approveHost }),
    ).toBeNull();
    expect(screen.getByText(messages.team.whoViewerDetail)).toBeInTheDocument();
    expect(
      request.mock.calls.some(([path]) => String(path).includes('/decision')),
    ).toBe(false);
  });

  it('hides workspace budget buttons from a member and shows them to an admin', async () => {
    approvals.push({
      id: 'budget-1',
      botId: 'bot-1',
      kind: 'raise_budget',
      status: 'pending',
      createdAt: '2026-11-16T00:00:00Z',
    });
    show('member');
    expect(
      await screen.findByText(messages.team.titleBudget),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: messages.team.approveBudget }),
    ).toBeNull();
    expect(
      screen.getAllByRole('button', { name: messages.team.approveHost }).length,
    ).toBeGreaterThan(0);
    cleanup();
    approvals = [
      hostApproval(),
      {
        id: 'budget-1',
        botId: 'bot-1',
        kind: 'raise_budget',
        status: 'pending',
        createdAt: '2026-11-16T00:00:00Z',
      },
    ];
    show('admin');
    expect(
      await screen.findByRole('button', { name: messages.team.approveBudget }),
    ).toBeInTheDocument();
  });

  it('uses the push and custom labels from the public payload', async () => {
    approvals = [
      {
        ...hostApproval(),
        kind: 'push_protected',
        host: undefined,
        branch: 'main',
      },
    ];
    show('member');
    await waitFor(() => {
      expect(
        screen.getAllByRole('button', { name: messages.team.approvePush }),
      ).toHaveLength(2);
    });
    expect(screen.getAllByText('main').length).toBeGreaterThan(0);
    cleanup();
    approvals = [
      {
        ...hostApproval(),
        kind: 'custom',
        host: undefined,
        label: 'ship the release',
      },
    ];
    show('member');
    await waitFor(() => {
      expect(screen.getAllByRole('button', { name: /^Approve$/ })).toHaveLength(
        2,
      );
    });
    expect(screen.getAllByText('ship the release').length).toBeGreaterThan(0);
  });

  it.each([
    [403, 'auth.forbidden', messages.errors.authForbidden],
    [404, 'approval.not_found', messages.errors.approvalNotFound],
    [400, 'validation_error', messages.errors.validationError],
    [409, 'approval.conflict', messages.errors.approvalConflict],
    [409, 'approval.host_not_blocked', messages.errors.approvalHostNotBlocked],
    [409, 'approval.cap_not_raised', messages.errors.approvalCapNotRaised],
    [429, 'chat.queue_full', messages.errors.chatQueueFull],
    [503, 'chat.unavailable', messages.errors.chatUnavailable],
    [500, 'approval.internal', messages.errors.unknownError],
  ] as const)(
    'shows the translated %s %s and keeps the card pending',
    async (status, code, text) => {
      decisionFailure = { status, code };
      show('owner');
      const [deny] = await screen.findAllByRole('button', { name: /^Deny$/ });
      if (!deny) throw new Error('deny button missing');
      fireEvent.click(deny);
      expect(await screen.findByRole('alert')).toHaveTextContent(text);
      expect(screen.queryByText('secret detail')).toBeNull();
      expect(screen.queryByText(messages.team.statusDenied)).toBeNull();
      expect(
        screen.getAllByRole('button', { name: /^Deny$/ }).length,
      ).toBeGreaterThan(0);
    },
  );

  it('shows an empty inbox and a load failure without the raw error', async () => {
    approvals = [];
    show('owner', 'inbox');
    expect(
      await screen.findByText(messages.team.emptyPending),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Deny$/ })).toBeNull();
    cleanup();
    failList = true;
    show('owner', 'inbox');
    expect(await screen.findByRole('alert')).toHaveTextContent(
      messages.team.inboxLoadFailed,
    );
    expect(screen.queryByText('secret detail')).toBeNull();
  });
});
