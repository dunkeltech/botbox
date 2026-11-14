import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mockedAuth = vi.hoisted(() => ({
  activeWorkspace: {
    id: 'ws-1',
    name: 'dunkel.Tech',
    slug: 'dunkel-tech',
    role: 'owner' as const,
    defaultDriver: 'docker' as const,
    defaultLocale: 'en',
    screenshotRetentionDays: 7,
  },
}));

vi.mock('next/navigation', () => ({
  usePathname: () => '/settings/guardrails',
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
}));

vi.mock('@/context/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'user-1', email: 'owner@example.com', displayName: 'Owner' },
    workspaces: [mockedAuth.activeWorkspace],
    activeWorkspace: mockedAuth.activeWorkspace,
    setupRequired: false,
    isLoading: false,
    applySession: vi.fn(),
    selectWorkspace: vi.fn(),
    reloadWorkspaces: vi.fn(),
    signOut: vi.fn(),
    refreshSetupStatus: vi.fn(),
  }),
}));

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return {
    ...actual,
    requestJson: vi.fn(),
  };
});

import { GuardrailNoticeCard } from '@/components/chat/GuardrailNoticeCard';
import { LLMGuardrailsPanel } from '@/components/settings/LLMGuardrailsPanel';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { ApiClientError, requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

import messages from '../../messages/en.json';

type WorkspaceGuardrailsResponse =
  components['schemas']['WorkspaceGuardrailsResponse'];
type WorkspaceGuardrailPolicy =
  components['schemas']['WorkspaceGuardrailPolicy'];
type WorkspaceGuardrailRule = components['schemas']['WorkspaceGuardrailRule'];
type GuardrailEventListResponse =
  components['schemas']['GuardrailEventListResponse'];

function renderWithIntl(ui: ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      {ui}
    </NextIntlClientProvider>,
  );
}

function makePolicy(enabled = false): WorkspaceGuardrailPolicy {
  return {
    workspaceId: 'ws-1',
    enabled,
    failureMode: 'fail_closed',
    secrets: { enabled: true, direction: 'both', action: 'block' },
    pii: {
      enabled: true,
      direction: 'both',
      action: 'redact',
      entities: ['email', 'iban', 'card', 'phone'],
    },
    promptInjection: { enabled: true, direction: 'input', action: 'audit' },
    revision: 1,
    updatedAt: '2026-11-19T00:00:00Z',
  };
}

function makeRule(
  overrides: Partial<WorkspaceGuardrailRule> = {},
): WorkspaceGuardrailRule {
  return {
    id: 'rule-1',
    workspaceId: 'ws-1',
    slug: 'customer-account-ids',
    name: 'Customer account IDs',
    matchMode: 'regex',
    pattern: '\\bACCT-[0-9]{6}\\b',
    direction: 'both',
    action: 'redact',
    replacementToken: '[REDACTED:ACCT_ID]',
    enabled: true,
    createdAt: '2026-11-19T00:00:00Z',
    updatedAt: '2026-11-19T00:00:00Z',
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('WorkspaceSettingsShell guardrails link', () => {
  it('marks LLM guardrails as active in Security & integrations', async () => {
    vi.mocked(requestJson).mockResolvedValue({ approvals: [] });
    renderWithIntl(
      <WorkspaceSettingsShell>
        <div>Panel content</div>
      </WorkspaceSettingsShell>,
    );

    const link = await screen.findByRole('link', { name: 'LLM guardrails' });
    expect(link).toHaveAttribute('href', '/settings/guardrails');
    expect(link).toHaveAttribute('aria-current', 'page');
  });
});

describe('LLMGuardrailsPanel', () => {
  it('preserves an unsaved policy draft when a custom rule refreshes the settings', async () => {
    const path = '/api/workspaces/ws-1/guardrails';
    let policy = makePolicy(true);
    let rule = makeRule();
    const reqMock = vi.mocked(requestJson).mockImplementation((url) => {
      if (url === path) {
        return Promise.resolve({
          policy,
          rules: [rule],
          summary24h: {
            totalEvaluations: 0,
            redactedCount: 0,
            blockedCount: 0,
            auditedCount: 0,
          },
          activeRuleCount: rule.enabled ? 4 : 3,
          coveredProviders: ['anthropic'],
        } satisfies WorkspaceGuardrailsResponse);
      }
      if (url === `${path}/rules/${rule.id}`) {
        rule = { ...rule, enabled: false };
        return Promise.resolve(rule);
      }
      if (url === `${path}/policy`) {
        policy = {
          ...policy,
          failureMode: 'fail_open',
          revision: policy.revision + 1,
        };
        return Promise.resolve(policy);
      }
      return Promise.resolve({ events: [] });
    });

    renderWithIntl(<LLMGuardrailsPanel workspaceId="ws-1" role="owner" />);

    fireEvent.click(
      await screen.findByRole('button', { name: 'Fail open (Audit)' }),
    );
    const ruleSwitch = { name: 'Toggle Customer account IDs' };
    fireEvent.click(screen.getByRole('switch', ruleSwitch));
    await screen.findByRole('switch', { ...ruleSwitch, checked: false });

    expect(
      screen.getByRole('button', { name: 'Fail open (Audit)' }),
    ).toHaveAttribute('aria-pressed', 'true');
    expect(reqMock.mock.calls.some(([url]) => url === `${path}/policy`)).toBe(
      false,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Save policy' }));
    const save = reqMock.mock.calls.find(([url]) => url === `${path}/policy`);
    expect(save?.[1]).toMatchObject({
      method: 'PUT',
      body: { failureMode: 'fail_open' },
    });
    await screen.findByText('Workspace guardrail policy updated.');
    expect(
      screen.getByRole('button', { name: 'Fail open (Audit)' }),
    ).toHaveAttribute('aria-pressed', 'true');
  });

  it('supports owner policy editing, PII selection, custom rule CRUD, Quick Test, activity filters, and API error messages', async () => {
    const reqMock = vi.mocked(requestJson);
    let currentPolicy = makePolicy(false);
    let currentRules: WorkspaceGuardrailRule[] = [];
    const currentEvents: GuardrailEventListResponse['events'] = [
      {
        id: 'ev-1',
        workspaceId: 'ws-1',
        botId: 'bot-1',
        botName: 'Atlas',
        provider: 'anthropic',
        model: 'claude-sonnet-4-5',
        routePath: '/v1/messages',
        direction: 'input',
        action: 'blocked',
        statusCode: 403,
        latencyMs: 2,
        ruleSlugs: ['secrets-leak-guard'],
        matchLabels: ['api_key'],
        matchCount: 1,
        summary: '1 policy violation(s) detected · request rejected',
        createdAt: '2026-11-19T00:15:30Z',
      },
    ];

    reqMock.mockImplementation((path: string, opts?: unknown) => {
      const options = (opts ?? {}) as {
        method?: string;
        body?: Record<string, unknown>;
      };
      if (path === '/api/workspaces/ws-1/guardrails') {
        const res: WorkspaceGuardrailsResponse = {
          policy: currentPolicy,
          rules: currentRules,
          summary24h: {
            totalEvaluations: 12,
            redactedCount: 2,
            blockedCount: 1,
            auditedCount: 0,
          },
          activeRuleCount: currentPolicy.enabled ? 3 + currentRules.length : 0,
          coveredProviders: [
            'anthropic',
            'openai',
            'openai-plan',
            'openrouter',
            'google',
          ],
        };
        return Promise.resolve(res);
      }
      if (
        path === '/api/workspaces/ws-1/guardrails/policy' &&
        options.method === 'PUT'
      ) {
        const body = options.body ?? {};
        currentPolicy = {
          ...currentPolicy,
          enabled: Boolean(body.enabled),
          failureMode:
            body.failureMode as WorkspaceGuardrailPolicy['failureMode'],
          secrets: body.secrets as WorkspaceGuardrailPolicy['secrets'],
          pii: body.pii as WorkspaceGuardrailPolicy['pii'],
          promptInjection:
            body.promptInjection as WorkspaceGuardrailPolicy['promptInjection'],
          revision: currentPolicy.revision + 1,
        };
        return Promise.resolve(currentPolicy);
      }
      if (
        path === '/api/workspaces/ws-1/guardrails/test' &&
        options.method === 'POST'
      ) {
        return Promise.resolve({
          valid: true,
          matchCount: 1,
          preview: 'Migrate [REDACTED:ACCT_ID] to staging',
        });
      }
      if (
        path === '/api/workspaces/ws-1/guardrails/rules' &&
        options.method === 'POST'
      ) {
        const body = options.body ?? {};
        const slug = typeof body.slug === 'string' ? body.slug : '';
        const name = typeof body.name === 'string' ? body.name : '';
        const pattern = typeof body.pattern === 'string' ? body.pattern : '';
        const replacementToken =
          typeof body.replacementToken === 'string'
            ? body.replacementToken
            : '[REDACTED]';
        if (currentRules.some((r) => r.slug === slug)) {
          return Promise.reject(
            new ApiClientError(
              409,
              'guardrail.rule_conflict',
              'duplicate slug',
            ),
          );
        }
        const created = makeRule({
          slug,
          name,
          pattern,
          replacementToken,
        });
        currentRules = [created];
        return Promise.resolve(created);
      }
      if (
        path === '/api/workspaces/ws-1/guardrails/rules/rule-1' &&
        options.method === 'PATCH'
      ) {
        const body = options.body ?? {};
        currentRules = currentRules.map((r) =>
          r.id === 'rule-1'
            ? {
                ...r,
                name: typeof body.name === 'string' ? body.name : r.name,
                enabled:
                  typeof body.enabled === 'boolean' ? body.enabled : r.enabled,
              }
            : r,
        );
        return Promise.resolve(currentRules[0]);
      }
      if (
        path === '/api/workspaces/ws-1/guardrails/rules/rule-1' &&
        options.method === 'DELETE'
      ) {
        currentRules = [];
        return Promise.resolve(undefined);
      }
      if (path.startsWith('/api/workspaces/ws-1/guardrails/events')) {
        return Promise.resolve({ events: currentEvents });
      }
      return Promise.resolve({});
    });

    renderWithIntl(<LLMGuardrailsPanel workspaceId="ws-1" role="owner" />);

    expect(await screen.findByText('Off · opt-in')).toBeInTheDocument();
    expect(
      screen.getByText(
        'No custom regex or keyword guardrail rules configured yet.',
      ),
    ).toBeInTheDocument();

    // Toggle master opt-in switch on.
    const masterSwitch = screen.getByRole('switch', {
      name: 'Enable LLM proxy guardrails for this workspace',
    });
    fireEvent.click(masterSwitch);
    expect(
      await screen.findByText('Workspace guardrail policy updated.'),
    ).toBeInTheDocument();
    expect(await screen.findByText('Enforcing · 3 active')).toBeInTheDocument();

    // Change failure behavior and PII entities, then Save policy.
    const failureGroup = screen.getByRole('group', {
      name: 'Failure behavior',
    });
    fireEvent.click(
      within(failureGroup).getByRole('button', { name: 'Fail open (Audit)' }),
    );
    fireEvent.click(screen.getByRole('button', { name: 'IBAN' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save policy' }));

    await waitFor(() => {
      const putCall = reqMock.mock.calls.find(
        ([p, opts]) =>
          p === '/api/workspaces/ws-1/guardrails/policy' &&
          (opts as { method?: string })?.method === 'PUT',
      );
      expect(putCall).toBeDefined();
    });

    // Quick Test and create custom rule.
    fireEvent.change(screen.getByLabelText('Rule name'), {
      target: { value: 'Customer account IDs' },
    });
    fireEvent.change(screen.getByLabelText('Pattern / keywords (RE2 engine)'), {
      target: { value: '\\bACCT-[0-9]{6}\\b' },
    });
    fireEvent.change(screen.getByLabelText('Replacement token'), {
      target: { value: '[REDACTED:ACCT_ID]' },
    });
    fireEvent.change(screen.getByLabelText('Quick test'), {
      target: { value: 'Migrate ACCT-492018 to staging' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Test rule' }));
    expect(
      await screen.findByText(
        '1 match → Migrate [REDACTED:ACCT_ID] to staging',
      ),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', { name: 'Save guardrail rule' }),
    );
    expect(
      await screen.findByText('Guardrail rule saved.'),
    ).toBeInTheDocument();
    expect(screen.getByText('customer-account-ids')).toBeInTheDocument();

    // Creating a duplicate slug surfaces localized 409 error envelope.
    fireEvent.change(screen.getByLabelText('Rule name'), {
      target: { value: 'Duplicate rule' },
    });
    fireEvent.change(screen.getByLabelText('Rule slug'), {
      target: { value: 'customer-account-ids' },
    });
    fireEvent.change(screen.getByLabelText('Pattern / keywords (RE2 engine)'), {
      target: { value: 'foo' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Save guardrail rule' }),
    );
    expect(
      await screen.findByText(
        'A guardrail rule with that slug already exists in this workspace.',
      ),
    ).toBeInTheDocument();

    // Edit & delete custom rule.
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    expect(
      screen.getByText('Edit guardrail rule: Customer account IDs'),
    ).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Rule name'), {
      target: { value: 'Customer account mask' },
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Update guardrail rule' }),
    );
    expect(
      await screen.findByText('Customer account mask'),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Remove' }));
    fireEvent.click(screen.getByRole('button', { name: 'Confirm remove' }));
    expect(
      await screen.findByText('Guardrail rule removed.'),
    ).toBeInTheDocument();

    // Dark activity log filters update query params cleanly.
    const activitySection = screen.getByRole('region', {
      name: 'Proxy decision stream',
    });
    fireEvent.click(
      within(activitySection).getByRole('button', { name: 'Blocked' }),
    );
    fireEvent.click(
      within(activitySection).getByRole('button', { name: 'Input' }),
    );

    await waitFor(() => {
      expect(reqMock).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/guardrails/events?action=blocked&direction=input',
      );
    });
    expect(within(activitySection).getByText('Atlas')).toBeInTheDocument();
    expect(
      within(activitySection).getByText('BLOCKED 403'),
    ).toBeInTheDocument();
  });

  it('locks down controls to read-only for member and viewer roles', async () => {
    const reqMock = vi.mocked(requestJson);
    reqMock.mockImplementation((path: string) => {
      if (path === '/api/workspaces/ws-1/guardrails') {
        return Promise.resolve({
          policy: makePolicy(true),
          rules: [makeRule()],
          summary24h: {
            totalEvaluations: 1,
            redactedCount: 0,
            blockedCount: 0,
            auditedCount: 0,
          },
          activeRuleCount: 4,
          coveredProviders: ['anthropic'],
        } satisfies WorkspaceGuardrailsResponse);
      }
      return Promise.resolve({ events: [] });
    });

    for (const role of ['member', 'viewer'] as const) {
      renderWithIntl(<LLMGuardrailsPanel workspaceId="ws-1" role={role} />);
      expect(
        await screen.findByText(
          'Only workspace owners and admins can change guardrail policies or custom rules. Members and viewers have read-only access.',
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByRole('switch', {
          name: 'Enable LLM proxy guardrails for this workspace',
        }),
      ).toBeDisabled();
      expect(screen.queryByRole('button', { name: 'Save policy' })).toBeNull();
      expect(
        screen.queryByRole('button', { name: 'Save guardrail rule' }),
      ).toBeNull();
      expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
      expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull();
      cleanup();
    }
  });
});

describe('GuardrailNoticeCard', () => {
  it('renders blue redacted notice with Inspect callback and enforces RBAC link visibility on dashed amber blocked card', () => {
    const onInspect = vi.fn();

    renderWithIntl(
      <GuardrailNoticeCard
        guardrail={{
          eventId: 'ev-red',
          turnId: 'turn-1',
          direction: 'input',
          action: 'redacted',
          provider: 'anthropic',
          model: 'claude-sonnet-4-5',
          routePath: '/v1/messages',
          statusCode: 200,
          latencyMs: 4,
          ruleSlugs: ['pii-protection'],
          matchLabels: ['email', 'iban'],
          matchCount: 2,
          summary: '2 match(es) masked (email, iban)',
        }}
        role="member"
        onInspect={onInspect}
      />,
    );

    expect(
      screen.getByText('LLM guardrail redacted prompt'),
    ).toBeInTheDocument();
    expect(screen.getByText('Input · 2 matches')).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole('button', { name: 'Inspect in Tool calls' }),
    );
    expect(onInspect).toHaveBeenCalledWith('turn-1');
    cleanup();

    // Admin sees Open Guardrails settings link on blocked 403 card; viewer does not.
    const blockedData = {
      eventId: 'ev-blk',
      turnId: 'turn-2',
      direction: 'output' as const,
      action: 'blocked' as const,
      provider: 'openai',
      model: 'gpt-5',
      routePath: '/v1/responses',
      statusCode: 403,
      latencyMs: 3,
      ruleSlugs: ['secrets-leak-guard'],
      matchLabels: ['api_key'],
      matchCount: 1,
      summary: '1 policy violation(s) detected · response rejected',
    };

    renderWithIntl(
      <GuardrailNoticeCard
        guardrail={blockedData}
        role="admin"
        onInspect={onInspect}
      />,
    );
    expect(screen.getByText('Blocked · 403')).toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: 'Open Guardrails settings' }),
    ).toHaveAttribute('href', '/settings/guardrails');
    cleanup();

    renderWithIntl(
      <GuardrailNoticeCard
        guardrail={blockedData}
        role="viewer"
        onInspect={onInspect}
      />,
    );
    expect(screen.getByText('Blocked · 403')).toBeInTheDocument();
    expect(
      screen.queryByRole('link', { name: 'Open Guardrails settings' }),
    ).toBeNull();
  });
});
