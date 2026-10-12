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

import NewBotPage from '@/app/bots/new/page';
import LoginPage from '@/app/login/page';
import FleetPage from '@/app/page';
import MembersSettingsPage from '@/app/settings/members/page';
import SetupPage from '@/app/setup/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  CreatedInvitationResponse,
  InvitationSummary,
  WorkspaceMember,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../messages/en.json';

const mockPush = vi.fn();
const mockReplace = vi.fn();
let mockPathname = '/';
let mockParams: Record<string, string> = {};

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: mockPush,
    replace: mockReplace,
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => mockPathname,
  useParams: () => mockParams,
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
    name: 'Operations Lab',
    slug: 'operations-lab',
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
    accessToken: 'jwt-test-token',
    ...overrides,
  };
}

function makeBot(overrides: Partial<Bot> = {}): Bot {
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
    status: 'idle',
    mood: 'idle',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
    ...overrides,
  };
}

interface RecordedCall {
  url: string;
  method: string;
  headers: Headers;
  body: string | null;
}

type FetchHandler = (
  url: string,
  method: string,
  headers: Headers,
  body: string | null,
) => Promise<Response> | Response;

function installFetchMock(handler: FetchHandler): RecordedCall[] {
  const calls: RecordedCall[] = [];
  const fetchSpy = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method = (init?.method ?? 'GET').toUpperCase();
      const headers = new Headers(init?.headers);
      const body = typeof init?.body === 'string' ? init.body : null;
      calls.push({ url, method, headers, body });
      return handler(url, method, headers, body);
    },
  );
  vi.stubGlobal('fetch', fetchSpy);
  return calls;
}

beforeEach(() => {
  mockPush.mockReset();
  mockReplace.mockReset();
  mockPathname = '/';
  mockParams = {};
  setAccessToken(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('Phase 1 frontend workflows', () => {
  it('Setup Wizard auto-generates workspace slug, surfaces invalid secret, and completes setup', async () => {
    mockPathname = '/setup';
    const session = makeSession({ accessToken: 'jwt-setup-1' });
    let completeAttempt = 0;

    const calls = installFetchMock((url, method) => {
      if (url === '/api/setup/status' && method === 'GET') {
        return jsonResponse({ setupRequired: true }, 200);
      }
      if (url === '/api/setup/complete' && method === 'POST') {
        completeAttempt += 1;
        if (completeAttempt === 1) {
          return jsonResponse(
            {
              error: {
                code: 'setup.invalid_secret',
                message: 'invalid setup secret',
              },
            },
            403,
          );
        }
        return jsonResponse(session, 201);
      }
      return jsonResponse({ error: { code: 'not_found' } }, 404);
    });

    renderWithProviders(<SetupPage />);

    const secretInput = await screen.findByLabelText(
      enMessages.setup.secretLabel,
    );
    const nameInput = screen.getByLabelText(enMessages.setup.displayNameLabel);
    const emailInput = screen.getByLabelText(enMessages.setup.emailLabel);
    const passwordInput = screen.getByLabelText(enMessages.setup.passwordLabel);
    const wsNameInput = screen.getByLabelText(
      enMessages.setup.workspaceNameLabel,
    );
    const wsSlugInput = screen.getByLabelText(
      enMessages.setup.workspaceSlugLabel,
    );

    fireEvent.change(wsNameInput, { target: { value: 'Operations Lab' } });
    expect((wsSlugInput as HTMLInputElement).value).toBe('operations-lab');

    fireEvent.change(wsSlugInput, { target: { value: 'custom-ops' } });
    fireEvent.change(wsNameInput, { target: { value: 'Ops Lab Two' } });
    expect((wsSlugInput as HTMLInputElement).value).toBe('custom-ops');

    fireEvent.change(secretInput, { target: { value: 'wrong-secret' } });
    fireEvent.change(nameInput, { target: { value: 'Mara Lindqvist' } });
    fireEvent.change(emailInput, { target: { value: 'mara@dunkel.tech' } });
    fireEvent.change(passwordInput, {
      target: { value: 'CorrectHorseBattery1!' },
    });

    fireEvent.click(
      screen.getByRole('button', { name: new RegExp(enMessages.setup.submit) }),
    );

    const alerts = await screen.findAllByRole('alert');
    expect(alerts.length).toBeGreaterThanOrEqual(1);
    expect(
      screen.getAllByText(enMessages.errors.setupInvalidSecret).length,
    ).toBeGreaterThanOrEqual(1);
    expect(secretInput).toHaveAttribute('aria-invalid', 'true');

    fireEvent.change(secretInput, { target: { value: 'valid-secret-value' } });
    fireEvent.click(
      screen.getByRole('button', { name: new RegExp(enMessages.setup.submit) }),
    );

    await waitFor(() => {
      expect(mockPush).toHaveBeenCalledWith('/');
    });

    const postCall = calls.filter(
      (c) => c.url === '/api/setup/complete' && c.method === 'POST',
    )[1];
    expect(postCall?.headers.get('X-BotBox-CSRF')).toBe('1');
  });

  it('Login screen handles invalid credentials alert, password toggle, and sign-in success', async () => {
    mockPathname = '/login';
    const session = makeSession({ accessToken: 'jwt-login-1' });
    let loginAttempt = 0;

    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(
          { error: { code: 'auth.unauthenticated', message: 'no cookie' } },
          401,
        );
      }
      if (url === '/api/auth/login' && method === 'POST') {
        loginAttempt += 1;
        if (loginAttempt === 1) {
          return jsonResponse(
            {
              error: {
                code: 'auth.invalid_credentials',
                message: 'bad login',
              },
            },
            401,
          );
        }
        return jsonResponse(session, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<LoginPage />);

    const emailInput = await screen.findByLabelText(enMessages.auth.emailLabel);
    const passwordInput = screen.getByLabelText<HTMLInputElement>(
      enMessages.auth.passwordLabel,
    );

    expect(passwordInput.type).toBe('password');
    fireEvent.click(
      screen.getByRole('button', { name: enMessages.auth.showPassword }),
    );
    expect(passwordInput.type).toBe('text');

    fireEvent.change(emailInput, { target: { value: 'mara@dunkel.tech' } });
    fireEvent.change(passwordInput, { target: { value: 'wrong-pass' } });

    fireEvent.click(
      screen.getByRole('button', { name: new RegExp(enMessages.auth.submit) }),
    );

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(enMessages.errors.authInvalidCredentials);

    fireEvent.change(passwordInput, {
      target: { value: 'CorrectHorseBattery1!' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: new RegExp(enMessages.auth.submit) }),
    );

    await waitFor(() => {
      expect(mockPush).toHaveBeenCalledWith('/');
    });
  });

  it('Fleet Dashboard enforces RBAC for owner vs viewer and filters bots', async () => {
    const ownerSession = makeSession();
    const bots: Bot[] = [
      makeBot({ id: 'bot-1', name: 'Atlas', avatarSeed: 'atlas-005a' }),
      makeBot({
        id: 'bot-2',
        name: 'Nova',
        avatarSeed: 'nova-00b2',
        llmModel: 'gemini-2.5-pro',
        llmProvider: 'google',
        status: 'thinking',
        mood: 'thinking',
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
        return jsonResponse(ownerSession, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'GET') {
        return jsonResponse({ bots }, 200);
      }
      return jsonResponse({}, 404);
    });

    const firstRender = renderWithProviders(<FleetPage />);

    expect(
      await screen.findByRole('link', { name: enMessages.fleet.createCta }),
    ).toBeInTheDocument();
    expect(await screen.findByText('Atlas')).toBeInTheDocument();
    expect(screen.getByText('Nova')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(enMessages.fleet.searchLabel), {
      target: { value: 'gemini' },
    });
    expect(screen.queryByText('Atlas')).not.toBeInTheDocument();
    expect(screen.getByText('Nova')).toBeInTheDocument();

    firstRender.unmount();

    // Viewer session hides Create bot CTA and displays read-only notice
    const viewerWs = makeWorkspace({ role: 'viewer' });
    const viewerSession = makeSession({
      workspaces: [viewerWs],
      activeWorkspace: viewerWs,
    });
    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(viewerSession, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'GET') {
        return jsonResponse({ bots: [bots[0]] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<FleetPage />);
    expect(
      await screen.findByText(enMessages.fleet.viewerReadonly),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('link', { name: enMessages.fleet.createCta }),
    ).not.toBeInTheDocument();
    expect(
      await screen.findByText(enMessages.fleet.inspectAction),
    ).toBeInTheDocument();
  });

  it('Bot Builder previews deterministic avatar seed + mood and submits createBot with Auth + CSRF', async () => {
    mockPathname = '/bots/new';
    const session = makeSession({ accessToken: 'jwt-builder-1' });
    const createdBot = makeBot({ id: 'bot-created', name: 'Sentinel' });

    const calls = installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'POST') {
        return jsonResponse(createdBot, 201);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<NewBotPage />);

    const nameInput = await screen.findByLabelText(
      enMessages.builder.nameLabel,
    );
    const seedInput = screen.getByLabelText<HTMLInputElement>(
      enMessages.builder.avatarSeedLabel,
    );

    fireEvent.change(nameInput, { target: { value: 'Sentinel' } });
    fireEvent.click(
      screen.getByRole('button', { name: enMessages.builder.moods.thinking }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: enMessages.builder.randomizeSeed }),
    );

    const randomizedSeed = seedInput.value;
    expect(randomizedSeed).toMatch(/^sentinel-[0-9a-f]{4}$/);
    expect(
      screen.getByRole('img', {
        name: `Robot avatar ${randomizedSeed}, thinking`,
      }),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: new RegExp(enMessages.builder.createSubmit),
      }),
    );

    await waitFor(() => {
      expect(mockPush).toHaveBeenCalledWith('/bots/bot-created');
    });

    const createCall = calls.find(
      (c) => c.url === '/api/workspaces/ws-1/bots' && c.method === 'POST',
    );
    expect(createCall?.headers.get('Authorization')).toBe(
      'Bearer jwt-builder-1',
    );
    expect(createCall?.headers.get('X-BotBox-CSRF')).toBe('1');
  });

  it('Members settings updates roles, creates invitations, and surfaces workspace.last_owner', async () => {
    mockPathname = '/settings/members';
    const session = makeSession({ accessToken: 'jwt-members-1' });
    const members: WorkspaceMember[] = [
      {
        workspaceId: 'ws-1',
        userId: 'user-1',
        email: 'mara@dunkel.tech',
        displayName: 'Mara Lindqvist',
        locale: 'en',
        role: 'owner',
        joinedAt: '2026-11-14T12:00:00Z',
      },
      {
        workspaceId: 'ws-1',
        userId: 'user-2',
        email: 'jonas@dunkel.tech',
        displayName: 'Jonas Weber',
        locale: 'en',
        role: 'member',
        joinedAt: '2026-11-14T13:00:00Z',
      },
    ];
    const invitations: InvitationSummary[] = [];

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
      if (url === '/api/workspaces/ws-1/members' && method === 'GET') {
        return jsonResponse({ members }, 200);
      }
      if (url === '/api/workspaces/ws-1/invitations' && method === 'GET') {
        return jsonResponse({ invitations }, 200);
      }
      if (url === '/api/workspaces/ws-1/members/user-2' && method === 'PATCH') {
        return jsonResponse({ ...members[1], role: 'admin' }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/members/user-1' &&
        method === 'DELETE'
      ) {
        return jsonResponse(
          {
            error: {
              code: 'workspace.last_owner',
              message: 'last owner cannot be removed',
            },
          },
          400,
        );
      }
      if (url === '/api/workspaces/ws-1/invitations' && method === 'POST') {
        const created: CreatedInvitationResponse = {
          invitation: {
            id: 'inv-1',
            workspaceId: 'ws-1',
            email: 'new@dunkel.tech',
            role: 'admin',
            expiresAt: '2026-11-21T12:00:00Z',
            acceptedAt: null,
            createdAt: '2026-11-14T14:00:00Z',
          },
          token: 'tok-abc',
          inviteUrl: '/invite/tok-abc',
        };
        return jsonResponse(created, 201);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<MembersSettingsPage />);

    expect(await screen.findByText('Jonas Weber')).toBeInTheDocument();
    const membersTable = screen.getByRole('table');
    expect(
      within(membersTable).getByText('Mara Lindqvist'),
    ).toBeInTheDocument();

    const jonasRoleSelect = screen.getByLabelText<HTMLSelectElement>(
      'Role for Jonas Weber',
    );
    fireEvent.change(jonasRoleSelect, { target: { value: 'admin' } });
    await waitFor(() => {
      expect(jonasRoleSelect.value).toBe('admin');
    });

    fireEvent.change(
      screen.getByLabelText(enMessages.members.inviteEmailLabel),
      { target: { value: 'new@dunkel.tech' } },
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: new RegExp(enMessages.members.inviteSubmit),
      }),
    );

    const generatedInput = await screen.findByLabelText<HTMLInputElement>(
      enMessages.members.generatedTitle,
    );
    expect(generatedInput.value).toContain('/invite/tok-abc');

    const ownerRow = within(membersTable)
      .getByText('Mara Lindqvist')
      .closest('tr');
    expect(ownerRow).not.toBeNull();
    fireEvent.click(
      within(ownerRow as HTMLElement).getByRole('button', {
        name: enMessages.members.removeMember,
      }),
    );

    const lastOwnerAlert = await screen.findByRole('alert');
    expect(lastOwnerAlert).toHaveTextContent(
      enMessages.errors.workspaceLastOwner,
    );
  });
});
