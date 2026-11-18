// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

let mockPathname = '/bots/bot-1/settings';
let mockParams = { botId: 'bot-1' };

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => mockPathname,
  useParams: () => mockParams,
}));

import BotSecretsPage from '@/app/bots/[botId]/secrets/page';
import BotBudgetsSettingsPage from '@/app/bots/[botId]/settings/budgets/page';
import BotIntegrationsSettingsPage from '@/app/bots/[botId]/settings/integrations/page';
import BotKnowledgeSettingsPage from '@/app/bots/[botId]/settings/knowledge/page';
import BotSettingsPage from '@/app/bots/[botId]/settings/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  WorkspaceRole,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../messages/en.json';

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
    accessToken: 'jwt-bot-settings-test',
  };
}

function makeBot(ownerUserId = 'user-1'): Bot {
  return {
    id: 'bot-1',
    workspaceId: 'ws-1',
    ownerUserId,
    name: 'Atlas',
    description: 'Verifies release pipelines on a Linux desktop.',
    avatarSeed: 'atlas-005a',
    llmMode: 'cli',
    llmProvider: 'anthropic',
    llmModel: 'claude-sonnet-4-20250514',
    driverType: 'docker',
    replyLanguage: 'en',
    turnTimeoutMinutes: 10,
    status: 'action',
    mood: 'action',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
  };
}

function renderWithProviders(ui: React.ReactElement) {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      <QueryProvider>
        <AuthProvider>{ui}</AuthProvider>
      </QueryProvider>
    </NextIntlClientProvider>,
  );
}

function stubSettingsFetch(options?: {
  role?: WorkspaceRole;
  botOwnerId?: string;
  botStatus?: number;
}) {
  const role = options?.role ?? 'owner';
  const session = makeSession(role);
  const bot = makeBot(options?.botOwnerId ?? 'user-1');

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
      if (url === '/api/workspaces/ws-1/approvals') {
        return Promise.resolve(jsonResponse({ approvals: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
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
      if (url === '/api/workspaces/ws-1/bots/bot-1/runtime') {
        return Promise.resolve(jsonResponse({ paused: false }, 200));
      }
      if (url.startsWith('/api/workspaces/ws-1/budgets')) {
        return Promise.resolve(
          jsonResponse(
            {
              spendTodayMicros: 3050000,
              budgets: [
                {
                  id: 'b-day',
                  period: 'day',
                  limitMicros: 5000000,
                  spentMicros: 3050000,
                  warning: false,
                  exceeded: false,
                },
              ],
              subscriptionUsage: [],
            },
            200,
          ),
        );
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/gitlab-instances') {
        return Promise.resolve(jsonResponse({ instances: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/mcp') {
        return Promise.resolve(jsonResponse({ servers: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/mcp/logs') {
        return Promise.resolve(jsonResponse({ entries: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/secrets') {
        return Promise.resolve(jsonResponse({ secrets: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/egress') {
        return Promise.resolve(
          jsonResponse(
            {
              mode: 'allowlist',
              allowedHosts: ['api.anthropic.com'],
              blockedHosts: [],
              effectiveSecretHosts: [],
              enforcementStatus: 'applied',
            },
            200,
          ),
        );
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/egress/logs')) {
        return Promise.resolve(jsonResponse({ logs: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1/codex-plan') {
        return Promise.resolve(
          jsonResponse({ status: 'disconnected', ownedByYou: false }, 200),
        );
      }
      if (url.startsWith('/api/workspaces/ws-1/bots/bot-1/memories')) {
        return Promise.resolve(
          jsonResponse(
            {
              summary: {
                autoSave: true,
                totalCount: 0,
                alwaysCount: 0,
                onDemandCount: 0,
                alwaysTokens: 0,
                alwaysTokenLimit: 4000,
                maxEntries: 200,
              },
              entries: [],
              filteredCount: 0,
              tags: [],
            },
            200,
          ),
        );
      }
      return Promise.resolve(jsonResponse({}, 404));
    },
  );

  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

describe('BotSettingsShell and all four bot settings sub-pages', () => {
  beforeEach(() => {
    mockPathname = '/bots/bot-1/settings';
    mockParams = { botId: 'bot-1' };
    setAccessToken('jwt-bot-settings-test');
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('renders General & runtime focused without Budgets, MCP, or GitLab clutter', async () => {
    stubSettingsFetch();

    renderWithProviders(<BotSettingsPage />);

    const nav = await screen.findByRole('navigation', {
      name: enMessages.builder.tabs.ariaLabel,
    });
    expect(nav).toBeInTheDocument();

    const generalLink = screen.getByRole('link', {
      name: enMessages.botSettings.nav.general,
    });
    expect(generalLink).toHaveAttribute('aria-current', 'page');
    expect(generalLink).toHaveAttribute('href', '/bots/bot-1/settings');

    expect(
      screen.getByRole('heading', { name: enMessages.runner.settingsTitle }),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText(enMessages.builder.descriptionLabel),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: /bot isolation/i }),
    ).not.toBeInTheDocument();

    // Uncluttered separation: other sub-pages must not appear inline here.
    expect(
      screen.queryByRole('heading', { name: enMessages.budgets.botTitle }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: enMessages.mcpProxy.botTitle }),
    ).not.toBeInTheDocument();
  });

  it('renders Budgets & usage inside BotSettingsShell with active nav state', async () => {
    mockPathname = '/bots/bot-1/settings/budgets';
    stubSettingsFetch();

    renderWithProviders(<BotBudgetsSettingsPage />);

    const budgetsLink = await screen.findByRole('link', {
      name: enMessages.botSettings.nav.budgets,
    });
    expect(budgetsLink).toHaveAttribute('aria-current', 'page');
    expect(budgetsLink).toHaveAttribute('href', '/bots/bot-1/settings/budgets');
    expect(
      await screen.findByRole('heading', { name: enMessages.budgets.botTitle }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: enMessages.budgets.saveBotLimits }),
    ).toBeInTheDocument();
  });

  it('renders MCP & GitLab inside BotSettingsShell with active nav state', async () => {
    mockPathname = '/bots/bot-1/settings/integrations';
    stubSettingsFetch();

    renderWithProviders(<BotIntegrationsSettingsPage />);

    const integrationsLink = await screen.findByRole('link', {
      name: enMessages.botSettings.nav.integrations,
    });
    expect(integrationsLink).toHaveAttribute('aria-current', 'page');
    expect(integrationsLink).toHaveAttribute(
      'href',
      '/bots/bot-1/settings/integrations',
    );
    expect(
      await screen.findByRole('heading', {
        name: enMessages.mcpProxy.botTitle,
      }),
    ).toBeInTheDocument();
  });

  it('renders Knowledge base inside BotSettingsShell with active nav state', async () => {
    mockPathname = '/bots/bot-1/settings/knowledge';
    stubSettingsFetch();

    renderWithProviders(<BotKnowledgeSettingsPage />);

    const knowledgeLink = await screen.findByRole('link', {
      name: enMessages.botSettings.nav.knowledge,
    });
    expect(knowledgeLink).toHaveAttribute('aria-current', 'page');
    expect(knowledgeLink).toHaveAttribute(
      'href',
      '/bots/bot-1/settings/knowledge',
    );
    expect(
      await screen.findByRole('heading', {
        name: enMessages.botSettings.knowledge.policyTitle,
      }),
    ).toBeInTheDocument();
  });

  it('renders Secrets & egress inside BotSettingsShell with CLI tabs, Network section, and no MCP/Budgets bleed', async () => {
    mockPathname = '/bots/bot-1/secrets';
    stubSettingsFetch();

    renderWithProviders(<BotSecretsPage />);

    const secretsLink = await screen.findByRole('link', {
      name: enMessages.botSettings.nav.secrets,
    });
    expect(secretsLink).toHaveAttribute('aria-current', 'page');
    expect(secretsLink).toHaveAttribute('href', '/bots/bot-1/secrets');
    expect(
      await screen.findByText('Atlas · Secrets & network'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /manage the credentials your bot can use and control which services it can connect to/i,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/a protected push asks/i),
    ).not.toBeInTheDocument();

    // Dedicated sub-sections are visible across the full content column.
    expect(
      await screen.findByRole('heading', {
        name: enMessages.secrets.cliToolsTitle,
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: enMessages.secrets.sectionTitle }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: enMessages.secrets.networkTitle }),
    ).toBeInTheDocument();

    // MCP & Budgets sections must stay on their own Bot Settings sub-pages.
    expect(
      screen.queryByRole('heading', { name: enMessages.mcpProxy.botTitle }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: enMessages.budgets.botTitle }),
    ).not.toBeInTheDocument();

    // CLI tools tabs switch cleanly between Claude Pro / Max and ChatGPT Codex plan.
    const claudeTab = screen.getByRole('tab', {
      name: enMessages.secrets.cliToolsTabClaude,
    });
    const codexTab = screen.getByRole('tab', {
      name: enMessages.secrets.cliToolsTabCodex,
    });
    expect(claudeTab).toHaveAttribute('aria-selected', 'true');
    expect(codexTab).toHaveAttribute('aria-selected', 'false');
    expect(
      screen.getByRole('button', {
        name: enMessages.secrets.claudeSubscriptionConfigure,
      }),
    ).toBeInTheDocument();

    fireEvent.click(codexTab);
    expect(codexTab).toHaveAttribute('aria-selected', 'true');
    expect(claudeTab).toHaveAttribute('aria-selected', 'false');
    expect(
      await screen.findByRole('heading', { name: enMessages.codexPlan.title }),
    ).toBeInTheDocument();
    expect(
      await screen.findByRole('button', { name: enMessages.codexPlan.connect }),
    ).toBeInTheDocument();
  });

  it('keeps CLI tools tabs read-only for viewers without edit permissions', async () => {
    mockPathname = '/bots/bot-1/secrets';
    stubSettingsFetch({ role: 'viewer', botOwnerId: 'other-user' });

    renderWithProviders(<BotSecretsPage />);

    expect(
      await screen.findByText(enMessages.secrets.cliToolsReadOnly),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', {
        name: enMessages.secrets.claudeSubscriptionConfigure,
      }),
    ).not.toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('tab', { name: enMessages.secrets.cliToolsTabCodex }),
    );
    expect(
      screen.getByText(enMessages.secrets.cliToolsReadOnly),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: enMessages.codexPlan.connect }),
    ).not.toBeInTheDocument();
  });

  it('keeps controls read-only for viewers across sections and isolates cross-workspace 404s', async () => {
    mockPathname = '/bots/bot-1/settings/budgets';
    stubSettingsFetch({ role: 'viewer', botOwnerId: 'other-user' });

    const { unmount } = renderWithProviders(<BotBudgetsSettingsPage />);

    expect(
      await screen.findByText(enMessages.botSettings.readOnly),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: enMessages.budgets.saveBotLimits }),
    ).not.toBeInTheDocument();
    unmount();

    // Cross-workspace bot lookup returns 404 and surfaces botNotFound without leaking data.
    mockPathname = '/bots/bot-1/settings/integrations';
    stubSettingsFetch({ botStatus: 404 });
    renderWithProviders(<BotIntegrationsSettingsPage />);

    await waitFor(() => {
      expect(
        screen.getByText(enMessages.errors.botNotFound),
      ).toBeInTheDocument();
    });
    expect(
      screen.queryByRole('navigation', {
        name: enMessages.builder.tabs.ariaLabel,
      }),
    ).not.toBeInTheDocument();
  });
});
