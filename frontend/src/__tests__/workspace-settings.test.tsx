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

import BudgetsSettingsPage from '@/app/settings/budgets/page';
import GeneralSettingsPage from '@/app/settings/general/page';
import IntegrationsSettingsPage from '@/app/settings/integrations/page';
import WorkspaceSecretsSettingsPage from '@/app/settings/secrets/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  ModelPrice,
  SecretMetadata,
  WorkspaceMember,
  WorkspaceRole,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../messages/en.json';

const mockPush = vi.fn();
const mockReplace = vi.fn();
let mockPathname = '/settings/general';

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
    screenshotRetentionDays: 7,
    createdAt: '2026-11-14T12:00:00Z',
    ...overrides,
  };
}

function makeSession(
  workspaces: WorkspaceSummary[] = [
    makeWorkspace(),
    makeWorkspace({
      id: 'ws-2',
      name: 'Beta Lab',
      slug: 'beta-lab',
      role: 'owner',
    }),
  ],
): AuthSessionResponse {
  return {
    user: {
      id: 'user-1',
      email: 'jan@dunkel.tech',
      displayName: 'Jan Gehring',
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces,
    activeWorkspace: workspaces[0],
    accessToken: 'jwt-settings-test',
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
  mockPathname = '/settings/general';
  setAccessToken(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('Workspace Settings IA & routes', () => {
  it('renders left sub-navigation and updates profile, runtime, ownership, and workspace deletion for owners', async () => {
    mockPathname = '/settings/general';
    const ws1 = makeWorkspace();
    const ws2 = makeWorkspace({
      id: 'ws-2',
      name: 'Beta Lab',
      slug: 'beta-lab',
    });
    const session = makeSession([ws1, ws2]);
    let currentWorkspaces = [ws1, ws2];
    let members: WorkspaceMember[] = [
      {
        workspaceId: 'ws-1',
        userId: 'user-1',
        email: 'jan@dunkel.tech',
        displayName: 'Jan Gehring',
        locale: 'en',
        role: 'owner',
        joinedAt: '2026-11-14T12:00:00Z',
      },
      {
        workspaceId: 'ws-1',
        userId: 'user-2',
        email: 'mara@dunkel.tech',
        displayName: 'Mara Lindqvist',
        locale: 'en',
        role: 'admin',
        joinedAt: '2026-11-14T13:00:00Z',
      },
    ];
    let deleteAttempts = 0;

    const fetchSpy = installFetchMock((url, method, body) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces' && method === 'GET') {
        return jsonResponse({ workspaces: currentWorkspaces }, 200);
      }
      if (url === '/api/workspaces/ws-1/members' && method === 'GET') {
        return jsonResponse({ members }, 200);
      }
      if (url === '/api/workspaces/ws-1' && method === 'PATCH') {
        const parsed = JSON.parse(body ?? '{}') as Partial<WorkspaceSummary>;
        const updated = makeWorkspace({ ...currentWorkspaces[0], ...parsed });
        currentWorkspaces = [updated, ws2];
        return jsonResponse(updated, 200);
      }
      if (url === '/api/workspaces/ws-1/members/user-2' && method === 'PATCH') {
        members = members.map((m) =>
          m.userId === 'user-2' ? { ...m, role: 'owner' } : m,
        );
        return jsonResponse(members[1], 200);
      }
      if (url === '/api/workspaces/ws-1' && method === 'DELETE') {
        deleteAttempts += 1;
        if (deleteAttempts === 1) {
          return jsonResponse(
            {
              error: {
                code: 'workspace.busy',
                message: 'active bot work',
              },
            },
            409,
          );
        }
        currentWorkspaces = [ws2];
        return new Response(null, { status: 204 });
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<GeneralSettingsPage />);

    const subNav = await screen.findByRole('navigation', {
      name: enMessages.workspaceSettings.navAria,
    });
    expect(
      within(subNav).getByRole('link', {
        name: enMessages.workspaceSettings.nav.general,
      }),
    ).toHaveAttribute('aria-current', 'page');
    expect(
      within(subNav).getByRole('link', {
        name: enMessages.workspaceSettings.nav.secrets,
      }),
    ).toHaveAttribute('href', '/settings/secrets');
    expect(
      screen.queryByRole('heading', { name: /tenant isolation: strict/i }),
    ).not.toBeInTheDocument();

    const nameInput = await screen.findByLabelText(
      enMessages.workspaceSettings.general.nameLabel,
    );
    fireEvent.change(nameInput, { target: { value: 'dunkel.Tech Core' } });
    fireEvent.change(
      screen.getByLabelText(enMessages.workspaceSettings.general.localeLabel),
      { target: { value: 'de' } },
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspaceSettings.general.saveProfile,
      }),
    );

    await screen.findByText(enMessages.workspaceSettings.general.savedProfile);

    fireEvent.click(
      screen.getByRole('radio', {
        name: new RegExp(enMessages.workspaceSettings.general.driverK8sTitle),
      }),
    );
    const retentionSlider = screen.getByRole('slider', {
      name: enMessages.workspaceSettings.general.retentionLabel,
    });
    fireEvent.change(retentionSlider, { target: { value: '14' } });
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspaceSettings.general.saveRuntime,
      }),
    );

    await screen.findByText(enMessages.workspaceSettings.general.savedRuntime);

    const patchCalls = fetchSpy.mock.calls.filter(
      ([u, init]) =>
        u === '/api/workspaces/ws-1' &&
        (init?.method ?? 'GET').toUpperCase() === 'PATCH',
    );
    expect(patchCalls).toHaveLength(2);
    expect(JSON.parse(patchCalls[0]?.[1]?.body as string)).toEqual({
      name: 'dunkel.Tech Core',
      defaultLocale: 'de',
    });
    expect(JSON.parse(patchCalls[1]?.[1]?.body as string)).toEqual({
      defaultDriver: 'kubernetes',
      screenshotRetentionDays: 14,
    });

    // Ownership transfer
    await waitFor(() => {
      expect(
        screen.getByLabelText(
          enMessages.workspaceSettings.danger.transferSelectLabel,
        ),
      ).toHaveValue('user-2');
    });
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspaceSettings.danger.transferSubmit,
      }),
    );
    await screen.findByText(
      'Mara Lindqvist is now an Owner of this workspace.',
    );

    // Delete workspace modal: busy error first, then clean deletion.
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspaceSettings.danger.deleteButton,
      }),
    );
    const dialog = await screen.findByRole('dialog');
    const confirmButton = within(dialog).getByRole('button', {
      name: enMessages.workspaceSettings.danger.confirmDelete,
    });
    expect(confirmButton).toBeDisabled();

    fireEvent.change(
      within(dialog).getByLabelText(
        'Type the workspace slug (dunkel-tech) to confirm',
      ),
      { target: { value: 'dunkel-tech' } },
    );
    expect(confirmButton).toBeEnabled();
    fireEvent.click(confirmButton);

    expect(
      await within(dialog).findByText(enMessages.errors.workspaceBusy),
    ).toBeInTheDocument();

    fireEvent.click(confirmButton);
    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith('/');
    });
  });

  it.each(['admin', 'member', 'viewer'] as WorkspaceRole[])(
    'keeps General settings read-only and hides Danger zone for %s',
    async (role) => {
      mockPathname = '/settings/general';
      const session = makeSession([makeWorkspace({ role })]);
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
        if (url.endsWith('/approvals')) {
          return jsonResponse({ approvals: [] }, 200);
        }
        return jsonResponse({}, 404);
      });

      renderWithProviders(<GeneralSettingsPage />);

      expect(
        await screen.findByText(
          enMessages.workspaceSettings.general.readOnlyNotice,
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByLabelText(enMessages.workspaceSettings.general.nameLabel),
      ).toBeDisabled();
      expect(
        screen.queryByRole('heading', {
          name: enMessages.workspaceSettings.danger.title,
        }),
      ).not.toBeInTheDocument();
    },
  );

  it('disables workspace deletion on General settings when only one workspace exists', async () => {
    mockPathname = '/settings/general';
    const session = makeSession([makeWorkspace({ role: 'owner' })]);
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
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url.endsWith('/members')) {
        return jsonResponse({ members: [] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<GeneralSettingsPage />);

    const deleteBtn = await screen.findByRole('button', {
      name: enMessages.workspaceSettings.danger.deleteButton,
    });
    expect(deleteBtn).toBeDisabled();
    expect(
      screen.getByText(
        enMessages.workspaceSettings.danger.deleteLastWorkspaceHint,
      ),
    ).toBeInTheDocument();
  });

  it('lists, edits, and saves model_prices on /settings/budgets', async () => {
    mockPathname = '/settings/budgets';
    const session = makeSession([makeWorkspace({ role: 'admin' })]);
    const initialPrices: ModelPrice[] = [
      {
        provider: 'anthropic',
        model: 'claude-sonnet-4',
        inputMicrosPerMillion: 3_000_000,
        outputMicrosPerMillion: 15_000_000,
        cacheMicrosPerMillion: 300_000,
      },
    ];
    let putErrorNext = true;

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
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/budgets') {
        return jsonResponse({ spendTodayMicros: 0, budgets: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/model-prices' && method === 'GET') {
        return jsonResponse({ prices: initialPrices }, 200);
      }
      if (url === '/api/workspaces/ws-1/model-prices' && method === 'PUT') {
        if (putErrorNext) {
          putErrorNext = false;
          return jsonResponse(
            { error: { code: 'validation_error', message: 'bad price' } },
            400,
          );
        }
        return jsonResponse({ saved: true }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BudgetsSettingsPage />);

    expect(await screen.findByText('claude-sonnet-4')).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspaceSettings.modelPrices.editAction,
      }),
    );

    const modelInput = screen.getByLabelText<HTMLInputElement>(
      enMessages.members.modelPriceModel,
    );
    expect(modelInput.value).toBe('claude-sonnet-4');

    fireEvent.change(
      screen.getByLabelText(enMessages.members.modelPriceInput),
      { target: { value: '4.50' } },
    );
    fireEvent.click(
      screen.getByRole('button', { name: enMessages.members.modelPriceSave }),
    );
    await screen.findByText(enMessages.members.modelPriceError);

    fireEvent.click(
      screen.getByRole('button', { name: enMessages.members.modelPriceSave }),
    );
    await screen.findByText(enMessages.members.modelPriceSaved);
    expect(screen.getByText('$4.50')).toBeInTheDocument();
  });

  it('creates, updates, and deletes shared workspace secrets on /settings/secrets without leaking values', async () => {
    mockPathname = '/settings/secrets';
    const session = makeSession([makeWorkspace({ role: 'owner' })]);
    const existing: SecretMetadata = {
      id: 'sec-1',
      workspaceId: 'ws-1',
      botId: null,
      scope: 'workspace',
      name: 'ANTHROPIC_ORG_KEY',
      domainMatch: 'api.anthropic.com',
      matchSubdomains: false,
      pathPrefix: '',
      authScheme: 'bearer',
      headerName: 'Authorization',
      username: '',
      maskedPreview: 'sk-ant-••••9f2a',
      usedCount: 12,
      lastUsedAt: null,
      createdAt: '2026-11-16T10:00:00Z',
      updatedAt: '2026-11-16T10:00:00Z',
    };
    const created: SecretMetadata = {
      id: 'sec-2',
      workspaceId: 'ws-1',
      botId: null,
      scope: 'workspace',
      name: 'OPENAI_API_KEY',
      domainMatch: 'api.openai.com',
      matchSubdomains: false,
      pathPrefix: '/v1',
      authScheme: 'bearer',
      headerName: 'Authorization',
      username: '',
      maskedPreview: 'sk-open••••1234',
      usedCount: 0,
      lastUsedAt: null,
      createdAt: '2026-11-18T10:00:00Z',
      updatedAt: '2026-11-18T10:00:00Z',
    };
    let conflictFirstCreate = true;

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
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/secrets' && method === 'GET') {
        return jsonResponse({ secrets: [existing] }, 200);
      }
      if (url === '/api/workspaces/ws-1/secrets' && method === 'POST') {
        if (conflictFirstCreate) {
          conflictFirstCreate = false;
          return jsonResponse(
            {
              error: {
                code: 'secret.already_exists',
                message: 'duplicate secret',
              },
            },
            409,
          );
        }
        return jsonResponse(created, 201);
      }
      if (url === '/api/workspaces/ws-1/secrets/sec-1' && method === 'PUT') {
        return jsonResponse(
          { ...existing, domainMatch: 'api-eu.anthropic.com' },
          200,
        );
      }
      if (url === '/api/workspaces/ws-1/secrets/sec-2' && method === 'DELETE') {
        return new Response(null, { status: 204 });
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<WorkspaceSecretsSettingsPage />);

    expect(await screen.findByText('ANTHROPIC_ORG_KEY')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(enMessages.secrets.nameLabel), {
      target: { value: 'OPENAI_API_KEY' },
    });
    fireEvent.change(screen.getByLabelText(enMessages.secrets.domainLabel), {
      target: { value: 'api.openai.com' },
    });
    fireEvent.change(
      screen.getByLabelText(enMessages.secrets.pathPrefixLabel),
      { target: { value: '/v1' } },
    );
    const valueInput = screen.getByLabelText<HTMLInputElement>(
      enMessages.secrets.valueCreateLabel,
    );
    fireEvent.change(valueInput, {
      target: { value: 'sk-super-secret-plaintext-1234' },
    });

    fireEvent.click(
      screen.getByRole('button', {
        name: new RegExp(enMessages.secrets.createSubmit),
      }),
    );
    await screen.findByText(enMessages.errors.secretAlreadyExists);

    fireEvent.click(
      screen.getByRole('button', {
        name: new RegExp(enMessages.secrets.createSubmit),
      }),
    );
    expect(await screen.findByText('OPENAI_API_KEY')).toBeInTheDocument();
    expect(
      screen.queryByText('sk-super-secret-plaintext-1234'),
    ).not.toBeInTheDocument();
    expect(valueInput.value).toBe('');

    // Edit existing secret
    const row1 = screen.getByText('ANTHROPIC_ORG_KEY').closest('tr');
    expect(row1).not.toBeNull();
    fireEvent.click(
      within(row1 as HTMLElement).getByRole('button', {
        name: enMessages.secrets.editAction,
      }),
    );
    fireEvent.change(screen.getByLabelText(enMessages.secrets.domainLabel), {
      target: { value: 'api-eu.anthropic.com' },
    });
    fireEvent.click(
      screen.getByRole('button', {
        name: new RegExp(enMessages.secrets.updateSubmit),
      }),
    );
    expect(await screen.findByText('api-eu.anthropic.com')).toBeInTheDocument();

    // Two-step delete
    const row2 = screen.getByText('OPENAI_API_KEY').closest('tr');
    expect(row2).not.toBeNull();
    fireEvent.click(
      within(row2 as HTMLElement).getByRole('button', {
        name: enMessages.secrets.deleteAction,
      }),
    );
    fireEvent.click(
      within(row2 as HTMLElement).getByRole('button', {
        name: enMessages.secrets.deleteConfirmAction,
      }),
    );
    await waitFor(() => {
      expect(screen.queryByText('OPENAI_API_KEY')).not.toBeInTheDocument();
    });
  });

  it('keeps /settings/secrets read-only for members and viewers', async () => {
    mockPathname = '/settings/secrets';
    const session = makeSession([makeWorkspace({ role: 'member' })]);
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
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/secrets') {
        return jsonResponse({ secrets: [] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<WorkspaceSecretsSettingsPage />);

    expect(
      await screen.findByText(
        enMessages.workspaceSettings.secrets.readOnlyNotice,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByLabelText(enMessages.secrets.nameLabel),
    ).not.toBeInTheDocument();
  });

  it('renders MCP servers and self-hosted GitLab together on /settings/integrations', async () => {
    mockPathname = '/settings/integrations';
    const session = makeSession([makeWorkspace({ role: 'owner' })]);
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
      if (url.endsWith('/approvals')) {
        return jsonResponse({ approvals: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/mcp-servers') {
        return jsonResponse({ servers: [] }, 200);
      }
      if (url === '/api/workspaces/ws-1/gitlab-instances') {
        return jsonResponse({ instances: [] }, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<IntegrationsSettingsPage />);

    expect(
      await screen.findByText(enMessages.mcpProxy.registryTitle),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(enMessages.gitlabInstances.title),
    ).toBeInTheDocument();
  });
});
