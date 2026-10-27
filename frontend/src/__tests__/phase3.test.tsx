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
    readonly url: string;
    viewOnly = false;
    scaleViewport = false;

    constructor(_target: HTMLElement, url: string) {
      this.url = url;
    }

    disconnect() {}

    addEventListener() {}
  },
}));

const mockPush = vi.fn();
const mockReplace = vi.fn();
let mockPathname = '/bots/bot-1/secrets';
let mockParams: Record<string, string> = { botId: 'bot-1' };

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

import BotWorkspacePage from '@/app/bots/[botId]/page';
import BotSecretsPage from '@/app/bots/[botId]/secrets/page';
import BotSettingsPage from '@/app/bots/[botId]/settings/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  EgressLogEntry,
  EgressPolicyResponse,
  EnvironmentStateResponse,
  SecretMetadata,
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
    accessToken: 'jwt-phase3-token',
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

function makeEnv(): EnvironmentStateResponse {
  return {
    botId: 'bot-1',
    workspaceId: 'ws-1',
    driver: 'docker',
    envId: 'env-1',
    status: 'stopped',
    image: 'botbox/sandbox:dev',
    cpuMillis: 2000,
    memoryMiB: 4096,
    volumeRef: 'vol-bot-1',
    controlHolder: null,
    controlNote: '',
    stats: {
      cpuPercent: 0,
      memoryUsedMiB: 0,
      memoryLimitMiB: 4096,
      volumeUsedMiB: 128,
      volumeLimitMiB: 10240,
    },
    watchers: [],
    startedAt: null,
    updatedAt: '2026-11-15T10:00:00Z',
  };
}

function makeSecret(overrides: Partial<SecretMetadata> = {}): SecretMetadata {
  return {
    id: 'sec-1',
    workspaceId: 'ws-1',
    botId: 'bot-1',
    scope: 'bot',
    name: 'GITHUB_TOKEN',
    domainMatch: 'api.github.com',
    matchSubdomains: false,
    pathPrefix: '',
    authScheme: 'bearer',
    headerName: '',
    username: '',
    maskedPreview: '••••abcd',
    usedCount: 0,
    lastUsedAt: null,
    createdAt: '2026-11-15T09:00:00Z',
    updatedAt: '2026-11-15T09:00:00Z',
    ...overrides,
  };
}

function makePolicy(
  overrides: Partial<EgressPolicyResponse> = {},
): EgressPolicyResponse {
  return {
    botId: 'bot-1',
    workspaceId: 'ws-1',
    policy: 'allowlist',
    allowedHosts: ['pypi.org'],
    blockedHosts: [],
    effectiveSecretHosts: ['api.github.com'],
    revision: 1,
    acknowledgedRevision: 1,
    enforcementStatus: 'applied',
    updatedAt: '2026-11-15T09:00:00Z',
    ...overrides,
  };
}

function makeLog(overrides: Partial<EgressLogEntry> = {}): EgressLogEntry {
  return {
    id: 'log-1',
    botId: 'bot-1',
    workspaceId: 'ws-1',
    method: 'POST',
    host: 'api.github.com',
    port: '443',
    path: '/graphql',
    action: 'mitm_token_added',
    statusCode: 200,
    secretId: 'sec-1',
    secretScope: 'bot',
    secretName: 'GITHUB_TOKEN',
    bytesTransferred: 128,
    latencyMs: 42,
    createdAt: '2026-11-15T10:15:00Z',
    ...overrides,
  };
}

interface RecordedCall {
  url: string;
  method: string;
  body: string | null;
}

type FetchHandler = (
  url: string,
  method: string,
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
      const body = typeof init?.body === 'string' ? init.body : null;
      calls.push({ url, method, body });
      return handler(url, method, body);
    },
  );
  vi.stubGlobal('fetch', fetchSpy);
  return calls;
}

beforeEach(() => {
  mockPush.mockReset();
  mockReplace.mockReset();
  mockPathname = '/bots/bot-1/secrets';
  mockParams = { botId: 'bot-1' };
  setAccessToken(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('Phase 3 Secrets & Outbound Egress UI', () => {
  it('displays masked secrets, creates write-only secret without leaking value, edits metadata cleanly, and deletes on confirmation', async () => {
    const session = makeSession();
    const bot = makeBot();
    let secrets: SecretMetadata[] = [
      makeSecret({
        id: 'sec-ws',
        botId: null,
        scope: 'workspace',
        name: 'OPENAI_API_KEY',
        domainMatch: 'api.openai.com',
        maskedPreview: '••••9f2a',
        usedCount: 3,
      }),
      makeSecret(),
    ];
    let policy = makePolicy({
      effectiveSecretHosts: ['api.github.com', 'api.openai.com'],
    });

    const calls = installFetchMock((url, method, body) => {
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
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'GET'
      ) {
        return jsonResponse({ secrets }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'GET'
      ) {
        return jsonResponse(policy, 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs: [] }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'POST'
      ) {
        const parsed = JSON.parse(body ?? '{}') as {
          name?: string;
          domainMatch?: string;
          matchSubdomains?: boolean;
          pathPrefix?: string;
          headerName?: string;
        };
        const created = makeSecret({
          id: 'sec-created',
          name: parsed.name ?? '',
          domainMatch: parsed.domainMatch ?? '',
          matchSubdomains: Boolean(parsed.matchSubdomains),
          pathPrefix: parsed.pathPrefix ?? '',
          authScheme: 'header',
          headerName: parsed.headerName ?? '',
          maskedPreview: '••••alue',
          usedCount: 0,
        });
        secrets = [...secrets, created];
        policy = makePolicy({
          effectiveSecretHosts: [
            'api.anthropic.com',
            'api.github.com',
            'api.openai.com',
          ],
        });
        return jsonResponse(created, 201);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets/sec-created' &&
        method === 'PUT'
      ) {
        const parsed = JSON.parse(body ?? '{}') as { pathPrefix?: string };
        secrets = secrets.map((s) =>
          s.id === 'sec-created'
            ? { ...s, pathPrefix: parsed.pathPrefix ?? '' }
            : s,
        );
        const updated = secrets.find((s) => s.id === 'sec-created')!;
        return jsonResponse(updated, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets/sec-created' &&
        method === 'DELETE'
      ) {
        secrets = secrets.filter((s) => s.id !== 'sec-created');
        return new Response(null, { status: 204 });
      }
      return jsonResponse(
        { error: { code: 'not_found', message: '404' } },
        404,
      );
    });

    renderWithProviders(<BotSecretsPage />);

    expect(
      await screen.findByText('Atlas · Secrets & network'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/The git host and the token's rights/),
    ).toBeInTheDocument();
    expect(await screen.findByText('OPENAI_API_KEY')).toBeInTheDocument();
    expect(screen.getByText('••••9f2a')).toBeInTheDocument();
    expect(screen.getByText('3 uses')).toBeInTheDocument();
    expect(screen.getByText('GITHUB_TOKEN')).toBeInTheDocument();
    expect(screen.getByText('••••abcd')).toBeInTheDocument();
    expect(screen.getByText('Never used')).toBeInTheDocument();
    expect(screen.getByText('claude setup-token')).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole('button', { name: 'Configure Claude Pro / Max' }),
    );
    expect(screen.getByLabelText('Secret name')).toHaveValue(
      'llm-anthropic-oauth',
    );
    expect(screen.getByLabelText('Destination host')).toHaveValue(
      'api.anthropic.com',
    );
    expect(screen.getByLabelText('Secret value (write-only)')).toHaveValue('');

    fireEvent.change(screen.getByLabelText('Secret name'), {
      target: { value: 'ANTHROPIC_API_KEY' },
    });
    fireEvent.change(screen.getByLabelText('Destination host'), {
      target: { value: 'api.anthropic.com' },
    });
    fireEvent.click(screen.getByLabelText('Include subdomains (*.)'));
    fireEvent.change(screen.getByLabelText('Path prefix (optional)'), {
      target: { value: '/v1' },
    });
    fireEvent.change(screen.getByLabelText('Auth scheme'), {
      target: { value: 'header' },
    });
    fireEvent.change(screen.getByLabelText('Header name'), {
      target: { value: 'X-Api-Key' },
    });

    const passwordInput = screen.getByLabelText<HTMLInputElement>(
      'Secret value (write-only)',
    );
    expect(passwordInput.type).toBe('password');
    fireEvent.change(passwordInput, {
      target: { value: 'sk-ant-super-secret-value' },
    });

    fireEvent.click(
      screen.getByRole('button', { name: 'Encrypt & save secret' }),
    );

    expect(
      await screen.findByText(
        'Secret encrypted and stored outside the sandbox.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByText('ANTHROPIC_API_KEY')).toBeInTheDocument();
    expect(screen.getByText('••••alue')).toBeInTheDocument();
    expect(passwordInput.value).toBe('');
    expect(document.body.textContent).not.toContain(
      'sk-ant-super-secret-value',
    );

    const postCall = calls.find(
      (c) =>
        c.url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        c.method === 'POST',
    );
    expect(postCall).toBeDefined();
    expect(JSON.parse(postCall!.body ?? '{}')).toEqual({
      name: 'ANTHROPIC_API_KEY',
      domainMatch: 'api.anthropic.com',
      matchSubdomains: true,
      pathPrefix: '/v1',
      authScheme: 'header',
      headerName: 'X-Api-Key',
      value: 'sk-ant-super-secret-value',
    });

    fireEvent.click(
      screen.getByRole('button', { name: 'Edit ANTHROPIC_API_KEY' }),
    );
    const replaceInput = screen.getByLabelText<HTMLInputElement>(
      'New secret value (optional — leave blank to keep current)',
    );
    expect(replaceInput.value).toBe('');

    fireEvent.change(screen.getByLabelText('Path prefix (optional)'), {
      target: { value: '/v2' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Save secret changes' }),
    );

    await waitFor(() => {
      const putCall = calls.find(
        (c) =>
          c.url === '/api/workspaces/ws-1/bots/bot-1/secrets/sec-created' &&
          c.method === 'PUT',
      );
      expect(putCall).toBeDefined();
      expect(JSON.parse(putCall!.body ?? '{}')).not.toHaveProperty('value');
    });
    expect(await screen.findByText('api.anthropic.com/v2')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', { name: 'Delete ANTHROPIC_API_KEY' }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: 'Confirm delete ANTHROPIC_API_KEY' }),
    );

    expect(await screen.findByText('Secret removed.')).toBeInTheDocument();
    expect(screen.queryByText('ANTHROPIC_API_KEY')).not.toBeInTheDocument();
  });

  it('enforces RBAC for member bot owners and read-only viewers, and surfaces 409 conflicts', async () => {
    const memberWorkspace = makeWorkspace({ role: 'member' });
    const memberSession = makeSession({
      user: {
        id: 'user-member',
        email: 'member@dunkel.tech',
        displayName: 'Member User',
        locale: 'en',
        createdAt: '2026-11-14T12:00:00Z',
      },
      workspaces: [memberWorkspace],
      activeWorkspace: memberWorkspace,
    });
    const bot = makeBot({ ownerUserId: 'user-member' });

    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(memberSession, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'GET'
      ) {
        return jsonResponse(
          {
            secrets: [
              makeSecret({
                id: 'sec-ws',
                botId: null,
                scope: 'workspace',
                name: 'SHARED_TOKEN',
                domainMatch: 'shared.example.com',
              }),
              makeSecret(),
            ],
          },
          200,
        );
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'POST'
      ) {
        return jsonResponse(
          {
            error: {
              code: 'secret.already_exists',
              message: 'duplicate secret name',
            },
          },
          409,
        );
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'GET'
      ) {
        return jsonResponse(makePolicy(), 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs: [] }, 200);
      }
      return jsonResponse(
        { error: { code: 'not_found', message: '404' } },
        404,
      );
    });

    renderWithProviders(<BotSecretsPage />);

    expect(
      await screen.findByText('Atlas · Secrets & network'),
    ).toBeInTheDocument();

    expect(
      await screen.findByRole('button', { name: 'Edit GITHUB_TOKEN' }),
    ).toBeEnabled();
    expect(
      screen.getByRole('button', { name: 'Edit SHARED_TOKEN' }),
    ).toBeDisabled();
    expect(
      screen.getByRole('button', { name: 'Delete SHARED_TOKEN' }),
    ).toBeDisabled();
    expect(
      screen.getByRole('radio', {
        name: `${enMessages.secrets.scopeWorkspace} ${enMessages.secrets.workspaceScopeAdminOnly}`,
      }),
    ).toBeDisabled();

    fireEvent.change(screen.getByLabelText('Secret name'), {
      target: { value: 'GITHUB_TOKEN' },
    });
    fireEvent.change(screen.getByLabelText('Destination host'), {
      target: { value: 'api.github.com' },
    });
    fireEvent.change(screen.getByLabelText('Secret value (write-only)'), {
      target: { value: 'ghp_duplicate' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Encrypt & save secret' }),
    );

    expect(
      await screen.findByText(enMessages.errors.secretAlreadyExists),
    ).toBeInTheDocument();

    cleanup();
    setAccessToken(null);

    const viewerWorkspace = makeWorkspace({ role: 'viewer' });
    const viewerSession = makeSession({
      user: {
        id: 'user-viewer',
        email: 'viewer@dunkel.tech',
        displayName: 'Viewer User',
        locale: 'en',
        createdAt: '2026-11-14T12:00:00Z',
      },
      workspaces: [viewerWorkspace],
      activeWorkspace: viewerWorkspace,
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
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(makeBot(), 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'GET'
      ) {
        return jsonResponse({ secrets: [makeSecret()] }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'GET'
      ) {
        return jsonResponse(makePolicy(), 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs: [] }, 200);
      }
      return jsonResponse(
        { error: { code: 'not_found', message: '404' } },
        404,
      );
    });

    renderWithProviders(<BotSecretsPage />);

    expect(
      await screen.findByText(enMessages.secrets.readOnlyNotice),
    ).toBeInTheDocument();
    expect(
      await screen.findByRole('button', { name: 'Edit GITHUB_TOKEN' }),
    ).toBeDisabled();
    expect(
      screen.queryByRole('button', { name: 'Encrypt & save secret' }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Save outbound policy' }),
    ).not.toBeInTheDocument();
  });

  it('edits outbound network policy hosts/mode and filters egress activity logs', async () => {
    const session = makeSession();
    const bot = makeBot();
    let policy = makePolicy();
    const allLogs: EgressLogEntry[] = [
      makeLog(),
      makeLog({
        id: 'log-2',
        method: 'CONNECT',
        host: 'evil.example',
        port: '443',
        path: '/',
        action: 'blocked_host',
        statusCode: 403,
        secretId: null,
        secretScope: '',
        secretName: '',
        bytesTransferred: 0,
        latencyMs: 3,
      }),
    ];

    const calls = installFetchMock((url, method, body) => {
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
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'GET'
      ) {
        return jsonResponse({ secrets: [makeSecret()] }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'GET'
      ) {
        return jsonResponse(policy, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'PATCH'
      ) {
        const parsed = JSON.parse(body ?? '{}') as {
          policy: 'allowlist' | 'open';
          allowedHosts: string[];
          blockedHosts?: string[];
          expectedRevision: number;
        };
        policy = makePolicy({
          policy: parsed.policy,
          allowedHosts: parsed.allowedHosts,
          blockedHosts: parsed.blockedHosts ?? policy.blockedHosts,
          revision: parsed.expectedRevision + 1,
          acknowledgedRevision: parsed.expectedRevision + 1,
        });
        return jsonResponse(policy, 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        const queryIndex = url.indexOf('?');
        const params = new URLSearchParams(
          queryIndex >= 0 ? url.slice(queryIndex + 1) : '',
        );
        const actionFilter = params.get('action');
        const filtered = actionFilter
          ? allLogs.filter((entry) => entry.action === actionFilter)
          : allLogs;
        return jsonResponse({ logs: filtered }, 200);
      }
      return jsonResponse(
        { error: { code: 'not_found', message: '404' } },
        404,
      );
    });

    renderWithProviders(<BotSecretsPage />);

    expect(await screen.findByText('pypi.org')).toBeInTheDocument();
    expect(
      screen.getByText('POST api.github.com:443/graphql'),
    ).toBeInTheDocument();
    expect(screen.getByText('CONNECT evil.example:443/')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Add allowed host or wildcard'), {
      target: { value: '*.pythonhosted.org' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add host' }));
    expect(screen.getByText('*.pythonhosted.org')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Remove pypi.org' }));
    expect(screen.queryByText('pypi.org')).not.toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('radio', {
        name: `${enMessages.secrets.policyOpenTitle} ${enMessages.secrets.policyOpenDesc}`,
      }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: 'Save outbound policy' }),
    );

    expect(
      await screen.findByText('Outbound policy saved and applied.'),
    ).toBeInTheDocument();

    const patchCall = calls.find(
      (c) =>
        c.url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        c.method === 'PATCH',
    );
    expect(patchCall).toBeDefined();
    expect(JSON.parse(patchCall!.body ?? '{}')).toEqual({
      policy: 'open',
      allowedHosts: ['*.pythonhosted.org'],
      blockedHosts: [],
      expectedRevision: 1,
    });

    fireEvent.click(screen.getByRole('button', { name: 'Blocked' }));

    await waitFor(() => {
      expect(
        calls.some(
          (c) =>
            c.url ===
            '/api/workspaces/ws-1/bots/bot-1/egress/logs?action=blocked_host&limit=50',
        ),
      ).toBe(true);
    });
    expect(
      screen.queryByText('POST api.github.com:443/graphql'),
    ).not.toBeInTheDocument();
    expect(screen.getByText('CONNECT evil.example:443/')).toBeInTheDocument();
  });

  it('links to Secrets & network from BotWorkspacePage and BotSettingsPage', async () => {
    const session = makeSession();
    const bot = makeBot();
    const env = makeEnv();

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
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/secrets' &&
        method === 'GET'
      ) {
        return jsonResponse(
          {
            secrets: [makeSecret(), makeSecret({ id: 'sec-2', name: 'PAT_2' })],
          },
          200,
        );
      }
      return jsonResponse(
        { error: { code: 'not_found', message: '404' } },
        404,
      );
    });

    mockPathname = '/bots/bot-1';
    renderWithProviders(<BotWorkspacePage />);

    const egressLink = await screen.findByRole('link', {
      name: 'Egress proxy · 2 secrets',
    });
    expect(egressLink).toHaveAttribute('href', '/bots/bot-1/secrets');

    cleanup();
    setAccessToken(null);
    mockPathname = '/bots/bot-1/settings';
    renderWithProviders(<BotSettingsPage />);

    const tabsNav = await screen.findByRole('navigation', {
      name: 'Bot configuration sections',
    });
    expect(tabsNav).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'General' })).toHaveAttribute(
      'href',
      '/bots/bot-1/settings',
    );
    expect(
      screen.getByRole('link', { name: 'Secrets & network' }),
    ).toHaveAttribute('href', '/bots/bot-1/secrets');
  });
});
