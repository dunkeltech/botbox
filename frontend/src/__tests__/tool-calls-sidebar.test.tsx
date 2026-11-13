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
import { BotWorkspaceView } from '@/components/workspace/BotWorkspaceView';
import { QueryProvider } from '@/components/providers/QueryProvider';
import {
  getAccessToken,
  getBot,
  getBotEnvironment,
  getWorkspaceGuardrails,
  listBotSecrets,
  listDesktopActions,
  requestJson,
} from '@/lib/api-client';
import type { Bot, WorkspaceMember } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import messages from '../../messages/en.json';

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => '/bots/bot-1',
  useParams: () => ({ botId: 'bot-1' }),
}));

vi.mock('@/context/AuthContext', () => {
  // The selected workspace keeps its identity across local UI updates.
  const auth = {
    user: { id: 'user-1', email: 'jan@example.com', displayName: 'Jan' },
    activeWorkspace: {
      id: 'ws-1',
      name: 'dunkel.Tech',
      slug: 'dunkel-tech',
      role: 'owner',
    },
  };
  return { useAuth: () => auth };
});

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return {
    ...actual,
    requestJson: vi.fn(),
    getAccessToken: vi.fn(() => null),
    getBot: vi.fn(),
    getBotEnvironment: vi.fn(),
    getWorkspaceGuardrails: vi.fn(),
    listBotSecrets: vi.fn(),
    listDesktopActions: vi.fn(),
  };
});

const request = vi.mocked(requestJson);
type Group = components['schemas']['Group'];
type ChatMessage = components['schemas']['ChatMessage'];

const bots: Bot[] = [
  {
    id: 'bot-1',
    workspaceId: 'ws-1',
    name: 'Mace Windu',
    description: 'Maintains GitLab workflows',
    avatarSeed: 'mace-windu',
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
    role: 'owner',
    joinedAt: '2026-11-15T00:00:00Z',
  },
];

const group: Group = {
  id: 'group-1',
  name: 'gitlab-setup',
  goal: 'Configure glab CLI',
  leadBotId: 'bot-1',
  botTalk: false,
  turnLimit: 20,
  answerPolicy: 'mentioned_else_lead',
  askBefore: [],
  budgetLimitMicros: 100,
  paused: false,
  pauseReason: '',
  turnsSinceHuman: 0,
  unread: 0,
  members: [
    { type: 'bot', id: 'bot-1', roleText: 'lead', name: 'Mace Windu' },
    { type: 'user', id: 'user-1', roleText: '', name: 'Jan' },
  ],
};

const sampleMessages: ChatMessage[] = [
  {
    id: 'msg-user-1',
    position: 1,
    senderType: 'user',
    senderUserId: 'user-1',
    kind: 'text',
    content: 'Please test https://gitlab.inovex.de',
    createdAt: '2026-11-17T14:23:00Z',
  },
  {
    id: 'msg-tool-call-1',
    position: 2,
    senderType: 'bot',
    senderBotId: 'bot-1',
    kind: 'tool',
    content: 'Bash\n{"command":"curl -I https://gitlab.inovex.de"}',
    createdAt: '2026-11-17T14:23:01Z',
    tool: {
      turnId: 'turn-1',
      toolCallId: 'tc-1',
      phase: 'call',
      isError: false,
    },
  },
  {
    id: 'msg-tool-res-1',
    position: 3,
    senderType: 'bot',
    senderBotId: 'bot-1',
    kind: 'tool',
    content: 'HTTP 200 OK',
    createdAt: '2026-11-17T14:23:02Z',
    tool: {
      turnId: 'turn-1',
      toolCallId: 'tc-1',
      phase: 'result',
      isError: false,
    },
  },
  {
    id: 'msg-tool-call-2',
    position: 4,
    senderType: 'bot',
    senderBotId: 'bot-1',
    kind: 'tool',
    content: 'ToolSearch\n{}',
    createdAt: '2026-11-17T14:23:02.5Z',
    tool: {
      turnId: 'turn-1',
      toolCallId: 'tc-2',
      phase: 'call',
      isError: false,
    },
  },
  {
    id: 'msg-tool-res-2',
    position: 5,
    senderType: 'bot',
    senderBotId: 'bot-1',
    kind: 'tool',
    content: 'blocked',
    createdAt: '2026-11-17T14:23:03Z',
    tool: {
      turnId: 'turn-1',
      toolCallId: 'tc-2',
      phase: 'result',
      isError: true,
      errorCode: 'egress.blocked',
    },
  },
  {
    id: 'msg-bot-1',
    position: 6,
    senderType: 'bot',
    senderBotId: 'bot-1',
    kind: 'text',
    content: 'HTTPS access to gitlab.inovex.de is configured.',
    createdAt: '2026-11-17T14:23:04Z',
  },
];

function renderWithIntl(ui: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryProvider>{ui}</QueryProvider>
    </NextIntlClientProvider>,
  );
}

beforeEach(() => {
  vi.mocked(getAccessToken).mockReturnValue(null);
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === '(min-width: 1280px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('Tool calls sidebar (Proposal 1)', () => {
  it('hides inline tool lines in Team group chat and opens the Tool calls sidebar tab on chip click', async () => {
    request.mockImplementation((path: string) => {
      if (path.endsWith('/groups')) return Promise.resolve([group]);
      if (path.endsWith('/bots')) return Promise.resolve({ bots });
      if (path.endsWith('/members'))
        return Promise.resolve({ members: humans });
      if (path.endsWith('/approvals'))
        return Promise.resolve({ approvals: [] });
      if (path.endsWith('/notifications')) {
        return Promise.resolve({ notifications: [] });
      }
      if (path.includes('/groups/group-1/messages')) {
        return Promise.resolve({
          messages: sampleMessages,
          turns: [
            { id: 'turn-1', status: 'completed', queueAhead: 0, errorCode: '' },
          ],
        });
      }
      return Promise.resolve({});
    });

    renderWithIntl(
      <TeamScreen
        workspaceId="ws-1"
        userId="user-1"
        displayName="Jan"
        role="owner"
        workspaceName="dunkel.Tech"
      />,
    );

    expect(
      await screen.findByText(
        'HTTPS access to gitlab.inovex.de is configured.',
      ),
    ).toBeInTheDocument();

    // Tool rows must NOT appear inline in the chat transcript.
    expect(screen.queryByText('Using Bash')).toBeNull();
    expect(screen.queryByText('Preparing desktop tools')).toBeNull();

    // Default sidebar tab remains Details until the user clicks the chip or tab.
    const aside = screen.getByRole('complementary', { name: 'Details' });
    expect(within(aside).getByText('Configure glab CLI')).toBeInTheDocument();

    const chip = screen.getByRole('button', { name: /2 tool steps/i });
    fireEvent.click(chip);

    const toolAside = await screen.findByRole('complementary', {
      name: 'Tool calls trace',
    });
    expect(within(toolAside).getByText('Using Bash')).toBeInTheDocument();
    expect(
      within(toolAside).getByText('Preparing desktop tools'),
    ).toBeInTheDocument();

    // Steps start collapsed by default; parameters and error details appear only after expanding.
    expect(
      within(toolAside).queryByText('curl -I https://gitlab.inovex.de'),
    ).toBeNull();
    expect(
      within(toolAside).queryByText('Error code: egress.blocked'),
    ).toBeNull();

    const expandButtons = within(toolAside).getAllByRole('button', {
      name: 'Show step details',
    });
    expect(expandButtons).toHaveLength(2);
    fireEvent.click(expandButtons[0]!);
    expect(within(toolAside).getByText('Parameters')).toBeInTheDocument();
    expect(
      within(toolAside).getByText('curl -I https://gitlab.inovex.de'),
    ).toBeInTheDocument();
    expect(within(toolAside).getByText('HTTP 200 OK')).toBeInTheDocument();

    fireEvent.click(expandButtons[1]!);
    expect(
      within(toolAside).getByText('Error code: egress.blocked'),
    ).toBeInTheDocument();

    // Filter to errors only keeps the failed step and hides the successful one.
    fireEvent.click(
      within(toolAside).getByRole('button', { name: 'Errors (1)' }),
    );
    expect(within(toolAside).queryByText('Using Bash')).toBeNull();
    expect(
      within(toolAside).getByText('Preparing desktop tools'),
    ).toBeInTheDocument();
  });

  it('shows parameters and the last 5 lines of tool output in BotWorkspaceView when expanded', async () => {
    const maceBot = bots[0]!;
    vi.mocked(getBot).mockResolvedValue(maceBot);
    vi.mocked(getBotEnvironment).mockResolvedValue({
      botId: 'bot-1',
      workspaceId: 'ws-1',
      envId: 'env-1',
      driver: 'docker',
      status: 'stopped',
      controlHolder: null,
      controlNote: '',
      watchers: [],
      stats: {
        cpuPercent: 0,
        memoryUsedMiB: 0,
        memoryLimitMiB: 2048,
        volumeUsedMiB: 0,
        volumeLimitMiB: 10240,
      },
      image: 'botbox/desktop:dev',
      cpuMillis: 2000,
      memoryMiB: 2048,
      volumeRef: 'vol-1',
      startedAt: null,
      updatedAt: '2026-11-15T00:00:00Z',
    });
    vi.mocked(listBotSecrets).mockResolvedValue({ secrets: [] });
    vi.mocked(getWorkspaceGuardrails).mockResolvedValue(null as never);
    vi.mocked(listDesktopActions).mockResolvedValue({
      actions: [],
      screenshotRetentionDays: 7,
    });

    const longMessages: ChatMessage[] = [
      {
        id: 'msg-u-long',
        position: 1,
        senderType: 'user',
        senderUserId: 'user-1',
        kind: 'text',
        content: 'Run tests',
        createdAt: '2026-11-19T09:00:00Z',
      },
      {
        id: 'msg-tc-long',
        position: 2,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'tool',
        content:
          'Bash\n{"command":"go test ./internal/api/...","workdir":"/workspace/botbox/backend"}',
        createdAt: '2026-11-19T09:00:01Z',
        tool: {
          turnId: 'turn-long',
          toolCallId: 'tc-long',
          phase: 'call',
          isError: false,
        },
      },
      {
        id: 'msg-tr-long',
        position: 3,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'tool',
        content: [
          'hidden line 1',
          'hidden line 2',
          'hidden line 3',
          '=== RUN TestOne',
          '--- PASS: TestOne (0.12s)',
          '=== RUN TestTwo',
          '--- PASS: TestTwo (0.08s)',
          'ok botbox/internal/api 0.40s',
        ].join('\n'),
        createdAt: '2026-11-19T09:00:02Z',
        tool: {
          turnId: 'turn-long',
          toolCallId: 'tc-long',
          phase: 'result',
          isError: false,
        },
      },
      {
        id: 'msg-b-long',
        position: 4,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'text',
        content: 'All tests passed.',
        createdAt: '2026-11-19T09:00:03Z',
      },
    ];

    request.mockImplementation((path: string) => {
      if (path.endsWith('/runtime')) return Promise.resolve({ paused: false });
      if (path.endsWith('/dm')) {
        return Promise.resolve({ id: 'conv-1', botId: 'bot-1' });
      }
      if (path.includes('/conversations/conv-1/messages')) {
        return Promise.resolve({
          messages: longMessages,
          turns: [
            {
              id: 'turn-long',
              status: 'completed',
              queueAhead: 0,
              errorCode: '',
            },
          ],
        });
      }
      if (path.includes('/budgets')) {
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      }
      return Promise.resolve({});
    });

    renderWithIntl(<BotWorkspaceView />);

    const chip = await screen.findByRole('button', { name: /1 tool step/i });
    fireEvent.click(chip);

    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: /Tool calls/i }),
      ).toHaveAttribute('aria-pressed', 'true');
    });

    // Collapsed initially.
    expect(screen.queryByText('go test ./internal/api/...')).toBeNull();
    expect(screen.queryByText('ok botbox/internal/api 0.40s')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Show step details' }));

    expect(screen.getByText('Parameters')).toBeInTheDocument();
    expect(screen.getByText('go test ./internal/api/...')).toBeInTheDocument();
    expect(screen.getByText('/workspace/botbox/backend')).toBeInTheDocument();
    expect(screen.getByText('Output · Last 5 lines')).toBeInTheDocument();
    expect(screen.getByText('… 3 earlier lines hidden')).toBeInTheDocument();
    expect(screen.queryByText('hidden line 1')).toBeNull();
    expect(
      screen.getByText(/ok botbox\/internal\/api 0\.40s/),
    ).toBeInTheDocument();
  });

  it('preserves Details tab in Team DM until clicked and closes mobile tool dialog on Escape', async () => {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: false,
        media: '(min-width: 1280px)',
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });

    request.mockImplementation((path: string) => {
      if (path.endsWith('/groups')) return Promise.resolve([]);
      if (path.endsWith('/bots')) return Promise.resolve({ bots });
      if (path.endsWith('/members'))
        return Promise.resolve({ members: humans });
      if (path.endsWith('/approvals'))
        return Promise.resolve({ approvals: [] });
      if (path.endsWith('/notifications')) {
        return Promise.resolve({ notifications: [] });
      }
      if (path.endsWith('/dm')) {
        return Promise.resolve({ id: 'conv-1', botId: 'bot-1' });
      }
      if (path.includes('/conversations/conv-1/messages')) {
        return Promise.resolve({
          messages: sampleMessages,
          turns: [
            { id: 'turn-1', status: 'completed', queueAhead: 0, errorCode: '' },
          ],
        });
      }
      if (path.includes('/budgets')) {
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      }
      return Promise.resolve({});
    });

    renderWithIntl(
      <TeamScreen
        workspaceId="ws-1"
        userId="user-1"
        displayName="Jan"
        role="owner"
        workspaceName="dunkel.Tech"
      />,
    );

    fireEvent.click(await screen.findByRole('button', { name: /Mace Windu/i }));
    expect(
      await screen.findByText(
        'HTTPS access to gitlab.inovex.de is configured.',
      ),
    ).toBeInTheDocument();

    // Narrow viewport opens a modal dialog on chip click and closes on Escape.
    const chip = screen.getByRole('button', { name: /2 tool steps/i });
    fireEvent.click(chip);
    const dialog = await screen.findByRole('dialog', {
      name: 'Tool calls trace',
    });
    expect(within(dialog).getByText('Using Bash')).toBeInTheDocument();

    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => {
      expect(
        screen.queryByRole('dialog', { name: 'Tool calls trace' }),
      ).toBeNull();
    });
  });

  it('switches to the Tool calls tab in BotWorkspaceView when clicking the chat turn chip', async () => {
    const maceBot = bots[0]!;
    vi.mocked(getBot).mockResolvedValue(maceBot);
    vi.mocked(getBotEnvironment).mockResolvedValue({
      botId: 'bot-1',
      workspaceId: 'ws-1',
      envId: 'env-1',
      driver: 'docker',
      status: 'stopped',
      controlHolder: null,
      controlNote: '',
      watchers: [],
      stats: {
        cpuPercent: 0,
        memoryUsedMiB: 0,
        memoryLimitMiB: 2048,
        volumeUsedMiB: 0,
        volumeLimitMiB: 10240,
      },
      image: 'botbox/desktop:dev',
      cpuMillis: 2000,
      memoryMiB: 2048,
      volumeRef: 'vol-1',
      startedAt: null,
      updatedAt: '2026-11-15T00:00:00Z',
    });
    vi.mocked(listBotSecrets).mockResolvedValue({ secrets: [] });
    vi.mocked(getWorkspaceGuardrails).mockResolvedValue(null as never);
    vi.mocked(listDesktopActions).mockResolvedValue({
      actions: [],
      screenshotRetentionDays: 7,
    });
    request.mockImplementation((path: string) => {
      if (path.endsWith('/runtime')) return Promise.resolve({ paused: false });
      if (path.endsWith('/dm')) {
        return Promise.resolve({ id: 'conv-1', botId: 'bot-1' });
      }
      if (path.includes('/conversations/conv-1/messages')) {
        return Promise.resolve({
          messages: sampleMessages,
          turns: [
            { id: 'turn-1', status: 'completed', queueAhead: 0, errorCode: '' },
          ],
        });
      }
      if (path.includes('/budgets')) {
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      }
      return Promise.resolve({});
    });

    renderWithIntl(<BotWorkspaceView />);

    expect(
      await screen.findByText(
        'HTTPS access to gitlab.inovex.de is configured.',
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText('Using Bash')).toBeNull();

    const chip = await screen.findByRole('button', { name: /2 tool steps/i });
    fireEvent.click(chip);

    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: /Tool calls/i }),
      ).toHaveAttribute('aria-pressed', 'true');
    });
    expect(screen.getByText('Using Bash')).toBeInTheDocument();
    expect(screen.getByText('Preparing desktop tools')).toBeInTheDocument();
  });

  it('renders inline guardrail notices, specialized guardrail tool trace cards, and header badge in BotWorkspaceView', async () => {
    const maceBot = bots[0]!;
    vi.mocked(getBot).mockResolvedValue(maceBot);
    vi.mocked(getBotEnvironment).mockResolvedValue({
      botId: 'bot-1',
      workspaceId: 'ws-1',
      envId: 'env-1',
      driver: 'docker',
      status: 'stopped',
      controlHolder: null,
      controlNote: '',
      watchers: [],
      stats: {
        cpuPercent: 0,
        memoryUsedMiB: 0,
        memoryLimitMiB: 2048,
        volumeUsedMiB: 0,
        volumeLimitMiB: 10240,
      },
      image: 'botbox/desktop:dev',
      cpuMillis: 2000,
      memoryMiB: 2048,
      volumeRef: 'vol-1',
      startedAt: null,
      updatedAt: '2026-11-15T00:00:00Z',
    });
    vi.mocked(listBotSecrets).mockResolvedValue({ secrets: [] });
    vi.mocked(getWorkspaceGuardrails).mockResolvedValue({
      policy: {
        workspaceId: 'ws-1',
        enabled: true,
        failureMode: 'fail_closed',
        secrets: { enabled: true, direction: 'both', action: 'block' },
        pii: {
          enabled: true,
          direction: 'both',
          action: 'redact',
          entities: ['email'],
        },
        promptInjection: { enabled: true, direction: 'input', action: 'audit' },
        revision: 1,
        updatedAt: '2026-11-19T00:00:00Z',
      },
      rules: [],
      summary24h: {
        totalEvaluations: 5,
        redactedCount: 1,
        blockedCount: 1,
        auditedCount: 0,
      },
      activeRuleCount: 3,
      coveredProviders: ['anthropic', 'openai'],
    });
    vi.mocked(listDesktopActions).mockResolvedValue({
      actions: [],
      screenshotRetentionDays: 7,
    });

    const guardrailMessages = [
      {
        id: 'msg-user-gr',
        position: 1,
        senderType: 'user',
        senderUserId: 'user-1',
        kind: 'text',
        content: 'Analyze this payload',
        createdAt: '2026-11-19T00:00:00Z',
      },
      {
        id: 'msg-gr-card-1',
        position: 2,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'guardrail',
        content: '1 match(es) masked (email)',
        createdAt: '2026-11-19T00:00:01Z',
        guardrail: {
          eventId: 'ev-gr-1',
          turnId: 'turn-gr',
          direction: 'input',
          action: 'redacted',
          provider: 'anthropic',
          model: 'claude-sonnet-4-5',
          routePath: '/v1/messages',
          statusCode: 200,
          latencyMs: 4,
          ruleSlugs: ['pii-protection'],
          matchLabels: ['email'],
          matchCount: 1,
          summary: '1 match(es) masked (email)',
        },
      },
      {
        id: 'msg-gr-tool-1',
        position: 3,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'tool',
        content: '1 match(es) masked (email)',
        createdAt: '2026-11-19T00:00:01Z',
        tool: {
          turnId: 'turn-gr',
          toolCallId: 'guardrail-ev-gr-1',
          phase: 'result',
          isError: false,
          guardrail: {
            eventId: 'ev-gr-1',
            turnId: 'turn-gr',
            direction: 'input',
            action: 'redacted',
            provider: 'anthropic',
            model: 'claude-sonnet-4-5',
            routePath: '/v1/messages',
            statusCode: 200,
            latencyMs: 4,
            ruleSlugs: ['pii-protection'],
            matchLabels: ['email'],
            matchCount: 1,
            summary: '1 match(es) masked (email)',
          },
        },
      },
      {
        id: 'msg-gr-card-2',
        position: 4,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'guardrail',
        content: '1 policy violation(s) detected · response rejected',
        createdAt: '2026-11-19T00:00:03Z',
        guardrail: {
          eventId: 'ev-gr-2',
          turnId: 'turn-gr',
          direction: 'output',
          action: 'blocked',
          provider: 'anthropic',
          model: 'claude-sonnet-4-5',
          routePath: '/v1/messages',
          statusCode: 403,
          latencyMs: 3,
          ruleSlugs: ['secrets-leak-guard'],
          matchLabels: ['api_key'],
          matchCount: 1,
          summary: '1 policy violation(s) detected · response rejected',
        },
      },
      {
        id: 'msg-gr-tool-2',
        position: 5,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'tool',
        content: '1 policy violation(s) detected · response rejected',
        createdAt: '2026-11-19T00:00:03Z',
        tool: {
          turnId: 'turn-gr',
          toolCallId: 'guardrail-ev-gr-2',
          phase: 'result',
          isError: true,
          errorCode: 'guardrail_blocked',
          guardrail: {
            eventId: 'ev-gr-2',
            turnId: 'turn-gr',
            direction: 'output',
            action: 'blocked',
            provider: 'anthropic',
            model: 'claude-sonnet-4-5',
            routePath: '/v1/messages',
            statusCode: 403,
            latencyMs: 3,
            ruleSlugs: ['secrets-leak-guard'],
            matchLabels: ['api_key'],
            matchCount: 1,
            summary: '1 policy violation(s) detected · response rejected',
          },
        },
      },
      {
        id: 'msg-gr-reply',
        position: 6,
        senderType: 'bot',
        senderBotId: 'bot-1',
        kind: 'text',
        content: 'Done after guardrail intervention.',
        createdAt: '2026-11-19T00:00:03Z',
      },
    ];

    request.mockImplementation((path: string) => {
      if (path.endsWith('/runtime')) return Promise.resolve({ paused: false });
      if (path.endsWith('/dm')) {
        return Promise.resolve({ id: 'conv-1', botId: 'bot-1' });
      }
      if (path.includes('/conversations/conv-1/messages')) {
        return Promise.resolve({
          messages: guardrailMessages,
          turns: [
            {
              id: 'turn-gr',
              status: 'failed',
              queueAhead: 0,
              errorCode: 'engine.guardrail_blocked',
            },
          ],
        });
      }
      if (path.includes('/budgets')) {
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      }
      return Promise.resolve({});
    });

    renderWithIntl(<BotWorkspaceView />);

    const headerChip = await screen.findByRole('link', {
      name: 'LLM guardrails · 3 active',
    });
    expect(headerChip).toHaveAttribute('href', '/settings/guardrails');

    expect(
      await screen.findByText('LLM guardrail redacted prompt'),
    ).toBeInTheDocument();
    expect(
      screen.getByText('LLM response blocked by workspace guardrail'),
    ).toBeInTheDocument();
    expect(
      screen.queryByText('LLM proxy guardrail · Input redacted'),
    ).toBeNull();

    const chip = await screen.findByRole('button', {
      name: '2 tool steps · 2.0 s · 1 guardrail redact · 1 guardrail block',
    });
    fireEvent.click(chip);

    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: 'Tool calls' }),
      ).toHaveAttribute('aria-pressed', 'true');
    });
    expect(
      screen.getByText('LLM proxy guardrail · Input redacted'),
    ).toBeInTheDocument();
    expect(
      screen.getByText('LLM proxy guardrail · Output blocked'),
    ).toBeInTheDocument();
    expect(screen.getByText('pii-protection')).toBeInTheDocument();
    expect(screen.getByText('secrets-leak-guard')).toBeInTheDocument();
    expect(screen.getByText('4 ms')).toBeInTheDocument();
    expect(screen.getByText('3 ms')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Errors (1)' }));
    expect(
      screen.queryByText('LLM proxy guardrail · Input redacted'),
    ).toBeNull();
    expect(
      screen.getByText('LLM proxy guardrail · Output blocked'),
    ).toBeInTheDocument();
  });
});
