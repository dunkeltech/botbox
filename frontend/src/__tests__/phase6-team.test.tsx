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
import { TeamScreen } from '@/components/team/TeamScreen';
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

function show(role: 'member' | 'viewer' = 'member') {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryProvider>
        <TeamScreen
          workspaceId="ws-1"
          userId="user-1"
          displayName="Jan"
          role={role}
          workspaceName="dunkel.Tech"
        />
      </QueryProvider>
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
});

describe('Phase 6 team chat', () => {
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
    expect(log).toHaveTextContent('Waiting on a protected push');
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
});
