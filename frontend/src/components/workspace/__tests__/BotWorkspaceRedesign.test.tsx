import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const rfbInstances = vi.hoisted(
  () =>
    [] as Array<{
      viewOnly: boolean;
      scaleViewport: boolean;
      disconnect: ReturnType<typeof vi.fn>;
      addEventListener: ReturnType<typeof vi.fn>;
      emit: (event: string) => void;
    }>,
);

vi.mock('@novnc/novnc', () => ({
  default: class FakeRFB {
    viewOnly = false;
    scaleViewport = false;
    private listeners: Record<string, Array<() => void>> = {};
    disconnect = vi.fn();
    addEventListener = vi.fn((type: string, cb: () => void) => {
      this.listeners[type] ??= [];
      this.listeners[type].push(cb);
    });
    emit = (type: string) => {
      for (const cb of this.listeners[type] ?? []) {
        cb();
      }
    };
    constructor() {
      rfbInstances.push(this);
      queueMicrotask(() => {
        this.emit('connect');
      });
    }
  },
}));

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

import { QueryProvider } from '@/components/providers/QueryProvider';
import { BotWorkspaceView } from '@/components/workspace/BotWorkspaceView';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  EnvironmentStateResponse,
  StreamTicketResponse,
  WorkspaceRole,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../../../messages/en.json';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function makeWorkspace(role: WorkspaceRole = 'owner'): WorkspaceSummary {
  return {
    id: 'ws-1',
    name: 'dunkel.Tech',
    slug: 'dunkel-tech',
    defaultDriver: 'docker',
    defaultLocale: 'en',
    role,
    createdAt: '2026-11-14T12:00:00Z',
  };
}

function makeSession(role: WorkspaceRole = 'owner'): AuthSessionResponse {
  const ws = makeWorkspace(role);
  return {
    user: {
      id: 'user-1',
      email: 'jan@dunkel.tech',
      displayName: 'Jan Gehring',
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces: [ws],
    activeWorkspace: ws,
    accessToken: 'jwt-workspace-test',
  };
}

function makeBot(): Bot {
  return {
    id: 'bot-1',
    workspaceId: 'ws-1',
    ownerUserId: 'user-1',
    name: 'Atlas',
    description: 'Verifies release pipelines on a Linux desktop.',
    avatarSeed: 'atlas-005a',
    llmMode: 'cli',
    llmProvider: 'anthropic',
    llmModel: 'claude-sonnet-4-20250514',
    driverType: 'docker',
    replyLanguage: 'en',
    status: 'action',
    mood: 'action',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
  };
}

function makeEnv(
  overrides: Partial<EnvironmentStateResponse> = {},
): EnvironmentStateResponse {
  return {
    botId: 'bot-1',
    workspaceId: 'ws-1',
    driver: 'docker',
    envId: 'env-1',
    status: 'running',
    image: 'botbox/sandbox:dev',
    cpuMillis: 2000,
    memoryMiB: 4096,
    volumeRef: 'vol-atlas',
    controlHolder: null,
    controlNote: '',
    watchers: [
      {
        userId: 'user-1',
        displayName: 'Jan Gehring',
      },
    ],
    stats: {
      cpuPercent: 24,
      memoryUsedMiB: 1024,
      memoryLimitMiB: 4096,
      volumeUsedMiB: 512,
      volumeLimitMiB: 10240,
    },
    startedAt: '2026-11-18T12:00:00Z',
    updatedAt: '2026-11-18T12:01:00Z',
    ...overrides,
  };
}

function renderWorkspace() {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      <QueryProvider>
        <AuthProvider>
          <BotWorkspaceView />
        </AuthProvider>
      </QueryProvider>
    </NextIntlClientProvider>,
  );
}

function stubWorkspaceFetch(options?: {
  role?: WorkspaceRole;
  env?: EnvironmentStateResponse;
  holdBotFetch?: Promise<Response>;
  botStatus?: number;
}) {
  const role = options?.role ?? 'owner';
  const session = makeSession(role);
  const bot = makeBot();
  let env = options?.env ?? makeEnv();

  const fetchMock = vi.fn(
    (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method = (init?.method ?? 'GET').toUpperCase();

      if (url === '/api/setup/status') {
        return Promise.resolve(
          jsonResponse(
            { error: { code: 'setup.already_completed', message: 'done' } },
            404,
          ),
        );
      }
      if (url === '/api/auth/refresh') {
        return Promise.resolve(jsonResponse(session, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        if (options?.holdBotFetch) {
          return options.holdBotFetch;
        }
        if (options?.botStatus === 404) {
          return Promise.resolve(
            jsonResponse(
              { error: { code: 'bot.not_found', message: 'not found' } },
              404,
            ),
          );
        }
        return Promise.resolve(jsonResponse(bot, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return Promise.resolve(jsonResponse(env, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'GET'
      ) {
        return Promise.resolve(jsonResponse({ secrets: [] }, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/runtime' &&
        method === 'GET'
      ) {
        return Promise.resolve(jsonResponse({ paused: false }, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        const ticket: StreamTicketResponse = {
          ticket: env.controlHolder ? 'ticket.ctrl' : 'ticket.view',
          wsPath: '/ws/bots/ws-1/bot-1/vnc',
          canControl: Boolean(env.controlHolder),
          expiresAt: '2026-11-18T17:00:00Z',
        };
        return Promise.resolve(jsonResponse(ticket, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/control/take' &&
        method === 'POST'
      ) {
        env = makeEnv({
          controlHolder: {
            userId: session.user.id,
            displayName: session.user.displayName,
            email: session.user.email,
          },
        });
        return Promise.resolve(jsonResponse(env, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/control/release' &&
        method === 'POST'
      ) {
        const body =
          typeof init?.body === 'string'
            ? (JSON.parse(init.body) as { note?: string })
            : {};
        env = makeEnv({
          controlHolder: null,
          controlNote: body.note ?? '',
        });
        return Promise.resolve(jsonResponse(env, 200));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment/stop' &&
        method === 'POST'
      ) {
        env = makeEnv({ status: 'stopped', envId: '' });
        return Promise.resolve(jsonResponse(env, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/dm' && method === 'POST') {
        return Promise.resolve(
          jsonResponse(
            {
              id: 'conv-1',
              workspaceId: 'ws-1',
              kind: 'dm',
              title: 'Atlas',
              botIds: ['bot-1'],
              createdAt: '2026-11-18T12:00:00Z',
            },
            200,
          ),
        );
      }
      if (
        url === '/api/workspaces/ws-1/conversations/conv-1/messages' &&
        method === 'GET'
      ) {
        return Promise.resolve(jsonResponse({ messages: [], turns: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/budgets' && method === 'GET') {
        return Promise.resolve(jsonResponse({ budgets: [] }, 200));
      }
      return Promise.resolve(jsonResponse({}, 404));
    },
  );

  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

describe('BotWorkspaceView redesign, loading skeleton, and desktop fullscreen', () => {
  beforeEach(() => {
    rfbInstances.length = 0;
    setAccessToken('jwt-workspace-test');
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    document.body.style.overflow = '';
  });

  it('renders the structural skeleton and branded hexagon loader while bot data is loading, then transitions cleanly on 404', async () => {
    let resolveBot!: (response: Response) => void;
    const pendingBot = new Promise<Response>((resolve) => {
      resolveBot = resolve;
    });
    stubWorkspaceFetch({ holdBotFetch: pendingBot });

    renderWorkspace();

    const loadingRegion = await screen.findByLabelText(
      enMessages.workspace.loading.regionLabel,
    );
    expect(loadingRegion).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByRole('status')).toHaveTextContent(
      enMessages.workspace.loadingBot,
    );
    expect(
      screen.getByText(enMessages.workspace.loading.subtitle),
    ).toBeInTheDocument();

    act(() => {
      resolveBot(
        jsonResponse(
          { error: { code: 'bot.not_found', message: 'not found' } },
          404,
        ),
      );
    });

    expect(
      await screen.findByText(enMessages.errors.botNotFound),
    ).toBeInTheDocument();
    expect(
      screen.queryByLabelText(enMessages.workspace.loading.regionLabel),
    ).not.toBeInTheDocument();
  });

  it('keeps Fullscreen disabled when the sandbox is stopped', async () => {
    stubWorkspaceFetch({ env: makeEnv({ status: 'stopped', envId: '' }) });

    renderWorkspace();

    expect(
      await screen.findByText(enMessages.workspace.computer.stoppedTitle),
    ).toBeInTheDocument();

    const fullscreenButton = screen.getByRole('button', {
      name: enMessages.workspace.computer.fullscreenAria.replace(
        '{name}',
        'Atlas',
      ),
    });
    expect(fullscreenButton).toBeDisabled();
    fireEvent.click(fullscreenButton);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('expands to fullscreen without reconnecting noVNC, supports chat drawer, browser fullscreen, Escape exit, and focus restoration', async () => {
    stubWorkspaceFetch();

    renderWorkspace();

    expect(await screen.findByTestId('vnc-stage')).toBeInTheDocument();
    await waitFor(() => {
      expect(rfbInstances.length).toBe(1);
    });

    // Telemetry progressbars are accessible in the docked bar.
    expect(
      screen.getByRole('progressbar', {
        name: enMessages.workspace.meters.cpuLabel,
      }),
    ).toHaveAttribute('aria-valuenow', '24');

    const fullscreenTrigger = screen.getByRole('button', {
      name: enMessages.workspace.computer.fullscreenAria.replace(
        '{name}',
        'Atlas',
      ),
    });
    expect(fullscreenTrigger).toBeEnabled();

    fireEvent.click(fullscreenTrigger);

    const dialog = screen.getByRole('dialog', {
      name: enMessages.workspace.computer.fullscreenRegionLabel.replace(
        '{name}',
        'Atlas',
      ),
    });
    expect(dialog).toBeInTheDocument();
    expect(document.body.style.overflow).toBe('hidden');
    // Must not reconnect RFB when moving between normal and fullscreen view.
    expect(rfbInstances.length).toBe(1);
    expect(rfbInstances[0]?.disconnect).not.toHaveBeenCalled();

    // Browser fullscreen button invokes requestFullscreen on the panel container.
    const requestFullscreen = vi.fn(() => Promise.resolve());
    Object.defineProperty(dialog, 'requestFullscreen', {
      configurable: true,
      value: requestFullscreen,
    });
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.browserFullscreen,
      }),
    );
    expect(requestFullscreen).toHaveBeenCalledTimes(1);

    // Toggle slide-over quick chat drawer inside fullscreen (single composer in DOM).
    const chatToggle = within(dialog).getByRole('button', {
      name: enMessages.workspace.computer.fullscreenChatToggle,
    });
    expect(chatToggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(chatToggle);
    expect(chatToggle).toHaveAttribute('aria-expanded', 'true');
    expect(
      within(dialog).getByRole('complementary', {
        name: enMessages.workspace.session.title,
      }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole('log')).toHaveLength(1);

    // If a native <dialog open> is open, Escape must not exit desktop fullscreen.
    const modalDialog = document.createElement('dialog');
    modalDialog.setAttribute('open', '');
    document.body.appendChild(modalDialog);
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(
      screen.getByRole('dialog', {
        name: enMessages.workspace.computer.fullscreenRegionLabel.replace(
          '{name}',
          'Atlas',
        ),
      }),
    ).toBeInTheDocument();
    modalDialog.remove();

    // Pressing Escape without a modal exits fullscreen and restores body scroll + focus.
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
    expect(document.body.style.overflow).toBe('');
    expect(rfbInstances.length).toBe(1);
    expect(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.fullscreenAria.replace(
          '{name}',
          'Atlas',
        ),
      }),
    ).toHaveFocus();
  });

  it('enforces RBAC and control locks inside fullscreen mode and supports takeover hand-back', async () => {
    stubWorkspaceFetch({
      role: 'viewer',
      env: makeEnv(),
    });

    const { unmount } = renderWorkspace();
    expect(await screen.findByTestId('vnc-stage')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.fullscreenAria.replace(
          '{name}',
          'Atlas',
        ),
      }),
    );

    const viewerControlBtn = screen.getByRole('button', {
      name: enMessages.workspace.computer.viewerReadonlyControl,
    });
    expect(viewerControlBtn).toBeDisabled();
    unmount();

    // Locked by another human: Take control stays disabled inside fullscreen.
    stubWorkspaceFetch({
      role: 'owner',
      env: makeEnv({
        controlHolder: {
          userId: 'user-2',
          displayName: 'Mara Lindqvist',
          email: 'mara@dunkel.tech',
        },
      }),
    });

    renderWorkspace();
    expect(await screen.findByTestId('vnc-stage')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.fullscreenAria.replace(
          '{name}',
          'Atlas',
        ),
      }),
    );

    expect(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.lockedByOther.replace(
          '{holder}',
          'Mara Lindqvist',
        ),
      }),
    ).toBeDisabled();
  });

  it('supports taking and releasing control inside fullscreen mode', async () => {
    stubWorkspaceFetch({ role: 'owner' });

    renderWorkspace();
    expect(await screen.findByTestId('vnc-stage')).toBeInTheDocument();
    await waitFor(() => {
      expect(rfbInstances.length).toBe(1);
    });

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.fullscreenAria.replace(
          '{name}',
          'Atlas',
        ),
      }),
    );

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.takeControl,
      }),
    );

    expect(
      await screen.findByText(
        enMessages.workspace.computer.takeoverBannerTitle.replace(
          '{name}',
          'Atlas',
        ),
      ),
    ).toBeInTheDocument();

    fireEvent.change(
      screen.getByLabelText(enMessages.workspace.computer.handbackNoteLabel),
      { target: { value: 'Finished manual check' } },
    );
    fireEvent.click(screen.getByRole('button', { name: /Hand back to Atlas/ }));

    await waitFor(() => {
      expect(
        screen.queryByText(
          enMessages.workspace.computer.takeoverBannerTitle.replace(
            '{name}',
            'Atlas',
          ),
        ),
      ).not.toBeInTheDocument();
    });

    fireEvent.click(
      screen.getByRole('button', {
        name: new RegExp(enMessages.workspace.computer.exitFullscreen),
      }),
    );
    expect(
      await screen.findByText('Finished manual check'),
    ).toBeInTheDocument();
  });
});
