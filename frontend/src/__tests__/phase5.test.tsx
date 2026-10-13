import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@novnc/novnc', () => ({
  default: class MockRFB {
    viewOnly = false;
    scaleViewport = false;
    constructor(target: HTMLElement, url: string) {
      void target;
      void url;
      queueMicrotask(() => {
        this.listeners.get('connect')?.();
      });
    }
    private readonly listeners = new Map<string, () => void>();
    disconnect() {}
    addEventListener(type: string, listener: () => void) {
      this.listeners.set(type, listener);
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

import BotWorkspacePage from '@/app/bots/[botId]/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  DesktopAction,
  EnvironmentStateResponse,
  StreamTicketResponse,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../messages/en.json';

function renderWithProviders(ui: ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      <QueryProvider>
        <AuthProvider>{ui}</AuthProvider>
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
    screenshotRetentionDays: 7,
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
    accessToken: 'jwt-phase5-token',
  };
}

function makeBot(): Bot {
  return {
    id: 'bot-1',
    workspaceId: 'ws-1',
    ownerUserId: 'user-1',
    name: 'Atlas',
    description: 'Fills forms on the desktop.',
    avatarSeed: 'atlas-005a',
    llmMode: 'api',
    llmProvider: 'anthropic',
    llmModel: 'claude-sonnet',
    driverType: 'docker',
    replyLanguage: 'en',
    status: 'action',
    mood: 'action',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
  };
}

function makeEnv(
  holder: EnvironmentStateResponse['controlHolder'],
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
    volumeRef: 'vol-bot-1',
    controlHolder: holder,
    controlNote: '',
    stats: {
      cpuPercent: 10,
      memoryUsedMiB: 256,
      memoryLimitMiB: 4096,
      volumeUsedMiB: 20,
      volumeLimitMiB: 10240,
    },
    watchers: [],
    startedAt: '2026-11-15T09:00:00Z',
    updatedAt: '2026-11-15T09:01:00Z',
  };
}

const actions: DesktopAction[] = [
  {
    id: 'action-click',
    botId: 'bot-1',
    kind: 'click',
    result: 'ok',
    errorCode: '',
    hasScreenshot: true,
    hasCoord: true,
    coordX: 100,
    coordY: 160,
    createdAt: '2026-11-16T09:00:00Z',
  },
  {
    id: 'action-locked',
    botId: 'bot-1',
    kind: 'type',
    result: '',
    errorCode: 'control.locked',
    hasScreenshot: false,
    hasCoord: false,
    createdAt: '2026-11-16T09:01:00Z',
  },
];

beforeEach(() => {
  setAccessToken(null);
  if (typeof URL.createObjectURL !== 'function') {
    URL.createObjectURL = () => 'blob:shot';
  }
  if (typeof URL.revokeObjectURL !== 'function') {
    URL.revokeObjectURL = () => undefined;
  }
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('Phase 5 desktop timeline and cursor', () => {
  it('shows the action timeline, labelled cursor, and hides the cursor during takeover', async () => {
    const session = makeSession();
    let env = makeEnv(null);
    const calls: string[] = [];
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
        calls.push(`${method} ${url}`);
        if (url === '/api/setup/status') {
          return jsonResponse(
            { error: { code: 'setup.already_completed', message: 'done' } },
            404,
          );
        }
        if (url === '/api/auth/refresh') {
          return jsonResponse(session, 200);
        }
        if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
          return jsonResponse(makeBot(), 200);
        }
        if (
          url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
          method === 'GET'
        ) {
          return jsonResponse(env, 200);
        }
        if (
          url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
          method === 'POST'
        ) {
          const ticket: StreamTicketResponse = {
            ticket: 'ticket.view',
            wsPath: '/ws/bots/ws-1/bot-1/vnc',
            canControl: false,
            expiresAt: '2026-11-16T10:00:00Z',
          };
          return jsonResponse(ticket, 200);
        }
        if (
          url === '/api/workspaces/ws-1/bots/bot-1/desktop-actions' &&
          method === 'GET'
        ) {
          return jsonResponse({ actions, screenshotRetentionDays: 7 }, 200);
        }
        if (url.endsWith('/desktop-actions/action-click/screenshot')) {
          return new Response(new Uint8Array([1, 2, 3]), {
            status: 200,
            headers: { 'Content-Type': 'image/png' },
          });
        }
        if (
          url === '/api/workspaces/ws-1/bots/bot-1/control/take' &&
          method === 'POST'
        ) {
          env = makeEnv({
            userId: session.user.id,
            displayName: session.user.displayName,
            email: session.user.email,
          });
          return jsonResponse(env, 200);
        }
        return jsonResponse({}, 404);
      }),
    );

    renderWithProviders(<BotWorkspacePage />);

    expect(await screen.findByTestId('bot-cursor')).toHaveTextContent('Atlas');
    expect(screen.getByTestId('bot-cursor')).toHaveStyle({
      left: `${(100 / 1280) * 100}%`,
      top: `${(160 / 800) * 100}%`,
    });

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.tabs.actions,
      }),
    );
    expect(
      await screen.findByText('Screenshots kept 7 days · excluded from logs'),
    ).toBeInTheDocument();
    expect(screen.getByText('100, 160 · 1280×800')).toBeInTheDocument();
    expect(screen.getByText('409')).toBeInTheDocument();
    expect(screen.queryByText('secret-typed-value')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Screenshot' }));
    expect(
      await screen.findByRole('img', { name: 'Screenshot of click' }),
    ).toBeInTheDocument();
    expect(
      calls.some((call) =>
        call.includes('/desktop-actions/action-click/screenshot'),
      ),
    ).toBe(true);

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.tabs.desktop,
      }),
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.takeControl,
      }),
    );
    await waitFor(() => {
      expect(screen.queryByTestId('bot-cursor')).not.toBeInTheDocument();
    });
  });

  it('ignores a missing timeline so the desktop tab stays quiet', async () => {
    const session = makeSession();
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
        if (url === '/api/setup/status') {
          return jsonResponse(
            { error: { code: 'setup.already_completed', message: 'done' } },
            404,
          );
        }
        if (url === '/api/auth/refresh') {
          return jsonResponse(session, 200);
        }
        if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
          return jsonResponse(makeBot(), 200);
        }
        if (
          url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
          method === 'GET'
        ) {
          return jsonResponse(makeEnv(null), 200);
        }
        if (
          url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
          method === 'POST'
        ) {
          const ticket: StreamTicketResponse = {
            ticket: 'ticket.view',
            wsPath: '/ws/bots/ws-1/bot-1/vnc',
            canControl: false,
            expiresAt: '2026-11-16T10:00:00Z',
          };
          return jsonResponse(ticket, 200);
        }
        return jsonResponse({}, 404);
      }),
    );

    renderWithProviders(<BotWorkspacePage />);
    expect(await screen.findByTestId('vnc-stage')).toBeInTheDocument();
    await waitFor(() => {
      expect(
        screen.queryByText(enMessages.errors.unknownError),
      ).not.toBeInTheDocument();
    });
    expect(screen.queryByTestId('bot-cursor')).not.toBeInTheDocument();
  });
});
