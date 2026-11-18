// SPDX-License-Identifier: AGPL-3.0-only

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { createElement } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const viewport = vi.hoisted(() => ({
  props: null as {
    ticket: string;
    onConnectFailed?: (wasConnected?: boolean) => void;
    onConnectionChange?: (connected: boolean) => void;
  } | null,
}));

vi.mock('@/components/workspace/VncViewport', () => ({
  VncViewport: (props: {
    ticket: string;
    onConnectFailed?: (wasConnected?: boolean) => void;
    onConnectionChange?: (connected: boolean) => void;
  }) => {
    viewport.props = props;
    return createElement('div', {
      'data-testid': 'vnc-stage',
      'data-ticket': props.ticket,
    });
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
import {
  BotWorkspaceView,
  VNC_TICKET_REFRESH_MS,
} from '@/components/workspace/BotWorkspaceView';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  EnvironmentStateResponse,
  StreamTicketResponse,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../../../messages/en.json';

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

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function makeWorkspace(): WorkspaceSummary {
  return {
    id: 'ws-1',
    name: 'Operations Lab',
    slug: 'operations-lab',
    defaultDriver: 'docker',
    defaultLocale: 'en',
    role: 'owner',
    createdAt: '2026-11-14T12:00:00Z',
  };
}

function makeSession(): AuthSessionResponse {
  const ws = makeWorkspace();
  return {
    user: {
      id: 'user-1',
      email: 'mara@dunkel.tech',
      displayName: 'Mara Lindqvist',
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces: [ws],
    activeWorkspace: ws,
    accessToken: 'jwt-vnc-token',
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
    turnTimeoutMinutes: 10,
    status: 'idle',
    mood: 'idle',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
  };
}

function makeEnv(
  status: EnvironmentStateResponse['status'],
): EnvironmentStateResponse {
  return {
    botId: 'bot-1',
    workspaceId: 'ws-1',
    driver: 'memory',
    envId: status === 'running' ? 'env-1' : '',
    status,
    image: 'botbox/sandbox:dev',
    cpuMillis: 2000,
    memoryMiB: 4096,
    volumeRef: 'vol-bot-1',
    controlHolder: null,
    controlNote: '',
    stats: {
      cpuPercent: 24,
      memoryUsedMiB: 1024,
      memoryLimitMiB: 4096,
      volumeUsedMiB: 512,
      volumeLimitMiB: 10240,
    },
    watchers: [{ userId: 'user-1', displayName: 'Mara Lindqvist' }],
    startedAt: status === 'running' ? '2026-11-15T09:00:00Z' : null,
    updatedAt: '2026-11-15T09:01:00Z',
  };
}

interface RecordedCall {
  url: string;
  method: string;
  headers: Headers;
}

function installFetch(state: {
  envStatus: EnvironmentStateResponse['status'];
  failTicketsAfter: number;
}) {
  const calls: RecordedCall[] = [];
  let tickets = 0;
  const session = makeSession();
  const bot = makeBot();
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method = (init?.method ?? 'GET').toUpperCase();
      calls.push({ url, method, headers: new Headers(init?.headers) });

      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') return jsonResponse(session);
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return jsonResponse(makeEnv(state.envStatus));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment/stop' &&
        method === 'POST'
      ) {
        state.envStatus = 'stopped';
        return jsonResponse(makeEnv('stopped'));
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        tickets += 1;
        if (tickets > state.failTicketsAfter) {
          return jsonResponse(
            {
              error: {
                code: 'environment.not_running',
                message: 'raw backend text',
              },
            },
            409,
          );
        }
        const ticket: StreamTicketResponse = {
          ticket: `ticket-${tickets}`,
          wsPath: '/ws/bots/ws-1/bot-1/vnc',
          canControl: false,
          expiresAt: '2026-11-15T10:00:00Z',
        };
        return jsonResponse(ticket);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] });
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/desktop-actions') {
        return jsonResponse({ actions: [], screenshotRetentionDays: 7 });
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/dm' && method === 'POST') {
        return jsonResponse({ id: 'conv-1', botId: 'bot-1' });
      }
      if (url.includes('/conversations/conv-1/messages')) {
        return jsonResponse({ messages: [], turns: [] });
      }
      if (url.startsWith('/api/workspaces/ws-1/budgets')) {
        return jsonResponse({ spendTodayMicros: 0, budgets: [] });
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/runtime') {
        return jsonResponse({ paused: false });
      }
      return jsonResponse(
        { error: { code: 'unknown', message: `unexpected ${method} ${url}` } },
        404,
      );
    }),
  );
  return {
    calls,
    ticketPosts: () =>
      calls.filter(
        (call) =>
          call.url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
          call.method === 'POST',
      ),
  };
}

class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = 0;
  url = '';
  protocols: string | string[] | undefined;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onopen: (() => void) | null = null;
  constructor(url: string, protocols?: string | string[]) {
    this.url = url;
    this.protocols = protocols;
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }
  send() {}
}

async function openRunningDesktop(
  state: {
    envStatus: EnvironmentStateResponse['status'];
    failTicketsAfter: number;
  } = { envStatus: 'running', failTicketsAfter: 100 },
) {
  const fetchMock = installFetch(state);
  renderWorkspace();
  await waitFor(() => {
    expect(screen.getByTestId('vnc-stage')).toHaveAttribute(
      'data-ticket',
      'ticket-1',
    );
  });
  return fetchMock;
}

beforeEach(() => {
  viewport.props = null;
  setAccessToken(null);
  vi.stubGlobal('WebSocket', FakeWebSocket);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('VNC ticket refresh while the desktop boots', () => {
  it('requests a fresh authorization immediately after an established stream closes', async () => {
    const { ticketPosts } = await openRunningDesktop();
    act(() => viewport.props?.onConnectionChange?.(true));
    act(() => {
      viewport.props?.onConnectionChange?.(false);
      viewport.props?.onConnectFailed?.(true);
    });

    await waitFor(() =>
      expect(screen.getByTestId('vnc-stage')).toHaveAttribute(
        'data-ticket',
        'ticket-2',
      ),
    );
    expect(ticketPosts()).toHaveLength(2);
  });
  it('mints one new ticket after the slow delay and does not show the close as an error', async () => {
    const { ticketPosts } = await openRunningDesktop();
    expect(ticketPosts()).toHaveLength(1);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();

    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => {
      viewport.props?.onConnectFailed?.();
      viewport.props?.onConnectFailed?.();
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(VNC_TICKET_REFRESH_MS - 1);
    });
    expect(ticketPosts()).toHaveLength(1);
    expect(screen.getByTestId('vnc-stage')).toHaveAttribute(
      'data-ticket',
      'ticket-1',
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
      for (let i = 0; i < 12; i += 1) await Promise.resolve();
    });
    expect(ticketPosts()).toHaveLength(2);
    expect(ticketPosts()[1]?.headers.get('X-BotBox-CSRF')).toBe('1');
    expect(screen.getByTestId('vnc-stage')).toHaveAttribute(
      'data-ticket',
      'ticket-2',
    );
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('raw backend text')).not.toBeInTheDocument();
  });

  it('does not renew the ticket after the desktop connects', async () => {
    const { ticketPosts } = await openRunningDesktop();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });

    act(() => {
      viewport.props?.onConnectFailed?.();
      viewport.props?.onConnectionChange?.(true);
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(VNC_TICKET_REFRESH_MS);
    });
    expect(ticketPosts()).toHaveLength(1);
    expect(screen.getByTestId('vnc-stage')).toHaveAttribute(
      'data-ticket',
      'ticket-1',
    );
  });

  it('does not renew the ticket after the machine stops', async () => {
    const { ticketPosts } = await openRunningDesktop();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => {
      viewport.props?.onConnectFailed?.();
    });

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Stop sandbox' }));
      for (let i = 0; i < 8; i += 1) {
        await Promise.resolve();
      }
    });

    expect(
      screen.getByText(enMessages.workspace.computer.stoppedTitle),
    ).toBeInTheDocument();
    expect(screen.queryByTestId('vnc-stage')).not.toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(VNC_TICKET_REFRESH_MS);
    });
    expect(ticketPosts()).toHaveLength(1);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows the translated API error when a renewal is rejected', async () => {
    const { ticketPosts } = await openRunningDesktop({
      envStatus: 'running',
      failTicketsAfter: 1,
    });
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => {
      viewport.props?.onConnectFailed?.();
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(VNC_TICKET_REFRESH_MS);
      for (let i = 0; i < 12; i += 1) await Promise.resolve();
    });

    expect(ticketPosts()).toHaveLength(2);
    expect(
      screen.getByText(enMessages.errors.environmentNotRunning),
    ).toBeInTheDocument();
    expect(screen.queryByText('raw backend text')).not.toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(VNC_TICKET_REFRESH_MS);
    });
    expect(ticketPosts()).toHaveLength(2);
  });
});
