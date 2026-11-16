import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TeamScreen } from '@/components/team/TeamScreen';
import {
  formatDayDividerLabel,
  formatFullDateTime,
  formatTimeLabel,
} from '@/components/chat/chatDate';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { ApiClientError, getAccessToken, requestJson } from '@/lib/api-client';
import type { Bot, WorkspaceMember } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return {
    ...actual,
    requestJson: vi.fn(),
    getAccessToken: vi.fn(() => null),
  };
});

const request = vi.mocked(requestJson);
type Group = components['schemas']['Group'];
type ChatMessage = components['schemas']['ChatMessage'];

const group: Group = {
  id: 'group-1',
  name: 'Launch',
  goal: 'Ship the fix',
  leadBotId: 'bot-1',
  botTalk: false,
  turnLimit: 20,
  answerPolicy: 'mentioned_else_lead',
  askBefore: [{ kind: 'custom', label: 'ship the release' }],
  budgetLimitMicros: 100,
  paused: false,
  pauseReason: '',
  turnsSinceHuman: 1,
  unread: 3,
  members: [
    { type: 'bot', id: 'bot-1', roleText: 'lead', name: 'Atlas' },
    { type: 'user', id: 'user-2', roleText: '', name: 'Sam' },
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
  {
    workspaceId: 'ws-1',
    userId: 'user-2',
    email: 'sam@example.com',
    displayName: 'Sam',
    locale: 'en',
    role: 'member',
    joinedAt: '2026-11-15T00:00:00Z',
  },
];

const history: ChatMessage[] = [
  {
    id: 'message-1',
    position: 1,
    senderType: 'user',
    senderUserId: 'user-2',
    kind: 'text',
    content: 'Ask @Atlas',
    createdAt: '2026-11-16T00:00:00Z',
  },
  {
    id: 'message-2',
    position: 2,
    senderType: 'system',
    kind: 'approval',
    content: 'Waiting on a protected push',
    createdAt: '2026-11-16T00:00:01Z',
  },
];

let groupRows: Group[] = [group];
let failCreate = false;

function handler(path: string, options?: { method?: string; body?: unknown }) {
  const method = options?.method ?? 'GET';
  if (method === 'POST' && path.endsWith('/groups')) {
    if (failCreate) {
      return Promise.reject(
        new ApiClientError(403, 'auth.forbidden', 'secret detail'),
      );
    }
    return Promise.resolve(group);
  }
  if (method === 'PATCH') return Promise.resolve(group);
  if (method === 'POST' && path.endsWith('/read'))
    return Promise.resolve(undefined);
  if (path.endsWith('/groups')) return Promise.resolve(groupRows);
  if (path.endsWith('/bots')) return Promise.resolve({ bots });
  if (path.endsWith('/members')) return Promise.resolve({ members: humans });
  if (path.includes('/approvals')) {
    return Promise.resolve({
      approvals: [
        {
          id: 'approval-1',
          botId: 'bot-1',
          kind: 'push_protected',
          status: 'pending',
          createdAt: '2026-11-16T00:00:00Z',
        },
        {
          id: 'approval-2',
          botId: 'bot-1',
          kind: 'new_host',
          status: 'pending',
          createdAt: '2026-11-16T00:00:00Z',
        },
      ],
    });
  }
  if (path.includes('/notifications')) {
    return Promise.resolve({
      notifications: [
        {
          id: 'note-1',
          kind: 'mention',
          conversationId: 'group-1',
          createdAt: '2026-11-16T00:00:00Z',
        },
      ],
    });
  }
  if (path.includes('/messages'))
    return Promise.resolve({ messages: history, turns: [] });
  if (path.endsWith('/dm'))
    return Promise.resolve({ id: 'dm-1', botId: 'bot-1' });
  if (path.includes('/budgets')) {
    return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
  }
  return Promise.resolve({});
}

function show(
  role: 'owner' | 'admin' | 'member' | 'viewer' = 'member',
  client?: QueryClient,
) {
  const content = (
    <TeamScreen
      workspaceId="ws-1"
      userId="user-1"
      displayName="Jan"
      role={role}
      workspaceName="dunkel.Tech"
    />
  );
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      {client ? (
        <QueryClientProvider client={client}>{content}</QueryClientProvider>
      ) : (
        <QueryProvider>{content}</QueryProvider>
      )}
    </NextIntlClientProvider>,
  );
}

beforeEach(() => {
  groupRows = [group];
  failCreate = false;
  vi.mocked(getAccessToken).mockReturnValue(null);
  request.mockImplementation(handler);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('Phase 6 team chat', () => {
  it('shows progress and a peer status snapshot alongside a running turn', async () => {
    groupRows = [
      {
        ...group,
        members: [
          ...group.members,
          { type: 'bot', id: 'bot-2', roleText: '', name: 'Babu Frik' },
        ],
      },
    ];
    const at = '2026-11-16T12:05:00Z';
    request.mockImplementation((path, opts) => {
      if (path.endsWith('/messages')) {
        return Promise.resolve({
          messages: [
            ...history,
            {
              id: 'milestone',
              position: 3,
              senderType: 'bot',
              senderBotId: 'bot-2',
              kind: 'progress',
              content: 'The game loop is in place.',
              createdAt: at,
            },
            {
              id: 'blocker',
              position: 4,
              senderType: 'bot',
              senderBotId: 'bot-2',
              kind: 'progress',
              progressBlocker: true,
              content: 'I need a test fixture.',
              createdAt: at,
            },
            {
              id: 'status-check',
              position: 5,
              senderType: 'bot',
              senderBotId: 'bot-1',
              kind: 'team_status',
              content: 'Babu Frik: working',
              createdAt: at,
              teamStatus: {
                botId: 'bot-2',
                name: 'Babu Frik',
                state: 'working',
                workingSince: '2026-11-16T12:00:00Z',
                progress: 'I need a test fixture.',
                progressAt: at,
              },
            },
          ] satisfies ChatMessage[],
          turns: [
            {
              id: 'work-turn',
              botId: 'bot-2',
              status: 'running',
              createdAt: at,
              updatedAt: at,
            },
          ],
        });
      }
      return handler(path, opts);
    });
    show();
    const progress = await screen.findByText('The game loop is in place.');
    expect(
      within(progress.closest('article')!).getByText('Progress'),
    ).toBeVisible();
    expect(screen.getByText('Blocker')).toBeVisible();
    expect(screen.getByText('Status check')).toBeVisible();
    expect(
      screen.getByText('Babu Frik is working in this chat.'),
    ).toBeVisible();
    expect(screen.getByText(/^Last update at/)).toBeVisible();
    expect(screen.getByText('Babu Frik is thinking…')).toBeVisible();
  });

  it.each(['owner', 'viewer'] as const)(
    'uses the sending bot for group memory cards and anchors tool-only turns for a %s',
    async (role) => {
      groupRows = [
        {
          ...group,
          members: [
            ...group.members,
            { type: 'bot', id: 'bot-2', roleText: '', name: 'Nova' },
          ],
        },
      ];
      const createdAt = '2026-11-16T00:00:02Z';
      const memory = {
        memoryId: 'memory-1',
        title: 'Release checklist',
        content: 'Run the smoke tests.',
        mode: 'on_demand',
        tags: [],
        tokenEstimate: 5,
        sourceLabel: 'Chat',
        deleted: false,
      } as const satisfies ChatMessage['memory'];
      const saved: ChatMessage = {
        id: 'saved',
        position: 3,
        senderType: 'bot',
        senderBotId: 'bot-2',
        kind: 'memory',
        content: 'Memory saved',
        createdAt,
        memory,
      };
      request.mockImplementation((path, options) => {
        if (!path.endsWith('/messages')) return handler(path, options);
        return Promise.resolve({
          messages: [
            ...history,
            saved,
            {
              id: 'tool',
              position: 4,
              senderType: 'bot',
              senderBotId: 'bot-2',
              kind: 'tool',
              content: 'Bash\n{}',
              createdAt,
            },
            {
              ...saved,
              id: 'without-bot',
              position: 5,
              senderBotId: undefined,
              content: 'Legacy memory message',
              memory: { ...memory, memoryId: 'memory-2' },
            },
          ] satisfies ChatMessage[],
          turns: [],
        });
      });
      show(role);

      const title = await screen.findByText('Release checklist');
      const card = title.closest('[data-memory-id]')!;
      expect(
        within(card as HTMLElement).getByRole('link', {
          name: 'Open in Knowledge base',
        }),
      ).toHaveAttribute('href', '/bots/bot-2/settings/knowledge');
      const undo = within(card as HTMLElement).queryByRole('button', {
        name: 'Undo',
      });
      if (role === 'owner') expect(undo).toBeEnabled();
      else expect(undo).not.toBeInTheDocument();
      expect(
        within(card.parentElement!).getByRole('button', {
          name: '1 tool step',
        }),
      ).toBeVisible();
      expect(screen.getByText('Legacy memory message')).toBeVisible();
      expect(screen.getAllByText('Release checklist')).toHaveLength(1);
    },
  );

  it('names the requesting bot in both group secret dialogs', async () => {
    groupRows = [
      {
        ...group,
        members: [
          ...group.members,
          { type: 'bot', id: 'bot-2', roleText: '', name: 'Nova' },
        ],
      },
    ];
    request.mockImplementation((path, opts) => {
      if (path.endsWith('/messages')) {
        return Promise.resolve({
          messages: [
            ...history,
            {
              id: 'credential-card',
              position: 3,
              senderType: 'bot',
              senderBotId: 'bot-2',
              kind: 'secret_request',
              content: 'Sign in to GitLab',
              createdAt: '2026-11-16T00:00:02Z',
              secretRequest: {
                secretRequestId: 'request-1',
                kind: 'browser_login',
                status: 'pending',
                adapterId: 'gitlab_web',
                targetOrigin: 'https://gitlab.example.org:443',
                reason: 'Sign in to GitLab',
                brokerEnabled: true,
              },
            } satisfies ChatMessage,
          ],
        });
      }
      return handler(path, opts);
    });
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: 'Provide secret securely' }),
    );
    expect(
      screen.getByRole('dialog', { name: 'Provide secret for Nova' }),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole('button', { name: 'Close secret dialog' }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: 'Open broker takeover' }),
    );
    expect(
      screen.getByRole('dialog', { name: 'Complete login for Nova' }),
    ).toBeInTheDocument();
  });

  it('shows who replied and who is still needed on a group input request', async () => {
    request.mockImplementation((path, opts) => {
      if (path.endsWith('/messages'))
        return Promise.resolve({
          messages: [
            ...history,
            {
              id: 'input-message',
              position: 3,
              senderType: 'bot',
              senderBotId: 'bot-1',
              kind: 'text',
              content: 'Any ideas for a holiday by train?',
              createdAt: '2026-11-16T00:00:02Z',
              inputRequest: {
                state: 'waiting',
                deadline: '2026-11-16T00:05:02Z',
                required: false,
                recipients: [
                  { botId: 'bot-2', name: 'Nova', state: 'replied' },
                  { botId: 'bot-3', name: 'Rook', state: 'waiting' },
                ],
              },
            } satisfies ChatMessage,
          ],
          turns: [],
        });
      return handler(path, opts);
    });
    show();
    await screen.findByText('Collecting input');
    const progress = screen.getByRole('status');
    expect(within(progress).getByText('Nova · Replied')).toBeInTheDocument();
    expect(within(progress).getByText('Rook · Waiting')).toBeInTheDocument();
  });

  it('shows named group turn failures without exposing raw dependency details', async () => {
    request.mockImplementation((path, opts) => {
      if (path.endsWith('/messages'))
        return Promise.resolve({
          messages: history,
          turns: [
            {
              id: 'failed-turn',
              botId: 'bot-1',
              status: 'failed',
              queueAhead: 0,
              errorCode: 'engine.provider_failed',
            },
          ],
        });
      return handler(path, opts);
    });
    show();
    expect(
      await screen.findByText(messages.chat.providerFailed),
    ).toBeInTheDocument();
    expect(
      screen.getByText('Atlas could not finish this turn.'),
    ).toBeInTheDocument();
  });

  it('refreshes every bot status on live events and shows all running bots without tool calls', async () => {
    class FakeSocket {
      static current: FakeSocket;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      close = vi.fn();
      constructor() {
        FakeSocket.current = this;
      }
    }
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.mocked(getAccessToken).mockReturnValue('fixture-token');
    const roster = [bots[0]!, { ...bots[0]!, id: 'bot-2', name: 'Nova' }];
    groupRows = [
      {
        ...group,
        members: [
          ...group.members,
          { type: 'bot', id: 'bot-2', name: 'Nova', roleText: 'reviewer' },
        ],
      },
    ];
    let active = false;
    request.mockImplementation((path, opts) => {
      if (path.endsWith('/bots'))
        return Promise.resolve({
          bots: roster.map((bot) => ({
            ...bot,
            status: active ? 'thinking' : 'idle',
          })),
        });
      if (path.endsWith('/messages'))
        return Promise.resolve({
          messages: history,
          turns: active
            ? roster.map((bot) => ({
                id: `turn-${bot.id}`,
                botId: bot.id,
                status: 'running',
                queueAhead: 0,
                errorCode: '',
              }))
            : [],
        });
      return handler(path, opts);
    });
    show();
    await screen.findByRole('textbox', { name: messages.team.composer });
    active = true;
    act(() =>
      FakeSocket.current.onmessage?.({
        data: JSON.stringify({
          Kind: 'status',
          BotID: 'bot-2',
          TurnID: 'turn-bot-2',
          Status: 'thinking',
        }),
      } as MessageEvent<string>),
    );
    await waitFor(() =>
      expect(
        screen.getAllByText(messages.team.statusThinking).length,
      ).toBeGreaterThanOrEqual(2),
    );
    expect(await screen.findByText('Atlas is thinking…')).toBeInTheDocument();
    expect(screen.getByText('Nova is thinking…')).toBeInTheDocument();
  });

  it('stops an individual running bot and leaves the other stop action available', async () => {
    groupRows = [
      {
        ...group,
        members: [
          ...group.members,
          { type: 'bot', id: 'bot-2', name: 'Nova', roleText: 'reviewer' },
          { type: 'bot', id: 'bot-3', name: 'Quill', roleText: 'writer' },
        ],
      },
    ];
    const stopAtlas =
      '/api/workspaces/ws-1/groups/group-1/turns/turn-atlas/stop';
    let attempts = 0;
    let stopRequested = false;
    request.mockImplementation((path, opts) => {
      if (path === stopAtlas) {
        if (++attempts === 1)
          return Promise.reject(new Error('private dependency detail'));
        stopRequested = true;
        return Promise.resolve(undefined);
      }
      if (path.endsWith('/messages')) {
        return Promise.resolve({
          messages: [
            ...history,
            {
              id: 'nova-tool',
              position: 3,
              senderType: 'bot',
              senderBotId: 'bot-2',
              kind: 'tool',
              content: 'Bash\n{}',
              createdAt: '2026-11-16T00:00:02Z',
              tool: {
                phase: 'call',
                isError: false,
                turnId: 'turn-nova',
                toolCallId: 'nova-call',
              },
            },
          ],
          turns: [
            {
              id: 'queued-atlas',
              botId: 'bot-1',
              status: 'queued',
              queueAhead: 1,
              errorCode: '',
            },
            {
              id: 'turn-atlas',
              botId: 'bot-1',
              status: 'running',
              queueAhead: 0,
              errorCode: '',
              stopRequested,
            },
            {
              id: 'turn-nova',
              botId: 'bot-2',
              status: 'running',
              queueAhead: 0,
              errorCode: '',
            },
            {
              id: 'turn-quill',
              botId: 'bot-3',
              status: 'queued',
              queueAhead: 0,
              errorCode: '',
            },
          ],
        });
      }
      return handler(path, opts);
    });
    show();

    const atlas = await screen.findByRole('button', {
      name: 'Stop response from Atlas',
    });
    expect(screen.getByText(/^Nova is running /)).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Stop response from Nova' }),
    ).toBeEnabled();
    expect(
      screen.queryByRole('button', { name: 'Stop response from Quill' }),
    ).toBeNull();
    fireEvent.click(atlas);
    await screen.findByText(messages.chat.stopTurnFailed);
    expect(screen.queryByText('private dependency detail')).toBeNull();
    await waitFor(() => expect(atlas).toBeEnabled());
    fireEvent.click(atlas);
    expect(
      await screen.findByRole('button', { name: 'Stopping Atlas…' }),
    ).toBeDisabled();
    expect(
      screen.getByRole('button', { name: 'Stop response from Nova' }),
    ).toBeEnabled();
    expect(
      request.mock.calls.filter(([path]) => path.includes('/turns/')),
    ).toEqual([
      [stopAtlas, { method: 'POST' }],
      [stopAtlas, { method: 'POST' }],
    ]);
  });

  it('requires confirmation and leaves the group intact when cancelled', async () => {
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: messages.team.deleteGroup }),
    );
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName('Delete #Launch?');
    expect(
      within(dialog).getByText(messages.team.deleteGroupWarning),
    ).toBeInTheDocument();
    expect(
      request.mock.calls.some(([, options]) => options?.method === 'DELETE'),
    ).toBe(false);
    fireEvent.click(
      within(dialog).getByRole('button', { name: messages.actions.cancel }),
    );
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: /Launch/, level: 1 }),
    ).toBeInTheDocument();
  });

  it.each(['owner', 'admin', 'member'] as const)(
    'lets a %s delete a group and clears the last transcript',
    async (role) => {
      request.mockImplementation((path, options) => {
        if (options?.method === 'DELETE') {
          groupRows = [];
          return Promise.resolve(undefined);
        }
        return handler(path, options);
      });
      show(role);
      fireEvent.click(
        await screen.findByRole('button', { name: messages.team.deleteGroup }),
      );
      fireEvent.click(
        within(screen.getByRole('dialog')).getByRole('button', {
          name: messages.team.deleteGroup,
        }),
      );
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      );
      expect(request).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/groups/group-1',
        { method: 'DELETE' },
      );
      expect(
        screen.queryByRole('heading', { name: /Launch/ }),
      ).not.toBeInTheDocument();
      expect(screen.queryByRole('log')).not.toBeInTheDocument();
      expect(screen.getByText(messages.team.emptyGroups)).toBeInTheDocument();
    },
  );

  it('keeps a failed deletion open with safe feedback and allows retry', async () => {
    let attempts = 0;
    request.mockImplementation((path, options) => {
      if (options?.method === 'DELETE') {
        attempts += 1;
        if (attempts === 1)
          return Promise.reject(
            new ApiClientError(409, 'group.busy', 'private dependency detail'),
          );
        groupRows = [];
        return Promise.resolve(undefined);
      }
      return handler(path, options);
    });
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: messages.team.deleteGroup }),
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.click(
      within(dialog).getByRole('button', { name: messages.team.deleteGroup }),
    );
    expect(
      await within(dialog).findByText(messages.errors.groupBusy),
    ).toBeInTheDocument();
    expect(
      screen.queryByText('private dependency detail'),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: /Launch/, level: 1 }),
    ).toBeInTheDocument();
    fireEvent.click(
      within(dialog).getByRole('button', { name: messages.team.deleteGroup }),
    );
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    );
    expect(attempts).toBe(2);
  });

  it('blocks duplicate deletion and closing while the request is pending', async () => {
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => {
      finish = resolve;
    });
    request.mockImplementation((path, options) =>
      options?.method === 'DELETE' ? pending : handler(path, options),
    );
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: messages.team.deleteGroup }),
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.click(
      within(dialog).getByRole('button', { name: messages.team.deleteGroup }),
    );
    expect(
      within(dialog).getByRole('button', { name: messages.team.deletingGroup }),
    ).toBeDisabled();
    expect(
      within(dialog).getByRole('button', { name: messages.actions.cancel }),
    ).toBeDisabled();
    fireEvent.click(
      within(dialog).getByRole('button', { name: messages.actions.close }),
    );
    expect(dialog).toBeInTheDocument();
    expect(
      request.mock.calls.filter(([, options]) => options?.method === 'DELETE'),
    ).toHaveLength(1);
    groupRows = [];
    finish();
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    );
  });

  it('opens the next group after deleting the selected one', async () => {
    groupRows = [group, { ...group, id: 'group-2', name: 'Ops' }];
    request.mockImplementation((path, options) => {
      if (options?.method === 'DELETE') {
        groupRows = groupRows.filter((row) => row.id !== group.id);
        return Promise.resolve(undefined);
      }
      return handler(path, options);
    });
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: messages.team.deleteGroup }),
    );
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: messages.team.deleteGroup,
      }),
    );
    expect(
      await screen.findByRole('heading', { name: /Ops/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: /Launch/ }),
    ).not.toBeInTheDocument();
  });

  it('hides group deletion from viewers', async () => {
    show('viewer');
    await screen.findByRole('heading', { name: /Launch/ });
    expect(
      screen.queryByRole('button', { name: messages.team.deleteGroup }),
    ).not.toBeInTheDocument();
  });

  it('switches groups when another member deletes the open chat', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
    groupRows = [group, { ...group, id: 'group-2', name: 'Ops' }];
    show('member', client);
    await screen.findByRole('heading', { name: /Launch/, level: 1 });
    groupRows = groupRows.filter((row) => row.id !== group.id);
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['groups', 'ws-1'] });
    });
    expect(
      await screen.findByRole('heading', { name: /Ops/, level: 1 }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: /Launch/, level: 1 }),
    ).not.toBeInTheDocument();
    client.clear();
  });

  it('inserts a mention from the keyboard and leaves an unmatched approval without decision buttons', async () => {
    show();
    const composer = await screen.findByLabelText(messages.team.composer);
    expect(await screen.findByText('3 unread')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Approvals/ })).toHaveAttribute(
      'href',
      '/team/approvals',
    );
    expect(screen.getByText('2 pending')).toBeInTheDocument();
    const log = screen.getByRole('log', { name: messages.team.messages });
    expect(
      await within(log).findByText('Waiting on a protected push'),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: /^(Approve|Deny)/ }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.mention }),
    );
    expect(
      await screen.findByRole('listbox', { name: messages.team.mentionList }),
    ).toBeInTheDocument();
    fireEvent.keyDown(composer, { key: 'ArrowDown' });
    fireEvent.keyDown(composer, { key: 'Enter' });
    expect(composer).toHaveValue('@Sam ');
    expect(
      request.mock.calls.some(
        ([path, options]) =>
          String(path).includes('/messages') && options?.method === 'POST',
      ),
    ).toBe(false);
  });

  it('inserts a mention when an option is clicked', async () => {
    show();
    const composer = await screen.findByLabelText(messages.team.composer);
    fireEvent.click(
      screen.getByRole('button', { name: messages.team.mention }),
    );
    fireEvent.click(await screen.findByRole('option', { name: /@Sam/ }));
    expect(composer).toHaveValue('@Sam ');
  });

  it('offers @all in the mention picker and posts it unchanged', async () => {
    show();
    const composer = await screen.findByLabelText(messages.team.composer);
    fireEvent.change(composer, { target: { value: '@al', selectionStart: 3 } });
    fireEvent.click(await screen.findByRole('option', { name: /@all/ }));
    expect(composer).toHaveValue('@all ');
    fireEvent.submit(composer.closest('form') as HTMLFormElement);
    await waitFor(() =>
      expect(
        request.mock.calls.some(
          ([path, options]) =>
            String(path).endsWith('/messages') &&
            options?.method === 'POST' &&
            (options.body as { content?: string })?.content === '@all ',
        ),
      ).toBe(true),
    );
  });

  it('filters the open transcript and keeps the selected group listed', async () => {
    show();
    const log = await screen.findByRole('log', {
      name: messages.team.messages,
    });
    await waitFor(() => expect(log).toHaveTextContent('Ask @Atlas'));
    fireEvent.change(
      screen.getByRole('searchbox', { name: messages.team.searchLabel }),
      {
        target: { value: 'protected' },
      },
    );
    expect(log).not.toHaveTextContent('Ask @Atlas');
    expect(log).toHaveTextContent('Waiting on a protected push');
    expect(screen.getByRole('button', { name: /Launch/ })).toBeInTheDocument();
  });

  it('hides posting, settings, hire, and new group from a viewer', async () => {
    request.mockImplementation((path, opts) =>
      path.endsWith('/messages')
        ? Promise.resolve({
            messages: history,
            turns: [
              {
                id: 'turn-atlas',
                botId: 'bot-1',
                status: 'running',
                queueAhead: 0,
                errorCode: '',
              },
            ],
          })
        : handler(path, opts),
    );
    show('viewer');
    expect(
      await screen.findByText(messages.team.viewerRead),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: messages.team.send }),
    ).toBeNull();
    expect(screen.queryByRole('switch')).toBeNull();
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(
      screen.queryByRole('button', { name: messages.team.newGroup }),
    ).toBeNull();
    expect(screen.queryByRole('link', { name: messages.team.hire })).toBeNull();
    await waitFor(() =>
      expect(
        screen.getByRole('log', { name: messages.team.messages }),
      ).toHaveTextContent('Waiting on a protected push'),
    );
    expect(await screen.findByText('2 pending')).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: /^(Approve|Deny)/ }),
    ).toBeNull();
    expect(await screen.findByText('Atlas is thinking…')).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Stop response from Atlas' }),
    ).toBeNull();
  });

  it('shows an empty group list without a composer', async () => {
    groupRows = [];
    show();
    expect(
      await screen.findByText(messages.team.emptyGroups),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText(messages.team.composer)).toBeNull();
    expect(
      screen.getByRole('button', { name: messages.team.newGroup }),
    ).toBeInTheDocument();
  });

  it('shows a translated error when creating a group fails', async () => {
    failCreate = true;
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: messages.team.newGroup }),
    );
    const heading = await screen.findByRole('heading', {
      name: messages.team.dialogTitle,
    });
    const dialog = heading.closest('dialog');
    expect(dialog).not.toBeNull();
    const view = within(dialog as HTMLElement);
    const create = view.getByRole('button', { name: messages.team.create });
    expect(create).toBeDisabled();
    fireEvent.change(view.getByLabelText(messages.team.name), {
      target: { value: 'Radar' },
    });
    fireEvent.click(await view.findByRole('button', { name: /Atlas/ }));
    expect(create).toBeEnabled();
    fireEvent.click(create);
    expect(await screen.findByRole('alert')).toHaveTextContent(
      messages.errors.authForbidden,
    );
    expect(screen.queryByText('secret detail')).toBeNull();
    const sent = request.mock.calls.find(
      ([path, options]) =>
        options?.method === 'POST' && String(path).endsWith('/groups'),
    );
    expect(sent?.[1]?.body).toMatchObject({
      name: 'Radar',
      leadBotId: 'bot-1',
      botTalk: true,
      turnLimit: 20,
      answerPolicy: 'mentioned_else_lead',
      askBefore: [],
      members: [
        { type: 'user', id: 'user-1', roleText: '' },
        { type: 'bot', id: 'bot-1', roleText: '' },
      ],
    });
  });

  it('patches one setting without dropping members or ask-before rules', async () => {
    show();
    fireEvent.click(
      await screen.findByRole('switch', { name: messages.team.botsMention }),
    );
    const sent = request.mock.calls.find(
      ([, options]) => options?.method === 'PATCH',
    );
    expect(sent?.[1]?.body).toEqual({
      name: 'Launch',
      goal: 'Ship the fix',
      leadBotId: 'bot-1',
      botTalk: true,
      turnLimit: 20,
      answerPolicy: 'mentioned_else_lead',
      askBefore: [{ kind: 'custom', label: 'ship the release' }],
      budgetLimitMicros: 100,
      members: [
        { type: 'bot', id: 'bot-1', roleText: 'lead' },
        { type: 'user', id: 'user-2', roleText: '' },
        { type: 'user', id: 'user-1', roleText: '' },
      ],
    });
  });

  it('lists a notification by kind and group name', async () => {
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: messages.team.activity }),
    );
    expect(
      await screen.findByText(messages.team.kindMention),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', {
        name: messages.team.openGroup.replace('{name}', 'Launch'),
      }),
    ).toBeInTheDocument();
    expect(screen.queryByText('secret message body')).toBeNull();
  });

  it('scrolls group chat to bottom, pauses while reading older messages, and resets on group switch', async () => {
    class FakeSocket {
      static current: FakeSocket;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      close = vi.fn();
      constructor() {
        FakeSocket.current = this;
      }
      emit(data: unknown) {
        this.onmessage?.({
          data: JSON.stringify(data),
        } as MessageEvent<string>);
      }
    }
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.mocked(getAccessToken).mockReturnValue('fixture-token');

    const secondGroup: Group = {
      ...group,
      id: 'group-2',
      name: 'Ops',
      unread: 0,
    };
    groupRows = [group, secondGroup];

    let group1Messages = [...history];
    const group2Messages: ChatMessage[] = [
      {
        id: 'g2-msg-1',
        position: 1,
        senderType: 'user',
        senderUserId: 'user-2',
        kind: 'text',
        content: 'Ops latest',
        createdAt: '2026-11-16T00:05:00Z',
      },
    ];

    request.mockImplementation(
      (path: string, options?: { method?: string }) => {
        if (path.includes('/groups/group-1/messages')) {
          if (options?.method === 'POST') {
            return Promise.resolve({ id: 'g1-sent' });
          }
          return Promise.resolve({ messages: group1Messages, turns: [] });
        }
        if (path.includes('/groups/group-2/messages')) {
          return Promise.resolve({ messages: group2Messages, turns: [] });
        }
        return handler(path, options);
      },
    );

    let currentScrollHeight = 1000;
    // Layout effects run on mount, before findByRole returns the element.
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(
      () => currentScrollHeight,
    );
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(400);

    show();

    const log = await screen.findByRole('log', {
      name: messages.team.messages,
    });

    await waitFor(() => expect(log).toHaveTextContent('Ask @Atlas'));
    await waitFor(() =>
      expect(FakeSocket.current?.onmessage).toBeTypeOf('function'),
    );
    expect(log.scrollTop).toBe(1000);

    // Negative path: user scrolled up, new incoming message must not jump to bottom.
    log.scrollTop = 120;
    fireEvent.scroll(log);
    group1Messages = [
      ...group1Messages,
      {
        id: 'message-3',
        position: 3,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'text',
        content: 'New reply from Atlas',
        createdAt: '2026-11-16T00:00:02Z',
      },
    ];
    currentScrollHeight = 1200;
    FakeSocket.current.emit({ Kind: 'message' });
    expect(await screen.findByText('New reply from Atlas')).toBeInTheDocument();
    expect(log.scrollTop).toBe(120);

    // Sending a message re-pins to the bottom.
    fireEvent.change(screen.getByLabelText(messages.team.composer), {
      target: { value: 'Looks good' },
    });
    fireEvent.submit(
      screen.getByRole('button', { name: messages.team.send }).closest('form')!,
    );
    await waitFor(() => expect(log.scrollTop).toBe(1200));

    // Switching to another group resets scroll-to-bottom even if user was scrolled up.
    log.scrollTop = 100;
    fireEvent.scroll(log);
    currentScrollHeight = 900;
    fireEvent.click(screen.getByRole('button', { name: /Ops/ }));
    expect(await screen.findByText('Ops latest')).toBeInTheDocument();
    expect(log.scrollTop).toBe(900);
    vi.unstubAllGlobals();
  });

  it('paginates group history to the beginning via button and scroll-to-top, backfills tool-heavy pages, renders day dividers, and shows date tooltips', async () => {
    const oldIso = new Date(2026, 9, 12, 8, 15, 0).toISOString();
    const today = new Date();
    today.setHours(11, 5, 0, 0);
    const todayIso = today.toISOString();

    // Latest 50 rows contain 48 tool rows and only 2 visible messages -> triggers initial backfill.
    const latestToolRows: ChatMessage[] = Array.from(
      { length: 48 },
      (_, index) => ({
        id: `grp-tool-${index + 53}`,
        position: index + 53,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'tool',
        content: 'click\n{"x":10,"y":20}',
        tool: {
          phase: 'call',
          isError: false,
          turnId: 'turn-recent',
          toolCallId: `tc-${index}`,
        },
        createdAt: todayIso,
      }),
    );
    const latestVisible: ChatMessage[] = [
      {
        id: 'grp-101',
        position: 101,
        senderType: 'user',
        senderUserId: 'user-1',
        kind: 'text',
        content: 'Latest human note',
        createdAt: todayIso,
      },
      {
        id: 'grp-102',
        position: 102,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'text',
        content: 'Latest bot reply',
        createdAt: todayIso,
      },
    ];
    const backfillPage: ChatMessage[] = Array.from({ length: 20 }, (_, i) => ({
      id: `grp-mid-${i + 33}`,
      position: i + 33,
      senderType: 'bot',
      senderBotId: 'bot-1',
      kind: 'text',
      content: `Backfilled message ${i + 33}`,
      createdAt: todayIso,
    }));
    const oldestPage: ChatMessage[] = [
      {
        id: 'grp-001',
        position: 1,
        senderType: 'user',
        senderUserId: 'user-2',
        kind: 'text',
        content: 'Very first group message',
        createdAt: oldIso,
      },
    ];

    let failOldest = false;
    request.mockImplementation(
      (path: string, options?: { method?: string }) => {
        if (path.includes('/groups/group-1/messages?before=53')) {
          return Promise.resolve({
            messages: backfillPage,
            turns: [],
            hasMore: true,
          });
        }
        if (path.includes('/groups/group-1/messages?before=33')) {
          if (failOldest) {
            return Promise.reject(new Error('history error'));
          }
          return Promise.resolve({
            messages: oldestPage,
            turns: [],
            hasMore: false,
          });
        }
        if (path.includes('/groups/group-1/messages')) {
          return Promise.resolve({
            messages: [...latestToolRows, ...latestVisible],
            turns: [],
            hasMore: true,
          });
        }
        return handler(path, options);
      },
    );

    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(
      function (this: HTMLElement) {
        return this.textContent?.includes('Very first group message')
          ? 1800
          : 1000;
      },
    );
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(400);

    show();

    const log = await screen.findByRole('log', {
      name: messages.team.messages,
    });
    // Initial backfill automatically fetched before=53 so Backfilled message 33 is visible immediately.
    expect(
      await screen.findByText('Backfilled message 33'),
    ).toBeInTheDocument();
    expect(screen.getByText('Latest bot reply')).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: messages.chat.older }),
    ).toBeInTheDocument();

    // Negative path: error loading oldest page shows loadFailed alert without losing current history.
    failOldest = true;
    fireEvent.click(screen.getByRole('button', { name: messages.chat.older }));
    expect(
      await screen.findByText(messages.team.loadFailed),
    ).toBeInTheDocument();
    expect(screen.getByText('Backfilled message 33')).toBeInTheDocument();

    // Scroll to top auto-loads the remaining oldest page and hides the button at the beginning.
    failOldest = false;
    log.scrollTop = 0;
    fireEvent.scroll(log);

    expect(
      await screen.findByText('Very first group message'),
    ).toBeInTheDocument();
    expect(log.scrollTop).toBe(800);
    expect(
      screen.queryByRole('button', { name: messages.chat.older }),
    ).not.toBeInTheDocument();

    // Day dividers reflect both the older calendar date and Today.
    const expectedOldDay = formatDayDividerLabel(oldIso, 'en', {
      today: messages.chat.todayDivider,
      yesterday: messages.chat.yesterdayDivider,
    });
    const separators = screen.getAllByRole('separator');
    expect(separators).toHaveLength(2);
    expect(separators[0]).toHaveTextContent(expectedOldDay);
    expect(separators[1]).toHaveTextContent(messages.chat.todayDivider);

    // Hovering over the oldest message's timestamp displays the full date/time tooltip.
    const oldTimeEl = screen.getByText(formatTimeLabel(oldIso, 'en'));
    fireEvent.mouseEnter(oldTimeEl);
    expect(screen.getByRole('tooltip')).toHaveTextContent(
      formatFullDateTime(oldIso, 'en'),
    );
  });
});
