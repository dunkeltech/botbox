// SPDX-License-Identifier: AGPL-3.0-only

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

const mockPush = vi.fn();
const mockReplace = vi.fn();

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: mockPush,
    replace: mockReplace,
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => '/bots/bot-1/secrets',
  useParams: () => ({ botId: 'bot-1' }),
}));

import { BotSecretsView } from '@/components/bots/BotSecretsView';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import {
  computeTriageMutation,
  computeUndoMutation,
  evaluateHostInPolicy,
} from '@/lib/egress-policy';
import type {
  AuthSessionResponse,
  Bot,
  EgressLogEntry,
  EgressPolicyResponse,
  WorkspaceRole,
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

function makeWorkspace(role: WorkspaceRole = 'owner'): WorkspaceSummary {
  return {
    id: 'ws-1',
    name: 'Operations Lab',
    slug: 'operations-lab',
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
      email: 'mara@dunkel.tech',
      displayName: 'Mara Lindqvist',
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces: [ws],
    activeWorkspace: ws,
    accessToken: 'jwt-triage-test',
  };
}

function makeBot(overrides: Partial<Bot> = {}): Bot {
  return {
    id: 'bot-1',
    workspaceId: 'ws-1',
    ownerUserId: 'user-1',
    name: 'Atlas',
    description: 'Release bot',
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
    allowedHosts: ['pypi.org', 'deb.debian.org'],
    blockedHosts: ['*.tracking.example'],
    effectiveSecretHosts: ['api.github.com'],
    revision: 1,
    acknowledgedRevision: 1,
    enforcementStatus: 'applied',
    updatedAt: '2026-11-17T10:00:00Z',
    ...overrides,
  };
}

function makeLog(overrides: Partial<EgressLogEntry> = {}): EgressLogEntry {
  return {
    id: 'log-1',
    botId: 'bot-1',
    workspaceId: 'ws-1',
    method: 'CONNECT',
    host: 'crates.io',
    port: '443',
    path: '/',
    action: 'blocked_host',
    statusCode: 403,
    secretId: null,
    secretScope: '',
    secretName: '',
    bytesTransferred: 0,
    latencyMs: 5,
    createdAt: '2026-11-17T10:15:00Z',
    ...overrides,
  };
}

function installFetchMock(
  handler: (
    url: string,
    method: string,
    body: string | null,
  ) => Response | Promise<Response>,
) {
  const spy = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url =
      typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url;
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? init.body : null;
    return handler(url, method, body);
  });
  vi.stubGlobal('fetch', spy);
  return spy;
}

describe('egress-policy helper contract', () => {
  it('enforces deny-first precedence, wildcards, and pattern-specific Undo', () => {
    const base = makePolicy({
      policy: 'open',
      allowedHosts: ['*.example.com'],
      blockedHosts: ['api.example.com', '*.tracking.example'],
      effectiveSecretHosts: ['api.example.com'],
      revision: 3,
    });

    const apiEval = evaluateHostInPolicy(base, 'API.EXAMPLE.COM.');
    expect(apiEval.isAllowed).toBe(false);
    expect(apiEval.isBlockedExact).toBe(true);

    const subTrack = evaluateHostInPolicy(base, 'a.b.tracking.example');
    expect(subTrack.isAllowed).toBe(false);
    expect(subTrack.isBlockedWildcard).toBe(true);
    expect(subTrack.matchedBlock).toBe('*.tracking.example');

    // Apex domain is NOT matched by *.tracking.example
    const apexTrack = evaluateHostInPolicy(base, 'tracking.example');
    expect(apexTrack.isAllowed).toBe(true);

    // Block A, Block B, Undo A -> B stays blocked!
    const allowlistBase = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['a.example', 'b.example'],
      blockedHosts: [],
      revision: 1,
    });
    const mutA = computeTriageMutation(allowlistBase, 'a.example', 'block');
    const afterA = makePolicy({
      ...allowlistBase,
      allowedHosts: mutA.allowedHosts,
      blockedHosts: mutA.blockedHosts,
      revision: 2,
    });
    const mutB = computeTriageMutation(afterA, 'b.example', 'block');
    const afterB = makePolicy({
      ...afterA,
      allowedHosts: mutB.allowedHosts,
      blockedHosts: mutB.blockedHosts,
      revision: 3,
    });
    const undoA = computeUndoMutation(afterB, mutA.undo);
    expect(undoA).toEqual({
      allowedHosts: ['b.example', 'a.example'].filter((x) => x === 'a.example'),
      blockedHosts: ['b.example'],
    });

    // If a.example was modified externally before Undo A, computeUndoMutation refuses!
    const externallyUnblocked = makePolicy({
      ...afterB,
      blockedHosts: ['b.example'],
      allowedHosts: ['a.example'],
      revision: 4,
    });
    expect(computeUndoMutation(externallyUnblocked, mutA.undo)).toBeNull();
  });
});

describe('BotSecretsView egress activity triage', () => {
  beforeEach(() => {
    setAccessToken(null);
    mockPush.mockReset();
    mockReplace.mockReset();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    setAccessToken(null);
  });

  it('supports one-click Allow/Block, syncs matching rows, respects wildcards, and isolates Block A -> Block B -> Undo A', async () => {
    const workspace = makeWorkspace('owner');
    const bot = makeBot();
    let policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['pypi.org'],
      blockedHosts: ['*.tracking.example'],
      revision: 1,
      acknowledgedRevision: 1,
      enforcementStatus: 'applied',
    });
    const logs: EgressLogEntry[] = [
      makeLog({
        id: 'log-crates-1',
        host: 'crates.io',
        action: 'blocked_host',
      }),
      makeLog({
        id: 'log-crates-2',
        host: 'crates.io',
        method: 'GET',
        path: '/api/v1/crates/serde',
        action: 'blocked_host',
      }),
      makeLog({
        id: 'log-pypi',
        host: 'pypi.org',
        method: 'GET',
        path: '/simple/tenacity/',
        action: 'tunnel_passthrough',
        statusCode: 200,
      }),
      makeLog({
        id: 'log-wild',
        host: 'ads.tracking.example',
        action: 'blocked_host',
      }),
      makeLog({
        id: 'log-secret',
        host: 'api.github.com',
        method: 'POST',
        path: '/graphql',
        action: 'mitm_token_added',
        statusCode: 200,
        secretId: 'sec-1',
        secretScope: 'bot',
        secretName: 'GITHUB_TOKEN',
      }),
    ];

    installFetchMock((url, method, body) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('owner'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
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
        if (parsed.expectedRevision !== policy.revision) {
          return jsonResponse(
            { error: { code: 'policy_conflict', message: 'stale' } },
            409,
          );
        }
        policy = makePolicy({
          policy: parsed.policy,
          allowedHosts: parsed.allowedHosts,
          blockedHosts: parsed.blockedHosts ?? policy.blockedHosts,
          revision: policy.revision + 1,
          acknowledgedRevision: policy.revision + 1,
          enforcementStatus: 'applied',
        });
        return jsonResponse(policy, 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs }, 200);
      }
      return jsonResponse(
        { error: { code: 'not_found', message: '404' } },
        404,
      );
    });

    renderWithProviders(<BotSecretsView workspace={workspace} bot={bot} />);

    // Wildcard-blocked row shows matching rule + Edit rule, NEVER Allow!
    expect(
      await screen.findByText('Blocked by *.tracking.example'),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Allow ads.tracking.example' }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit rule' })).toHaveClass(
      'min-h-[44px]',
    );

    // Secret row stays read-only Via secret.
    expect(screen.getByText('Via secret')).toBeInTheDocument();

    // Two rows for crates.io initially expose Allow crates.io; clicking one syncs both rows!
    const allowCratesButtons = screen.getAllByRole('button', {
      name: 'Allow crates.io',
    });
    expect(allowCratesButtons).toHaveLength(2);
    expect(allowCratesButtons[0]).toHaveClass('min-h-[44px]');

    fireEvent.click(allowCratesButtons[0]!);

    await waitFor(() => {
      expect(screen.getAllByText('Added to allowed hosts')).toHaveLength(2);
    });
    expect(policy.allowedHosts).toContain('crates.io');

    // Block pypi.org as well (Block B), then Undo crates.io (Undo A) -> pypi.org stays blocked!
    fireEvent.click(screen.getByRole('button', { name: 'Block pypi.org' }));
    await waitFor(() => {
      expect(screen.getByText('Added to blocked hosts')).toBeInTheDocument();
    });
    expect(policy.blockedHosts).toContain('pypi.org');

    const undoCrates = screen.getAllByRole('button', {
      name: 'Undo policy change for crates.io',
    });
    fireEvent.click(undoCrates[0]!);

    await waitFor(() => {
      expect(
        screen.getByText('Reverted policy change for crates.io.'),
      ).toBeInTheDocument();
    });
    expect(policy.allowedHosts).not.toContain('crates.io');
    expect(policy.blockedHosts).toContain('pypi.org');
  });

  it('preserves unsaved form drafts across log triage and prompts to load saved policy', async () => {
    const workspace = makeWorkspace('owner');
    const bot = makeBot();
    let policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['pypi.org'],
      blockedHosts: [],
      revision: 1,
    });
    const logs = [
      makeLog({ id: 'log-1', host: 'crates.io', action: 'blocked_host' }),
    ];
    const patchBodies: Array<Record<string, unknown>> = [];

    installFetchMock((url, method, body) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('owner'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
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
          blockedHosts: string[];
          expectedRevision: number;
        };
        patchBodies.push(parsed);
        policy = makePolicy({
          policy: parsed.policy,
          allowedHosts: parsed.allowedHosts,
          blockedHosts: parsed.blockedHosts,
          revision: policy.revision + 1,
          acknowledgedRevision: policy.revision + 1,
        });
        return jsonResponse(policy, 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotSecretsView workspace={workspace} bot={bot} />);
    await screen.findByRole('button', { name: 'Allow crates.io' });

    // Edit form draft (switch to Open internet + add a draft blocked host) WITHOUT saving.
    fireEvent.click(
      screen.getByRole('radio', {
        name: `${enMessages.secrets.policyOpenTitle} ${enMessages.secrets.policyOpenDesc}`,
      }),
    );
    fireEvent.change(screen.getByLabelText('Add blocked host or wildcard'), {
      target: { value: 'draft.blocked.example' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Block host' }));

    // Trigger log-row triage on crates.io: must save ONLY crates.io against saved allowlist policy!
    fireEvent.click(screen.getByRole('button', { name: 'Allow crates.io' }));

    await waitFor(() => {
      expect(patchBodies).toHaveLength(1);
    });
    expect(patchBodies[0]).toEqual({
      policy: 'allowlist',
      allowedHosts: ['pypi.org', 'crates.io'],
      blockedHosts: [],
      expectedRevision: 1,
    });

    // Unsaved draft edits remain intact and draft conflict banner is shown.
    expect(screen.getByText('draft.blocked.example')).toBeInTheDocument();
    expect(
      screen.getByText(
        'The saved policy updated to revision 2 while you have unsaved edits in this form.',
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep my draft' }));
    expect(
      screen.queryByRole('button', { name: 'Keep my draft' }),
    ).not.toBeInTheDocument();
    expect(screen.getByText('draft.blocked.example')).toBeInTheDocument();
    expect(patchBodies).toHaveLength(1);
  });

  it('shows 202 pending enforcement separately from applied, reconciles lost responses, and locks writes on failed reload', async () => {
    const workspace = makeWorkspace('owner');
    const bot = makeBot();
    let policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['pypi.org'],
      blockedHosts: [],
      revision: 1,
      acknowledgedRevision: 1,
      enforcementStatus: 'applied',
    });
    const logs = [
      makeLog({ id: 'log-1', host: 'crates.io', action: 'blocked_host' }),
      makeLog({ id: 'log-2', host: 'other.example', action: 'blocked_host' }),
    ];

    let patchAttempt = 0;
    let getBehavior: 'normal' | 'fail' = 'normal';

    installFetchMock((url, method, body) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('owner'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'GET'
      ) {
        if (getBehavior === 'fail') {
          return Promise.reject(new TypeError('network down'));
        }
        return jsonResponse(policy, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'PATCH'
      ) {
        patchAttempt += 1;
        const parsed = JSON.parse(body ?? '{}') as {
          policy: 'allowlist' | 'open';
          allowedHosts: string[];
          blockedHosts: string[];
        };
        if (patchAttempt === 1) {
          // Return 202 Pending Enforcement!
          policy = makePolicy({
            policy: parsed.policy,
            allowedHosts: parsed.allowedHosts,
            blockedHosts: parsed.blockedHosts,
            revision: 2,
            acknowledgedRevision: 1,
            enforcementStatus: 'pending',
          });
          return jsonResponse(policy, 202);
        }
        // Second PATCH loses connection AND GET reload also fails -> unknown state!
        getBehavior = 'fail';
        return Promise.reject(new TypeError('lost response'));
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotSecretsView workspace={workspace} bot={bot} />);
    const allowCrates = await screen.findByRole('button', {
      name: 'Allow crates.io',
    });
    fireEvent.click(allowCrates);

    // 202 shows pending enforcement, NOT applied!
    expect(
      await screen.findByText('Saved · proxy pending'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        'Outbound policy saved (revision 2). Waiting for the running proxy to adopt it.',
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText('Added to allowed hosts'),
    ).not.toBeInTheDocument();

    // Second PATCH fails with lost response and failed reload -> visibly unknown + writes locked!
    fireEvent.click(
      screen.getByRole('button', { name: 'Allow other.example' }),
    );
    expect(
      await screen.findByText(
        'Could not verify the current saved policy after an interrupted request. Reload before making changes.',
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Allow other.example' }),
    ).toBeDisabled();
    expect(
      screen.getByRole('button', { name: 'Save outbound policy' }),
    ).toBeDisabled();

    // Clicking Reload policy when server recovers restores controls cleanly.
    getBehavior = 'normal';
    fireEvent.click(screen.getByRole('button', { name: 'Reload policy' }));
    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: 'Allow other.example' }),
      ).not.toBeDisabled();
    });
  });

  it('keeps triage and blocklist read-only for viewers and blocks secret-card hosts via policy card', async () => {
    const viewerWs = makeWorkspace('viewer');
    const bot = makeBot();
    const policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['pypi.org'],
      blockedHosts: ['api.github.com'],
      effectiveSecretHosts: ['api.github.com'],
      revision: 1,
    });
    const logs = [
      makeLog({
        id: 'log-secret',
        host: 'api.github.com',
        action: 'mitm_token_added',
        secretId: 'sec-1',
        secretScope: 'bot',
        secretName: 'GITHUB_TOKEN',
      }),
      makeLog({ id: 'log-blocked', host: 'crates.io', action: 'blocked_host' }),
    ];

    installFetchMock((url) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('viewer'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/egress') {
        return jsonResponse(policy, 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotSecretsView workspace={viewerWs} bot={bot} />);
    expect(await screen.findByText('Via secret · blocked')).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Allow crates.io' }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Save outbound policy' }),
    ).not.toBeInTheDocument();
  });

  it('refuses Undo when affected rule changed concurrently (409) without overwriting newer change', async () => {
    const workspace = makeWorkspace('owner');
    const bot = makeBot();
    let policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['pypi.org'],
      blockedHosts: [],
      revision: 1,
    });
    const logs = [
      makeLog({ id: 'log-1', host: 'crates.io', action: 'blocked_host' }),
    ];

    let patchCalls = 0;
    installFetchMock((url, method, body) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('owner'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
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
        patchCalls += 1;
        const parsed = JSON.parse(body ?? '{}') as {
          policy: 'allowlist' | 'open';
          allowedHosts: string[];
          blockedHosts: string[];
        };
        if (patchCalls === 1) {
          policy = makePolicy({
            policy: parsed.policy,
            allowedHosts: parsed.allowedHosts,
            blockedHosts: parsed.blockedHosts,
            revision: 2,
          });
          return jsonResponse(policy, 200);
        }
        // Simulate another browser blocking crates.io at revision 3 before Undo runs!
        policy = makePolicy({
          policy: 'allowlist',
          allowedHosts: ['pypi.org'],
          blockedHosts: ['crates.io', 'extra.example'],
          revision: 3,
        });
        return jsonResponse(
          { error: { code: 'policy_conflict', message: 'stale' } },
          409,
        );
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotSecretsView workspace={workspace} bot={bot} />);
    fireEvent.click(
      await screen.findByRole('button', { name: 'Allow crates.io' }),
    );
    const undoBtn = await screen.findByRole('button', {
      name: 'Undo policy change for crates.io',
    });

    fireEvent.click(undoBtn);
    expect(
      await screen.findByText(
        'The rule for crates.io changed since your last action, so Undo was skipped.',
      ),
    ).toBeInTheDocument();
    // Only 2 PATCH calls occurred (Allow + conflicted Undo); never issued a blind overwrite!
    expect(patchCalls).toBe(2);
    expect(policy.blockedHosts).toEqual(['crates.io', 'extra.example']);
  });

  it('serializes rapid row clicks and reverts cleanly on definite 403 rejection', async () => {
    const workspace = makeWorkspace('owner');
    const bot = makeBot();
    const policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: [],
      blockedHosts: [],
      revision: 1,
    });
    const logs = [
      makeLog({ id: 'log-1', host: 'crates.io', action: 'blocked_host' }),
    ];

    let patchCalls = 0;
    let resolvePatch: ((resp: Response) => void) | null = null;

    installFetchMock((url, method) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('owner'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
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
        patchCalls += 1;
        return new Promise<Response>((resolve) => {
          resolvePatch = resolve;
        });
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return jsonResponse({ logs }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotSecretsView workspace={workspace} bot={bot} />);
    const allowBtn = await screen.findByRole('button', {
      name: 'Allow crates.io',
    });

    // Rapid double click triggers only a single in-flight PATCH.
    fireEvent.click(allowBtn);
    fireEvent.click(allowBtn);
    expect(patchCalls).toBe(1);
    expect(await screen.findByText('Saving…')).toBeInTheDocument();

    // Definite 403 rejection surfaces translated alert and keeps crates.io un-allowed.
    resolvePatch!(
      jsonResponse(
        { error: { code: 'auth.forbidden', message: 'forbidden' } },
        403,
      ),
    );
    expect(
      await screen.findByText(
        'You do not have permission to perform this action in this workspace.',
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Allow crates.io' }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText('Added to allowed hosts'),
    ).not.toBeInTheDocument();
  });

  it('paginates egress activity logs (10 per page), filters live by search query, and loads older entries via offset', async () => {
    const workspace = makeWorkspace('owner');
    const bot = makeBot();
    const policy = makePolicy({
      policy: 'allowlist',
      allowedHosts: ['pypi.org'],
      blockedHosts: [],
      revision: 1,
    });

    const firstBatch: EgressLogEntry[] = Array.from({ length: 200 }, (_, i) =>
      makeLog({
        id: `log-${i + 1}`,
        host: `service-${String(i + 1).padStart(3, '0')}.example`,
        action: i % 2 === 0 ? 'blocked_host' : 'tunnel_passthrough',
        statusCode: i % 2 === 0 ? 403 : 200,
        createdAt: `2026-11-18T12:${String(Math.floor(i / 60)).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}Z`,
      }),
    );
    const olderBatch: EgressLogEntry[] = [
      makeLog({
        id: 'log-201',
        host: 'legacy-archive.example',
        action: 'blocked_host',
        statusCode: 403,
        createdAt: '2026-11-17T08:00:00Z',
      }),
    ];

    const requestedUrls: string[] = [];
    let failOlder = true;

    installFetchMock((url, method) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse(makeSession('owner'), 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/egress' &&
        method === 'GET'
      ) {
        return jsonResponse(policy, 200);
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        requestedUrls.push(url);
        if (url.includes('offset=200')) {
          if (failOlder) {
            return jsonResponse(
              { error: { code: 'internal_error', message: 'db unavailable' } },
              503,
            );
          }
          return jsonResponse({ logs: olderBatch }, 200);
        }
        return jsonResponse({ logs: firstBatch }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotSecretsView workspace={workspace} bot={bot} />);

    // Page 1 shows entries 1..10 of 200, without forcing min-w-[840px] clipping.
    expect(await screen.findByText('Showing 1–10 of 200')).toBeInTheDocument();
    expect(screen.getByText('Page 1 of 20')).toBeInTheDocument();
    const table = screen.getByRole('table', {
      name: 'Recent egress activity table',
    });
    expect(table).not.toHaveClass('min-w-[840px]');
    expect(
      screen.getByRole('button', { name: 'Allow service-001.example' }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/service-011\.example/)).not.toBeInTheDocument();

    const prevButton = screen.getByRole('button', { name: 'Previous' });
    const nextButton = screen.getByRole('button', { name: 'Next' });
    expect(prevButton).toBeDisabled();
    expect(nextButton).toBeEnabled();

    // Navigate to page 2 -> entries 11..20 appear and Previous is enabled.
    fireEvent.click(nextButton);
    expect(screen.getByText('Showing 11–20 of 200')).toBeInTheDocument();
    expect(screen.getByText('Page 2 of 20')).toBeInTheDocument();
    expect(prevButton).toBeEnabled();
    expect(
      screen.getByRole('button', { name: 'Allow service-011.example' }),
    ).toBeInTheDocument();

    // Live search resets back to page 1 and filters by host substring.
    const searchInput = screen.getByLabelText('Search egress activity');
    fireEvent.change(searchInput, { target: { value: 'service-04' } });
    expect(screen.getByText('Showing 1–10 of 10')).toBeInTheDocument();
    expect(screen.getByText('Page 1 of 1')).toBeInTheDocument();
    expect(screen.getByText(/service-040\.example/)).toBeInTheDocument();
    expect(nextButton).toBeDisabled();

    // Unmatched search shows empty filtered state with clear button.
    fireEvent.change(searchInput, {
      target: { value: 'nonexistent-domain.invalid' },
    });
    expect(
      screen.getByText('No egress entries match your search filter.'),
    ).toBeInTheDocument();

    // Clear search restores paginated 200-item view.
    fireEvent.click(
      screen.getAllByRole('button', { name: 'Clear search' })[0]!,
    );
    expect(screen.getByText('Showing 1–10 of 200')).toBeInTheDocument();

    // Loading older entries handles server errors gracefully, then succeeds via offset=200.
    const loadOlderBtn = screen.getByRole('button', {
      name: 'Load older entries',
    });
    fireEvent.click(loadOlderBtn);
    expect(
      await screen.findByText(
        'Something went wrong talking to the server. Please try again.',
      ),
    ).toBeInTheDocument();

    failOlder = false;
    fireEvent.click(screen.getByRole('button', { name: 'Load older entries' }));
    expect(await screen.findByText('Showing 1–10 of 201')).toBeInTheDocument();
    expect(requestedUrls).toContain(
      '/api/workspaces/ws-1/bots/bot-1/egress/logs?limit=200&offset=200',
    );
    expect(
      screen.queryByRole('button', { name: 'Load older entries' }),
    ).not.toBeInTheDocument();

    // Search finds the newly loaded older entry from page 21.
    fireEvent.change(screen.getByLabelText('Search egress activity'), {
      target: { value: 'legacy-archive' },
    });
    expect(screen.getByText('Showing 1–1 of 1')).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Allow legacy-archive.example' }),
    ).toBeInTheDocument();
  });
});
