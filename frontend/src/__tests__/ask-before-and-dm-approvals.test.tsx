// SPDX-License-Identifier: AGPL-3.0-only

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
import { BotBuilderForm } from '@/components/bots/BotBuilderForm';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { ApprovalsInbox } from '@/components/team/ApprovalsInbox';
import { GroupDetails } from '@/components/team/GroupDetails';
import { NewGroupDialog } from '@/components/team/NewGroupDialog';
import { TeamScreen } from '@/components/team/TeamScreen';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { requestJson } from '@/lib/api-client';
import type {
  Bot,
  WorkspaceMember,
  WorkspaceRole,
  WorkspaceSummary,
} from '@/types/api';
import type { components } from '@/types/openapi.gen';
import messages from '../../messages/en.json';

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => '/bots/bot-1/settings',
}));

vi.mock('@/context/AuthContext', () => ({
  useAuth: () => ({
    user: {
      id: 'user-1',
      email: 'jan@example.com',
      displayName: 'Jan',
      locale: 'en',
    },
  }),
}));

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return {
    ...actual,
    requestJson: vi.fn(),
    getAccessToken: vi.fn(() => null),
    createBot: vi.fn(),
    updateBot: vi.fn(),
    deleteBot: vi.fn(),
    listModelPrices: vi.fn().mockResolvedValue({
      prices: [
        {
          provider: 'openrouter',
          model: 'openrouter/fixture',
          inputMicrosPerMillion: 100,
          outputMicrosPerMillion: 200,
          cacheMicrosPerMillion: 0,
        },
      ],
    }),
  };
});

const request = vi.mocked(requestJson);
type Group = components['schemas']['Group'];
type Approval = components['schemas']['Approval'];
type ChatMessage = components['schemas']['ChatMessage'];

const workspace: WorkspaceSummary = {
  id: 'ws-1',
  name: 'dunkel.Tech',
  slug: 'dunkel-tech',
  role: 'owner',
  defaultDriver: 'docker',
  defaultLocale: 'en',
  createdAt: '2026-11-15T00:00:00Z',
};

const baseBot: Bot = {
  id: 'bot-1',
  workspaceId: 'ws-1',
  ownerUserId: 'user-1',
  name: 'Atlas',
  description: 'Helpful engineer',
  avatarSeed: 'atlas',
  llmMode: 'cli',
  llmProvider: 'openrouter',
  llmModel: 'openrouter/fixture',
  driverType: 'docker',
  replyLanguage: 'en',
  turnTimeoutMinutes: 10,
  askBefore: [{ kind: 'new_host' }],
  status: 'idle',
  mood: 'idle',
  createdAt: '2026-11-15T00:00:00Z',
  updatedAt: '2026-11-15T00:00:00Z',
};

const humans: WorkspaceMember[] = [
  {
    workspaceId: 'ws-1',
    userId: 'user-1',
    email: 'jan@example.com',
    displayName: 'Jan',
    locale: 'en',
    role: 'owner',
    joinedAt: '2026-11-15T00:00:00Z',
  },
];

const baseGroup: Group = {
  id: 'group-1',
  name: 'Launch',
  goal: 'Ship the fix',
  leadBotId: 'bot-1',
  botTalk: true,
  turnLimit: 20,
  answerPolicy: 'mentioned_else_lead',
  askBefore: [],
  budgetLimitMicros: null,
  paused: false,
  pauseReason: '',
  turnsSinceHuman: 0,
  unread: 0,
  members: [
    { type: 'bot', id: 'bot-1', roleText: 'lead', name: 'Atlas' },
    { type: 'user', id: 'user-1', roleText: '', name: 'Jan' },
  ],
};

function renderWithIntl(ui: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryProvider>{ui}</QueryProvider>
    </NextIntlClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
});

describe('AskBefore configuration UI', () => {
  it('configures askBefore rules when creating a group in NewGroupDialog', async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderWithIntl(
      <NewGroupDialog
        userId="user-1"
        displayName="Jan"
        bots={[baseBot]}
        humans={humans}
        creating={false}
        error={null}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );

    fireEvent.change(screen.getByLabelText(messages.team.name), {
      target: { value: 'ops-team' },
    });
    fireEvent.click(screen.getByRole('button', { name: /Atlas/ }));
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.askNewHost }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.askBudget }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.askPush }),
    );
    fireEvent.change(screen.getByLabelText(messages.team.askPushBranchLabel), {
      target: { value: 'release' },
    });
    fireEvent.blur(screen.getByLabelText(messages.team.askPushBranchLabel));
    fireEvent.change(screen.getByLabelText(messages.team.askCustomLabel), {
      target: { value: 'delete prod data' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.askCustomAdd }),
    );

    fireEvent.click(screen.getByRole('button', { name: messages.team.create }));

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1));
    expect(onCreate).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'ops-team',
        askBefore: [
          { kind: 'new_host' },
          { kind: 'raise_budget' },
          { kind: 'push_protected', label: 'release' },
          { kind: 'custom', label: 'delete prod data' },
        ],
      }),
    );
  });

  it('edits askBefore in GroupDetails for editors and shows read-only pills for viewers', () => {
    const onPatch = vi.fn();
    const { rerender } = renderWithIntl(
      <GroupDetails
        group={baseGroup}
        bots={[baseBot]}
        canEdit
        error={null}
        onPatch={onPatch}
      />,
    );

    fireEvent.click(
      screen.getByRole('button', { name: messages.team.askNewHost }),
    );
    expect(onPatch).toHaveBeenCalledWith({
      askBefore: [{ kind: 'new_host' }],
    });

    rerender(
      <NextIntlClientProvider locale="en" messages={messages}>
        <QueryProvider>
          <GroupDetails
            group={{ ...baseGroup, askBefore: [{ kind: 'new_host' }] }}
            bots={[baseBot]}
            canEdit={false}
            error={null}
            onPatch={onPatch}
          />
        </QueryProvider>
      </NextIntlClientProvider>,
    );

    expect(
      screen.queryByRole('button', { name: messages.team.askNewHost }),
    ).toBeNull();
    expect(screen.getByText(messages.team.askNewHost)).toBeInTheDocument();
  });

  it('configures bot askBefore in BotBuilderForm without offering raise_budget', async () => {
    const apiClient = await import('@/lib/api-client');
    const updateBotMock = vi.mocked(apiClient.updateBot);
    updateBotMock.mockResolvedValue({
      ...baseBot,
      askBefore: [{ kind: 'new_host' }, { kind: 'custom', label: 'wipe disk' }],
    });

    renderWithIntl(
      <BotBuilderForm
        mode="edit"
        workspace={workspace}
        bot={baseBot}
        embedded
      />,
    );

    expect(
      screen.queryByRole('button', { name: messages.team.askBudget }),
    ).toBeNull();
    expect(
      screen.getByRole('button', { name: messages.team.askNewHost }),
    ).toHaveAttribute('aria-pressed', 'true');

    fireEvent.change(screen.getByLabelText(messages.team.askCustomLabel), {
      target: { value: 'wipe disk' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.askCustomAdd }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: messages.builder.saveSubmit }),
    );

    await waitFor(() => expect(updateBotMock).toHaveBeenCalledTimes(1));
    expect(updateBotMock).toHaveBeenCalledWith(
      'ws-1',
      'bot-1',
      expect.objectContaining({
        askBefore: [
          { kind: 'new_host' },
          { kind: 'custom', label: 'wipe disk' },
        ],
      }),
    );
  });

  it('edits bot askBefore from TeamScreen DM details when authorized and locks for non-owners', async () => {
    let botsState: Bot[] = [{ ...baseBot, ownerUserId: 'other-user' }];
    request.mockImplementation((path, options) => {
      if (options?.method === 'PATCH' && path.includes('/bots/bot-1')) {
        const nextAsk = (options.body as { askBefore?: Bot['askBefore'] })
          ?.askBefore;
        botsState = botsState.map((b) =>
          b.id === 'bot-1' ? { ...b, askBefore: nextAsk } : b,
        );
        return Promise.resolve(botsState[0]);
      }
      if (path.endsWith('/groups')) return Promise.resolve([baseGroup]);
      if (path.endsWith('/bots')) return Promise.resolve({ bots: botsState });
      if (path.endsWith('/members'))
        return Promise.resolve({ members: humans });
      if (path.includes('/approvals'))
        return Promise.resolve({ approvals: [] });
      if (path.includes('/notifications'))
        return Promise.resolve({ notifications: [] });
      if (path.endsWith('/dm'))
        return Promise.resolve({ id: 'dm-1', botId: 'bot-1' });
      if (path.includes('/messages'))
        return Promise.resolve({ messages: [], turns: [] });
      if (path.includes('/budgets'))
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      return Promise.resolve({});
    });

    const renderScreen = (role: WorkspaceRole) =>
      renderWithIntl(
        <TeamScreen
          workspaceId="ws-1"
          userId="user-1"
          displayName="Jan"
          role={role}
          workspaceName="dunkel.Tech"
        />,
      );

    // Member who does not own the bot sees read-only rules in DM details.
    renderScreen('member');
    fireEvent.click(await screen.findByRole('button', { name: /Atlas/ }));
    expect(
      await screen.findByText(messages.team.askBeforeReadOnly),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: messages.team.askNewHost }),
    ).toBeNull();

    cleanup();
    // Owner can toggle askBefore directly from DM details.
    renderScreen('owner');
    fireEvent.click(await screen.findByRole('button', { name: /Atlas/ }));
    const toggle = await screen.findByRole('button', {
      name: messages.team.askNewHost,
    });
    fireEvent.click(toggle);

    await waitFor(() => {
      expect(request).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/bots/bot-1',
        expect.objectContaining({
          method: 'PATCH',
          body: { askBefore: [] },
        }),
      );
    });
  });
});

describe('1:1 DM and Bot Workspace approval cards', () => {
  it('renders, approves, and denies approval cards in ChatPanel and labels DM in ApprovalsInbox', async () => {
    let approvals: Approval[] = [
      {
        id: 'approval-dm-1',
        botId: 'bot-1',
        conversationId: 'dm-1',
        messageId: 'msg-approval-1',
        kind: 'new_host',
        status: 'pending',
        host: 'paste.example.net',
        createdAt: '2026-11-16T00:00:00Z',
      },
    ];
    let dmMessages: ChatMessage[] = [
      {
        id: 'msg-user-1',
        position: 1,
        senderType: 'user',
        senderUserId: 'user-1',
        kind: 'text',
        content: 'Check paste.example.net',
        createdAt: '2026-11-16T00:00:00Z',
      },
      {
        id: 'msg-approval-1',
        position: 2,
        senderType: 'system',
        kind: 'approval',
        content: 'Allow host paste.example.net',
        createdAt: '2026-11-16T00:00:01Z',
      },
    ];

    request.mockImplementation((path, options) => {
      const method = options?.method ?? 'GET';
      if (method === 'POST' && path.includes('/decision')) {
        const decision = (options?.body as { decision?: Approval['status'] })
          ?.decision;
        if (decision === 'approved' || decision === 'denied') {
          approvals = approvals.map((row) =>
            row.id === 'approval-dm-1' ? { ...row, status: decision } : row,
          );
          if (decision === 'denied') {
            dmMessages = [
              ...dmMessages,
              {
                id: 'msg-denied-1',
                position: 3,
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
      if (path.endsWith('/dm'))
        return Promise.resolve({ id: 'dm-1', botId: 'bot-1' });
      if (path.includes('/messages'))
        return Promise.resolve({ messages: dmMessages, turns: [] });
      if (path.includes('/approvals')) return Promise.resolve({ approvals });
      if (path.endsWith('/groups')) return Promise.resolve([baseGroup]);
      if (path.endsWith('/bots')) return Promise.resolve({ bots: [baseBot] });
      if (path.includes('/budgets'))
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      return Promise.resolve({});
    });

    renderWithIntl(
      <>
        <ChatPanel
          workspaceId="ws-1"
          userId="user-1"
          botId="bot-1"
          name="Atlas"
          avatarSeed="atlas"
          role="member"
          variant="session"
        />
        <ApprovalsInbox workspaceId="ws-1" role="member" />
      </>,
    );

    const log = await screen.findByRole('log', { name: messages.chat.history });
    const inbox = screen.getByRole('region', {
      name: messages.team.inboxTitle,
    });

    expect(
      await within(log).findByText('paste.example.net'),
    ).toBeInTheDocument();
    expect(
      await within(inbox).findByText(/Direct message/),
    ).toBeInTheDocument();

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

    // Reset to pending and test deny + system notice rendering in DM.
    cleanup();
    approvals = [{ ...approvals[0]!, status: 'pending' }];
    renderWithIntl(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas"
        role="member"
        variant="slack"
      />,
    );
    const slackLog = await screen.findByRole('log', {
      name: messages.chat.history,
    });
    fireEvent.click(
      await within(slackLog).findByRole('button', { name: /^Deny$/ }),
    );
    await waitFor(() => {
      expect(slackLog).toHaveTextContent('Denied · this request stays blocked');
    });
    expect(
      within(slackLog).getByText(messages.team.statusDenied),
    ).toBeInTheDocument();
  });
});
