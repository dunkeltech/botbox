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
import { type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import FleetPage from '@/app/page';
import TeamPage from '@/app/team/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AppShell } from '@/components/shell/AppShell';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type { AuthSessionResponse, Bot, WorkspaceSummary } from '@/types/api';
import enMessages from '../../messages/en.json';

const mockPush = vi.fn();
const mockReplace = vi.fn();
let mockPathname = '/';

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: mockPush,
    replace: mockReplace,
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => mockPathname,
  useParams: () => ({}),
}));

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

function makeWorkspace(
  overrides: Partial<WorkspaceSummary> = {},
): WorkspaceSummary {
  return {
    id: 'ws-1',
    name: 'dunkel.Tech',
    slug: 'dunkel-tech',
    defaultDriver: 'docker',
    defaultLocale: 'en',
    role: 'owner',
    createdAt: '2026-11-14T12:00:00Z',
    ...overrides,
  };
}

function makeSession(
  overrides: Partial<AuthSessionResponse> = {},
): AuthSessionResponse {
  const ws1 = makeWorkspace();
  const ws2 = makeWorkspace({
    id: 'ws-2',
    name: 'Client Lab',
    slug: 'client-lab',
    role: 'member',
  });
  return {
    user: {
      id: 'user-1',
      email: 'jan@dunkel.tech',
      displayName: 'Jan Gehring',
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces: [ws1, ws2],
    activeWorkspace: ws1,
    accessToken: 'jwt-shell-test',
    ...overrides,
  };
}

function makeBot(overrides: Partial<Bot> = {}): Bot {
  return {
    id: 'bot-1',
    workspaceId: 'ws-1',
    ownerUserId: 'user-1',
    name: 'Atlas',
    description: 'Maintains the Odoo cron modules.',
    avatarSeed: 'atlas-005a',
    llmMode: 'cli',
    llmProvider: 'anthropic',
    llmModel: 'claude-opus-4-6',
    driverType: 'docker',
    replyLanguage: 'en',
    turnTimeoutMinutes: 10,
    status: 'action',
    mood: 'action',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
    ...overrides,
  };
}

function installFetchMock(
  handler: (
    url: string,
    method: string,
    body: string | null,
  ) => Promise<Response> | Response,
) {
  const fetchSpy = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? init.body : null;
      return handler(url, method, body);
    },
  );
  vi.stubGlobal('fetch', fetchSpy);
  return fetchSpy;
}

beforeEach(() => {
  mockPush.mockReset();
  mockReplace.mockReset();
  mockPathname = '/';
  setAccessToken(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('Horizontal top navigation shell and Fleet dashboard', () => {
  it('renders horizontal top nav, switches workspaces, surfaces create error, and signs out', async () => {
    const session = makeSession();
    const fetchSpy = installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces' && method === 'POST') {
        return jsonResponse(
          {
            error: {
              code: 'workspace.slug_taken',
              message: 'slug already exists',
            },
          },
          409,
        );
      }
      if (url === '/api/auth/logout' && method === 'POST') {
        return new Response(null, { status: 204 });
      }
      return jsonResponse({ approvals: [] }, 200);
    });

    renderWithProviders(
      <AppShell>
        <div>workspace-body</div>
      </AppShell>,
    );

    const nav = await screen.findByRole('navigation', {
      name: enMessages.shell.navAria,
    });
    const header = nav.closest('header');
    expect(header).not.toBeNull();
    expect(header).toHaveClass('bg-terminal');
    expect(header?.querySelector('.max-w-\\[1360px\\]')).toBeNull();
    expect(
      screen.queryByRole('searchbox', { name: enMessages.team.searchLabel }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: enMessages.shell.notificationsAria }),
    ).toHaveAttribute('href', '/team/approvals');
    expect(
      within(nav).getByRole('link', { name: enMessages.shell.navFleet }),
    ).toHaveAttribute('aria-current', 'page');
    expect(
      within(nav).queryByRole('link', { name: enMessages.shell.navSecrets }),
    ).not.toBeInTheDocument();

    const switcherBtn = screen.getByRole('button', {
      name: 'Switch workspace: dunkel.Tech, role Owner',
    });
    fireEvent.click(switcherBtn);
    fireEvent.click(screen.getByRole('menuitemradio', { name: /Client Lab/ }));
    expect(
      screen.getByRole('button', {
        name: 'Switch workspace: Client Lab, role Member',
      }),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: 'Switch workspace: Client Lab, role Member',
      }),
    );
    fireEvent.click(
      screen.getByRole('menuitem', { name: enMessages.shell.newWorkspace }),
    );
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(
      within(dialog).getByLabelText(enMessages.shell.workspaceNameLabel),
      { target: { value: 'dunkel Tech' } },
    );
    fireEvent.click(
      within(dialog).getByRole('button', {
        name: enMessages.shell.createWorkspaceSubmit,
      }),
    );
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      enMessages.errors.workspaceSlugTaken,
    );
    fireEvent.click(
      within(dialog).getByRole('button', { name: enMessages.actions.cancel }),
    );

    fireEvent.click(
      screen.getByRole('button', { name: enMessages.shell.accountAria }),
    );
    fireEvent.click(
      screen.getByRole('menuitem', { name: enMessages.shell.signOut }),
    );

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith('/login');
    });
    expect(fetchSpy.mock.calls.some(([u]) => u === '/api/auth/logout')).toBe(
      true,
    );
  });

  it('shows contextual Secrets link in top nav when viewing a bot route', async () => {
    mockPathname = '/bots/bot-1';
    const session = makeSession();
    installFetchMock((url) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      return jsonResponse({ approvals: [] }, 200);
    });

    renderWithProviders(
      <AppShell>
        <div>bot-workspace-body</div>
      </AppShell>,
    );

    const nav = await screen.findByRole('navigation', {
      name: enMessages.shell.navAria,
    });
    expect(
      within(nav).getByRole('link', { name: enMessages.shell.navSecrets }),
    ).toHaveAttribute('href', '/bots/bot-1/secrets');
  });

  it('filters Fleet bots by status tabs and degrades cleanly when optional endpoints fail', async () => {
    const session = makeSession();
    const bots: Bot[] = [
      makeBot({ id: 'bot-1', name: 'Atlas', status: 'action', mood: 'action' }),
      makeBot({ id: 'bot-2', name: 'Nimbus', status: 'idle', mood: 'idle' }),
      makeBot({
        id: 'bot-3',
        name: 'Zink',
        status: 'stopped',
        mood: 'offline',
      }),
    ];

    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'GET') {
        return jsonResponse({ bots }, 200);
      }
      return jsonResponse({ error: { code: 'not_found' } }, 503);
    });

    renderWithProviders(<FleetPage />);

    expect(await screen.findByText('Atlas')).toBeInTheDocument();
    expect(screen.getByText('Nimbus')).toBeInTheDocument();
    expect(screen.getByText('Zink')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Running 1' }));
    expect(screen.getByText('Atlas')).toBeInTheDocument();
    expect(screen.queryByText('Nimbus')).not.toBeInTheDocument();
    expect(screen.queryByText('Zink')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Stopped 1' }));
    expect(screen.getByText('Zink')).toBeInTheDocument();
    expect(screen.queryByText('Atlas')).not.toBeInTheDocument();
    expect(
      screen.getByText(enMessages.fleet.statSpendNoMonthlyCap),
    ).toBeInTheDocument();
  });

  it('shows combined daily and weekly spend with workspace monthly budget meter on Fleet', async () => {
    const session = makeSession();
    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'GET') {
        return jsonResponse({ bots: [makeBot()] }, 200);
      }
      if (url === '/api/workspaces/ws-1/budgets' && method === 'GET') {
        return jsonResponse(
          {
            spendTodayMicros: 0,
            spendThisWeekMicros: 0,
            totalSpendTodayMicros: 4250000,
            totalSpendThisWeekMicros: 12750000,
            budgets: [
              {
                id: 'b-month',
                period: 'month',
                limitMicros: 50000000,
                spentMicros: 42000000,
                warning: true,
                exceeded: false,
              },
            ],
          },
          200,
        );
      }
      if (url === '/api/workspaces/ws-1/approvals') {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/notifications') {
        return jsonResponse({ notifications: [] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<FleetPage />);

    expect(await screen.findByText('$4.25')).toBeInTheDocument();
    expect(screen.getByText('$12.75')).toBeInTheDocument();
    expect(screen.getByText(enMessages.fleet.statSpend)).toBeInTheDocument();
    expect(
      screen.getByText(enMessages.fleet.statSpendTodayLabel),
    ).toBeInTheDocument();
    expect(
      screen.getByText(enMessages.fleet.statSpendWeekLabel),
    ).toBeInTheDocument();
    expect(screen.getByText('$42.00 of $50.00 / mo')).toBeInTheDocument();
    const meter = screen.getByRole('progressbar', {
      name: '$42.00 of $50.00 / mo',
    });
    expect(meter).toHaveAttribute('aria-valuenow', '84');
  });

  it('shows zero daily and weekly spend with no monthly cap fallback when workspace has no limits', async () => {
    const session = makeSession();
    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'GET') {
        return jsonResponse({ bots: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/budgets' && method === 'GET') {
        return jsonResponse(
          {
            spendTodayMicros: 0,
            spendThisWeekMicros: 0,
            totalSpendTodayMicros: 0,
            totalSpendThisWeekMicros: 0,
            budgets: [],
          },
          200,
        );
      }
      if (url === '/api/workspaces/ws-1/approvals') {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/notifications') {
        return jsonResponse({ notifications: [] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<FleetPage />);

    expect(
      await screen.findByText(enMessages.fleet.statSpendNoMonthlyCap),
    ).toBeInTheDocument();
    expect(screen.getAllByText('$0.00')).toHaveLength(2);
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
  });

  it('opens and closes the Team sidebar slide-over drawer inside AppShell variant="team"', async () => {
    mockPathname = '/team';
    const session = makeSession();
    installFetchMock((url) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url.endsWith('/groups')) {
        return jsonResponse([], 200);
      }
      if (url.endsWith('/bots')) {
        return jsonResponse({ bots: [] }, 200);
      }
      if (url.endsWith('/members')) {
        return jsonResponse({ members: [] }, 200);
      }
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url.endsWith('/notifications')) {
        return jsonResponse({ notifications: [] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<TeamPage />);

    const toggle = await screen.findByRole('button', {
      name: enMessages.shell.teamMenuToggleAria,
    });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(
      screen.getByRole('button', { name: enMessages.team.closeSidebar }),
    ).toBeInTheDocument();

    fireEvent.keyDown(document, { key: 'Escape' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
  });
});
